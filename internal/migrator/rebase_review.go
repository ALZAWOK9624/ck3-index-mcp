package migrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ck3-index/internal/indexer"
)

const (
	rebaseReviewMaxRequestBytes = 1 << 20
	rebaseReviewMaxManualBytes  = 64 << 20
	rebaseReviewMaxCommentRunes = 4096
)

// RebaseReviewServer is an isolated local HTTP server for one rebase
// transaction. It deliberately exposes neither source trees nor arbitrary
// artifact files: only the generated report and the transaction's JSON APIs
// are routed.
type RebaseReviewServer struct {
	URL   string
	Token string

	server   *http.Server
	listener net.Listener
	done     chan struct{}
	serveErr error

	closeOnce sync.Once
	closeErr  error
}

// Close stops the local review server. It is safe to call more than once.
func (review *RebaseReviewServer) Close() error {
	if review == nil || review.server == nil {
		return nil
	}
	review.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		review.closeErr = review.server.Shutdown(ctx)
		if review.closeErr != nil && !errors.Is(review.closeErr, http.ErrServerClosed) {
			_ = review.server.Close()
		}
		if errors.Is(review.closeErr, http.ErrServerClosed) {
			review.closeErr = nil
		}
	})
	return review.closeErr
}

// Wait keeps a command alive while its review server is serving. A cancelled
// context performs a graceful local shutdown rather than leaving a listener
// behind.
func (review *RebaseReviewServer) Wait(ctx context.Context) error {
	if review == nil {
		return fmt.Errorf("rebase review server is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-review.done:
		return review.serveErr
	case <-ctx.Done():
		return review.Close()
	}
}

// StartRebaseReview starts a review server bound exclusively to a loopback
// address. The caller should print URL and then call Wait with its command
// context. The report is refreshed before the listener is exposed.
func StartRebaseReview(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions, addr string) (*RebaseReviewServer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := rebaseArtifactRoot(cfg, opts)
	if err != nil {
		return nil, err
	}
	transaction, resolutions, err := loadRebaseReviewData(root, id)
	if err != nil {
		return nil, err
	}
	transaction.Resolutions = resolutions
	// A pixel conflict count alone is not enough evidence to review a map
	// change.  When all three source files still match the transaction hashes,
	// materialize a transparent PNG mask containing the *actual* conflicting
	// coordinates.  Failure to re-read or re-validate a source intentionally
	// leaves the mask absent; it must never be guessed from a count or from a
	// later, drifted upstream file.
	_ = materializeRebaseReviewPixelMasks(cfg, root, transaction)
	if err := writeRebaseReport(root, transaction); err != nil {
		return nil, err
	}

	listenAddr, err := normalizeRebaseReviewAddress(addr)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	token, err := newRebaseReviewToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	review := &RebaseReviewServer{
		URL:      "http://" + listener.Addr().String() + "/?token=" + token,
		Token:    token,
		listener: listener,
		done:     make(chan struct{}),
	}
	review.server = &http.Server{
		Handler:           newRebaseReviewHandler(root, id, token),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		err := review.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		review.serveErr = err
		close(review.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = review.Close()
		case <-review.done:
		}
	}()
	return review, nil
}

// ServeRebaseReview is a convenience wrapper for callers which only need the
// URL and retain the supplied context for the life of the process. Command
// implementations should prefer StartRebaseReview followed by Wait so the URL
// can be displayed before blocking.
func ServeRebaseReview(ctx context.Context, cfg indexer.Config, id string, opts RebaseOptions, addr string) (string, error) {
	review, err := StartRebaseReview(ctx, cfg, id, opts, addr)
	if err != nil {
		return "", err
	}
	return review.URL, nil
}

func normalizeRebaseReviewAddress(addr string) (string, error) {
	value := strings.TrimSpace(addr)
	if value == "" {
		return "127.0.0.1:0", nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(port) == "" {
		return "", fmt.Errorf("review address must be an explicit loopback host and port: %q", addr)
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return "", fmt.Errorf("review address must bind only a loopback address: %q", addr)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func newRebaseReviewToken() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func newRebaseReviewHandler(root, id, token string) http.Handler {
	var resolutionMu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		setRebaseReviewHeaders(writer)
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(writer, http.MethodGet+", "+http.MethodHead)
			return
		}
		if request.URL.Path != "/" && request.URL.Path != "/report.html" {
			http.NotFound(writer, request)
			return
		}
		data, err := readRebaseReport(root, id)
		if err != nil {
			http.Error(writer, "review report is unavailable", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.Method != http.MethodHead {
			_, _ = writer.Write(data)
		}
	})
	mux.HandleFunc("/api/transaction", func(writer http.ResponseWriter, request *http.Request) {
		setRebaseReviewHeaders(writer)
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		transaction, resolutions, err := loadRebaseReviewData(root, id)
		if err != nil {
			http.Error(writer, "transaction is unavailable", http.StatusInternalServerError)
			return
		}
		transaction.Resolutions = resolutions
		writeRebaseReviewJSON(writer, http.StatusOK, transaction)
	})
	mux.HandleFunc("/api/resolutions", func(writer http.ResponseWriter, request *http.Request) {
		setRebaseReviewHeaders(writer)
		switch request.Method {
		case http.MethodGet:
			resolutions, err := loadRebaseResolutions(root, id)
			if err != nil {
				http.Error(writer, "resolutions are unavailable", http.StatusInternalServerError)
				return
			}
			writeRebaseReviewJSON(writer, http.StatusOK, resolutions)
		case http.MethodPost:
			if !rebaseReviewCapabilityAllowed(request, token) {
				http.Error(writer, "review capability token is required", http.StatusForbidden)
				return
			}
			resolutionMu.Lock()
			defer resolutionMu.Unlock()
			resolutions, err := decodeRebaseResolutionRequest(writer, request)
			if err != nil {
				status := http.StatusBadRequest
				var maxBytes *http.MaxBytesError
				if errors.As(err, &maxBytes) {
					status = http.StatusRequestEntityTooLarge
				}
				http.Error(writer, err.Error(), status)
				return
			}
			transaction, _, err := loadRebaseReviewData(root, id)
			if err != nil {
				http.Error(writer, "transaction is unavailable", http.StatusInternalServerError)
				return
			}
			resolutions, err = validateRebaseReviewResolutions(transaction, resolutions)
			if err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			if err := writeRebaseResolutions(root, id, resolutions); err != nil {
				http.Error(writer, "could not save resolutions", http.StatusInternalServerError)
				return
			}
			transaction.Resolutions = resolutions
			if err := writeRebaseReport(root, transaction); err != nil {
				http.Error(writer, "could not refresh review report", http.StatusInternalServerError)
				return
			}
			writeRebaseReviewJSON(writer, http.StatusOK, resolutions)
		default:
			methodNotAllowed(writer, http.MethodGet+", "+http.MethodPost)
		}
	})
	mux.HandleFunc("/api/manual", func(writer http.ResponseWriter, request *http.Request) {
		setRebaseReviewHeaders(writer)
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		if !rebaseReviewCapabilityAllowed(request, token) {
			http.Error(writer, "review capability token is required", http.StatusForbidden)
			return
		}
		resolutionMu.Lock()
		defer resolutionMu.Unlock()
		result, err := storeRebaseManualUpload(writer, request, root, id)
		if err != nil {
			status := http.StatusBadRequest
			var maxBytes *http.MaxBytesError
			if errors.As(err, &maxBytes) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(writer, err.Error(), status)
			return
		}
		writeRebaseReviewJSON(writer, http.StatusOK, result)
	})
	mux.HandleFunc("/pixel-conflicts/", func(writer http.ResponseWriter, request *http.Request) {
		setRebaseReviewHeaders(writer)
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(writer, http.MethodGet+", "+http.MethodHead)
			return
		}
		transaction, _, err := loadRebaseReviewData(root, id)
		if err != nil {
			http.Error(writer, "transaction is unavailable", http.StatusInternalServerError)
			return
		}
		relative := strings.TrimPrefix(request.URL.Path, "/")
		for _, conflict := range transaction.Conflicts {
			if conflict.Code != "pixel_merge_conflict" {
				continue
			}
			expected, err := rebaseReviewPixelMaskRelativePath(conflict.ID)
			if err != nil || relative != expected {
				continue
			}
			data, err := readRebaseReviewPixelMask(root, id, conflict.ID)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					http.NotFound(writer, request)
					return
				}
				http.Error(writer, "pixel conflict evidence is unavailable", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "image/png")
			writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(data)
			}
			return
		}
		http.NotFound(writer, request)
	})
	mux.HandleFunc("/map-deltas/", func(writer http.ResponseWriter, request *http.Request) {
		setRebaseReviewHeaders(writer)
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(writer, http.MethodGet+", "+http.MethodHead)
			return
		}
		transaction, _, err := loadRebaseReviewData(root, id)
		if err != nil {
			http.Error(writer, "transaction is unavailable", http.StatusInternalServerError)
			return
		}
		relative := strings.TrimPrefix(request.URL.Path, "/")
		for _, decision := range transaction.Files {
			if decision.MapDelta == nil || relative != decision.MapDelta.Path {
				continue
			}
			data, err := readRebaseReviewMapDelta(root, id, *decision.MapDelta)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					http.NotFound(writer, request)
					return
				}
				http.Error(writer, "map delta evidence is unavailable", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "image/png")
			writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(data)
			}
			return
		}
		http.NotFound(writer, request)
	})
	return mux
}

func rebaseReviewCapabilityAllowed(request *http.Request, token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	provided := strings.TrimSpace(request.Header.Get("X-CK3-Rebase-Token"))
	return len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func methodNotAllowed(writer http.ResponseWriter, allow string) {
	writer.Header().Set("Allow", allow)
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}

func loadRebaseReviewData(root, id string) (RebaseTransaction, []RebaseResolution, error) {
	transaction, err := loadRebaseTransactionFromRoot(root, id)
	if err != nil {
		return RebaseTransaction{}, nil, err
	}
	resolutions, err := loadRebaseResolutions(root, id)
	if err != nil {
		return RebaseTransaction{}, nil, err
	}
	return transaction, resolutions, nil
}

func readRebaseReport(root, id string) ([]byte, error) {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, rebaseReportName)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rebase report is not a regular file")
	}
	return os.ReadFile(path)
}

func decodeRebaseResolutionRequest(writer http.ResponseWriter, request *http.Request) ([]RebaseResolution, error) {
	data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, rebaseReviewMaxRequestBytes))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("resolution request body is required")
	}
	var resolutions []RebaseResolution
	switch trimmed[0] {
	case '[':
		if err := decodeRebaseReviewJSON(trimmed, &resolutions); err != nil {
			return nil, fmt.Errorf("invalid resolutions: %w", err)
		}
	case '{':
		var payload struct {
			Resolutions []RebaseResolution `json:"resolutions"`
		}
		if err := decodeRebaseReviewJSON(trimmed, &payload); err != nil {
			return nil, fmt.Errorf("invalid resolution request: %w", err)
		}
		resolutions = payload.Resolutions
	default:
		return nil, fmt.Errorf("resolutions must be a JSON array or object with a resolutions field")
	}
	if resolutions == nil {
		return nil, fmt.Errorf("resolutions must be a JSON array")
	}
	return resolutions, nil
}

type rebaseManualUploadResult struct {
	ConflictID string `json:"conflict_id"`
	ManualPath string `json:"manual_path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

// storeRebaseManualUpload accepts one human-authored candidate into the
// transaction-owned manual directory. The browser never supplies a path: the
// conflict id determines the stable, safe artifact name and the response
// supplies the exact SHA-256 which must accompany the later resolution.
func storeRebaseManualUpload(writer http.ResponseWriter, request *http.Request, root, id string) (rebaseManualUploadResult, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, rebaseReviewMaxManualBytes)
	if err := request.ParseMultipartForm(rebaseReviewMaxManualBytes); err != nil {
		return rebaseManualUploadResult{}, err
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	conflictID := strings.TrimSpace(request.FormValue("conflict_id"))
	transaction, _, err := loadRebaseReviewData(root, id)
	if err != nil {
		return rebaseManualUploadResult{}, fmt.Errorf("transaction is unavailable")
	}
	var conflict *RebaseConflict
	for index := range transaction.Conflicts {
		if transaction.Conflicts[index].ID == conflictID {
			conflict = &transaction.Conflicts[index]
			break
		}
	}
	if conflict == nil || !rebaseReviewActionAllowed(conflict.AllowedActions, "manual") {
		return rebaseManualUploadResult{}, fmt.Errorf("conflict does not accept a manual candidate")
	}
	file, _, err := request.FormFile("file")
	if err != nil {
		return rebaseManualUploadResult{}, fmt.Errorf("manual candidate file is required")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, rebaseReviewMaxManualBytes+1))
	if err != nil {
		return rebaseManualUploadResult{}, err
	}
	if len(data) == 0 {
		return rebaseManualUploadResult{}, fmt.Errorf("manual candidate file is empty")
	}
	if int64(len(data)) > rebaseReviewMaxManualBytes {
		return rebaseManualUploadResult{}, fmt.Errorf("manual candidate exceeds %d byte limit", rebaseReviewMaxManualBytes)
	}
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return rebaseManualUploadResult{}, err
	}
	nameHash := sha256.Sum256([]byte(conflict.ID))
	manualPath := filepath.ToSlash(filepath.Join("manual", hex.EncodeToString(nameHash[:])+".candidate"))
	full := filepath.Join(dir, filepath.FromSlash(manualPath))
	if !pathContains(dir, full) {
		return rebaseManualUploadResult{}, fmt.Errorf("manual candidate path escapes transaction")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return rebaseManualUploadResult{}, err
	}
	if err := writeRebaseReviewFileAtomic(full, data); err != nil {
		return rebaseManualUploadResult{}, err
	}
	hash := hashBytes(data)
	return rebaseManualUploadResult{ConflictID: conflict.ID, ManualPath: manualPath, SHA256: hash, Size: int64(len(data))}, nil
}

func decodeRebaseReviewJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func validateRebaseReviewResolutions(transaction RebaseTransaction, resolutions []RebaseResolution) ([]RebaseResolution, error) {
	conflicts := make(map[string]RebaseConflict, len(transaction.Conflicts))
	for _, conflict := range transaction.Conflicts {
		conflicts[conflict.ID] = conflict
	}
	seen := make(map[string]bool, len(resolutions))
	validated := append([]RebaseResolution(nil), resolutions...)
	for index := range validated {
		resolution := &validated[index]
		resolution.ConflictID = strings.TrimSpace(resolution.ConflictID)
		resolution.Action = strings.TrimSpace(resolution.Action)
		if resolution.ConflictID == "" {
			return nil, fmt.Errorf("resolution %d has no conflict_id", index+1)
		}
		if seen[resolution.ConflictID] {
			return nil, fmt.Errorf("duplicate resolution for conflict %q", resolution.ConflictID)
		}
		seen[resolution.ConflictID] = true
		conflict, exists := conflicts[resolution.ConflictID]
		if !exists {
			return nil, fmt.Errorf("resolution references unknown conflict %q", resolution.ConflictID)
		}
		if resolution.Action == "" || !rebaseReviewActionAllowed(conflict.AllowedActions, resolution.Action) {
			return nil, fmt.Errorf("resolution action %q is not allowed for conflict %q", resolution.Action, resolution.ConflictID)
		}
		if !rebaseMaterializableResolutionAction(resolution.Action) {
			return nil, fmt.Errorf("resolution action %q requires a new plan and cannot be materialized", resolution.Action)
		}
		if resolution.ManualPath != "" {
			path, err := normalizeRel(resolution.ManualPath)
			if err != nil {
				return nil, fmt.Errorf("manual_path for conflict %q: %w", resolution.ConflictID, err)
			}
			resolution.ManualPath = path
		}
		if resolution.SHA256 != "" {
			value := strings.ToLower(strings.TrimSpace(resolution.SHA256))
			if len(value) != sha256.Size*2 {
				return nil, fmt.Errorf("sha256 for conflict %q must be a SHA-256 hex digest", resolution.ConflictID)
			}
			if _, err := hex.DecodeString(value); err != nil {
				return nil, fmt.Errorf("sha256 for conflict %q must be a SHA-256 hex digest", resolution.ConflictID)
			}
			resolution.SHA256 = value
		}
		if resolution.Action == "manual" {
			if resolution.ManualPath == "" || !strings.HasPrefix(filepath.ToSlash(resolution.ManualPath), "manual/") || resolution.SHA256 == "" {
				return nil, fmt.Errorf("manual resolution for conflict %q requires manual_path under manual/ and sha256", resolution.ConflictID)
			}
		} else if resolution.ManualPath != "" || resolution.SHA256 != "" {
			return nil, fmt.Errorf("only manual resolutions may include manual_path or sha256")
		}
		if utf8RuneCount(resolution.Comment) > rebaseReviewMaxCommentRunes {
			return nil, fmt.Errorf("comment for conflict %q is too long", resolution.ConflictID)
		}
	}
	sort.Slice(validated, func(i, j int) bool { return validated[i].ConflictID < validated[j].ConflictID })
	return validated, nil
}

func utf8RuneCount(value string) int {
	return len([]rune(value))
}

func rebaseReviewActionAllowed(allowed []string, action string) bool {
	for _, candidate := range allowed {
		if action == candidate {
			return true
		}
	}
	return false
}

func rebaseMaterializableResolutionAction(action string) bool {
	switch action {
	case "keep_project", "use_target", "drop", "manual":
		return true
	default:
		return false
	}
}

func rebaseReviewHasMaterializableAction(actions []string) bool {
	for _, action := range actions {
		if rebaseMaterializableResolutionAction(action) {
			return true
		}
	}
	return false
}

func writeRebaseReviewJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(true)
	_ = encoder.Encode(value)
}

func setRebaseReviewHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", rebaseReviewCSP())
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "no-store")
}

type rebaseReviewCount struct {
	Key   string
	Value int
}

// rebaseReviewPixelConflict is a report-only projection.  It keeps the
// three source hashes beside the pixel count and, when it was safely
// materialized, the transaction-pinned spatial mask.  The mask contains no
// source pixels: transparent means non-conflicting and opaque magenta means
// the two sides changed that exact decoded pixel differently.
type rebaseReviewPixelConflict struct {
	ID             string
	Path           string
	Message        string
	PixelCount     string
	Base           RebaseFileState
	Project        RebaseFileState
	Target         RebaseFileState
	AllowedActions []string
	MaskPath       string
}

type rebaseReviewMapDelta struct {
	SourcePath string
	Artifact   RebaseMapDelta
}

type rebaseReviewTemplateData struct {
	Transaction     RebaseTransaction
	Classifications []rebaseReviewCount
	Counts          []rebaseReviewCount
	Decisions       []RebaseFileDecision
	MapDeltas       []rebaseReviewMapDelta
	PixelConflicts  []rebaseReviewPixelConflict
	TransactionJSON template.JS
	CSP             string
	Style           template.CSS
	Script          template.JS
	ResolutionCount int
}

func writeRebaseReport(root string, transaction RebaseTransaction) error {
	dir, err := rebaseTransactionDir(root, transaction.ID)
	if err != nil {
		return err
	}
	dir, err = ensureRebaseDirectory(dir)
	if err != nil {
		return err
	}
	jsonData, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	data := rebaseReviewTemplateData{
		Transaction:     transaction,
		Classifications: rebaseReviewClassificationCounts(transaction),
		Counts:          rebaseReviewCounts(transaction.Counts),
		Decisions:       rebaseReviewDecisions(transaction.Files),
		MapDeltas:       rebaseReviewMapDeltas(transaction.Files),
		PixelConflicts:  rebaseReviewPixelConflicts(root, transaction),
		TransactionJSON: template.JS(jsonData), // json.Marshal escapes <, >, and & for a script data block.
		CSP:             rebaseReviewCSP(),
		Style:           template.CSS(rebaseReviewStyle),
		Script:          template.JS(rebaseReviewScript),
		ResolutionCount: len(transaction.Resolutions),
	}
	var rendered bytes.Buffer
	if err := rebaseReviewTemplate.Execute(&rendered, data); err != nil {
		return err
	}
	return writeRebaseReviewFileAtomic(filepath.Join(dir, rebaseReportName), rendered.Bytes())
}

func rebaseReviewMapDeltas(decisions []RebaseFileDecision) []rebaseReviewMapDelta {
	var out []rebaseReviewMapDelta
	for _, decision := range decisions {
		if decision.MapDelta == nil {
			continue
		}
		out = append(out, rebaseReviewMapDelta{SourcePath: decision.Path, Artifact: *decision.MapDelta})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourcePath != out[j].SourcePath {
			return out[i].SourcePath < out[j].SourcePath
		}
		return out[i].Artifact.Path < out[j].Artifact.Path
	})
	return out
}

func writeRebaseReviewFileAtomic(path string, data []byte) error {
	parent, err := ensureRebaseDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	path, err = safeRebaseContainedPath(parent, filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".rebase-report-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceRebaseFileAtomically(path, tmpName)
}

func rebaseReviewCounts(counts map[string]int) []rebaseReviewCount {
	entries := make([]rebaseReviewCount, 0, len(counts))
	for key, value := range counts {
		entries = append(entries, rebaseReviewCount{Key: key, Value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

func rebaseReviewClassificationCounts(transaction RebaseTransaction) []rebaseReviewCount {
	counts := map[string]int{}
	for _, decision := range transaction.Files {
		if decision.Classification != "" {
			counts[decision.Classification]++
		}
	}
	if len(counts) == 0 {
		for key, value := range transaction.Counts {
			if rebaseReviewClassificationCounter(key) {
				counts[key] = value
			}
		}
	}
	return rebaseReviewCounts(counts)
}

func rebaseReviewClassificationCounter(key string) bool {
	if strings.HasPrefix(key, "action_") {
		return false
	}
	switch key {
	case "base_files", "base_excluded", "project_files", "project_excluded", "target_files", "target_excluded", "conflicts":
		return false
	default:
		return true
	}
}

func rebaseReviewDecisions(values []RebaseFileDecision) []RebaseFileDecision {
	out := append([]RebaseFileDecision(nil), values...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Adapter != out[j].Adapter {
			return out[i].Adapter < out[j].Adapter
		}
		return out[i].Action < out[j].Action
	})
	return out
}

func rebaseReviewPixelConflicts(root string, transaction RebaseTransaction) []rebaseReviewPixelConflict {
	decisions := make(map[string]RebaseFileDecision, len(transaction.Files))
	for _, decision := range transaction.Files {
		path, err := normalizeRel(decision.Path)
		if err != nil {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(path))
		// A duplicate normalized path is planner corruption.  Do not claim
		// either set of hashes is the conflict evidence.
		if _, exists := decisions[key]; exists {
			delete(decisions, key)
			continue
		}
		decisions[key] = decision
	}

	var rows []rebaseReviewPixelConflict
	for _, conflict := range transaction.Conflicts {
		if conflict.Code != "pixel_merge_conflict" {
			continue
		}
		row := rebaseReviewPixelConflict{
			ID:             conflict.ID,
			Path:           conflict.Path,
			Message:        conflict.Message,
			PixelCount:     rebaseReviewPixelConflictCount(conflict.Message),
			AllowedActions: append([]string(nil), conflict.AllowedActions...),
		}
		if normalized, err := normalizeRel(conflict.Path); err == nil {
			if decision, exists := decisions[strings.ToLower(filepath.ToSlash(normalized))]; exists {
				row.Base = decision.Base
				row.Project = decision.Project
				row.Target = decision.Target
			}
		}
		if path, err := rebaseReviewPixelMaskRelativePath(conflict.ID); err == nil {
			if _, err := readRebaseReviewPixelMask(root, transaction.ID, conflict.ID); err == nil {
				row.MaskPath = path
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

func rebaseReviewPixelConflictCount(message string) string {
	const (
		prefix = "project and target changed "
		suffix = " pixel(s) differently"
	)
	value := strings.TrimSpace(message)
	if !strings.HasPrefix(value, prefix) {
		return "not recorded"
	}
	value = strings.TrimPrefix(value, prefix)
	end := strings.Index(value, suffix)
	if end <= 0 {
		return "not recorded"
	}
	count := strings.TrimSpace(value[:end])
	for _, runeValue := range count {
		if runeValue < '0' || runeValue > '9' {
			return "not recorded"
		}
	}
	return count
}

func rebaseReviewFileStateEvidence(state RebaseFileState) string {
	if !state.Exists {
		return "absent"
	}
	if strings.TrimSpace(state.SHA256) == "" {
		return "present (hash unavailable)"
	}
	return state.SHA256
}

// materializeRebaseReviewPixelMasks writes review-only spatial masks under
// the transaction directory.  It is deliberately best-effort: a source that
// has drifted since planning must not make the whole report unavailable, but
// it also must not produce a map from unpinned input.  Each successful mask
// is derived from the exact base/project/target bytes identified in the plan.
func materializeRebaseReviewPixelMasks(cfg indexer.Config, root string, transaction RebaseTransaction) error {
	if !rebaseReviewHasPixelConflict(transaction.Conflicts) {
		return nil
	}
	project, base, target, err := resolveRebaseSources(cfg, transaction.Profile)
	if err != nil {
		return err
	}
	var failures []error
	for _, conflict := range transaction.Conflicts {
		if conflict.Code != "pixel_merge_conflict" {
			continue
		}
		decision, ok := rebaseReviewDecisionForConflict(transaction.Files, conflict)
		if !ok {
			failures = append(failures, fmt.Errorf("pixel conflict %q has no unique file decision", conflict.ID))
			continue
		}
		if err := materializeRebaseReviewPixelMask(root, transaction.ID, conflict.ID, decision, base.Path, project.Path, target.Path); err != nil {
			failures = append(failures, fmt.Errorf("pixel conflict %q: %w", conflict.ID, err))
		}
	}
	return errors.Join(failures...)
}

func rebaseReviewHasPixelConflict(conflicts []RebaseConflict) bool {
	for _, conflict := range conflicts {
		if conflict.Code == "pixel_merge_conflict" {
			return true
		}
	}
	return false
}

func rebaseReviewDecisionForConflict(decisions []RebaseFileDecision, conflict RebaseConflict) (RebaseFileDecision, bool) {
	normalized, err := normalizeRel(conflict.Path)
	if err != nil {
		return RebaseFileDecision{}, false
	}
	var found *RebaseFileDecision
	for index := range decisions {
		candidate, err := normalizeRel(decisions[index].Path)
		if err != nil || !strings.EqualFold(filepath.ToSlash(candidate), filepath.ToSlash(normalized)) {
			continue
		}
		if found != nil {
			return RebaseFileDecision{}, false
		}
		found = &decisions[index]
	}
	if found == nil {
		return RebaseFileDecision{}, false
	}
	return *found, true
}

func materializeRebaseReviewPixelMask(root, id, conflictID string, decision RebaseFileDecision, baseRoot, projectRoot, targetRoot string) error {
	if !decision.Base.Exists || !decision.Project.Exists || !decision.Target.Exists ||
		strings.TrimSpace(decision.Base.SHA256) == "" || strings.TrimSpace(decision.Project.SHA256) == "" || strings.TrimSpace(decision.Target.SHA256) == "" {
		return fmt.Errorf("pixel conflict source states are incomplete")
	}
	baseData, err := readSourceFile(baseRoot, decision.Path)
	if err != nil {
		return fmt.Errorf("read base: %w", err)
	}
	projectData, err := readSourceFile(projectRoot, decision.Path)
	if err != nil {
		return fmt.Errorf("read project: %w", err)
	}
	targetData, err := readSourceFile(targetRoot, decision.Path)
	if err != nil {
		return fmt.Errorf("read target: %w", err)
	}
	if hashBytes(baseData) != strings.ToLower(decision.Base.SHA256) ||
		hashBytes(projectData) != strings.ToLower(decision.Project.SHA256) ||
		hashBytes(targetData) != strings.ToLower(decision.Target.SHA256) {
		return fmt.Errorf("source hashes drifted since planning")
	}
	baseRaster, err := decodeRebaseReviewRaster(decision.Path, baseData)
	if err != nil {
		return fmt.Errorf("decode base: %w", err)
	}
	projectRaster, err := decodeRebaseReviewRaster(decision.Path, projectData)
	if err != nil {
		return fmt.Errorf("decode project: %w", err)
	}
	targetRaster, err := decodeRebaseReviewRaster(decision.Path, targetData)
	if err != nil {
		return fmt.Errorf("decode target: %w", err)
	}
	mask, conflicts, err := rebaseReviewPixelMask(baseRaster, projectRaster, targetRaster)
	if err != nil {
		return err
	}
	if conflicts == 0 {
		return fmt.Errorf("no decoded pixel conflict remains")
	}
	data, err := encodeRebasePNG(mask)
	if err != nil {
		return fmt.Errorf("encode conflict mask: %w", err)
	}
	return writeRebaseReviewPixelMask(root, id, conflictID, data)
}

func decodeRebaseReviewRaster(rel string, data []byte) (rebaseRaster, error) {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png":
		return decodeRebasePNG(data)
	case ".tga":
		return decodeRebaseTGA(data)
	default:
		return rebaseRaster{}, fmt.Errorf("unsupported pixel conflict extension %q", filepath.Ext(rel))
	}
}

// rebaseReviewPixelMask uses the identical collision predicate as the raster
// merge adapter.  Its output is transparent except for opaque magenta pixels
// at locations where project and target both changed the base differently.
func rebaseReviewPixelMask(base, project, target rebaseRaster) (rebaseRaster, int, error) {
	for _, named := range []struct {
		name   string
		raster rebaseRaster
	}{{"base", base}, {"project", project}, {"target", target}} {
		if err := validateRebaseRaster(named.raster); err != nil {
			return rebaseRaster{}, 0, fmt.Errorf("%s raster is invalid: %w", named.name, err)
		}
	}
	if base.width != project.width || base.height != project.height || base.width != target.width || base.height != target.height {
		return rebaseRaster{}, 0, fmt.Errorf("raster dimensions differ (base=%dx%d project=%dx%d target=%dx%d)", base.width, base.height, project.width, project.height, target.width, target.height)
	}
	mask := rebaseRaster{width: base.width, height: base.height, pixels: make([]byte, len(base.pixels))}
	conflicts := 0
	for offset := 0; offset < len(base.pixels); offset += 4 {
		if sameRebasePixel(project.pixels[offset:offset+4], base.pixels[offset:offset+4]) ||
			sameRebasePixel(target.pixels[offset:offset+4], base.pixels[offset:offset+4]) ||
			sameRebasePixel(project.pixels[offset:offset+4], target.pixels[offset:offset+4]) {
			continue
		}
		mask.pixels[offset+0] = 0xff
		mask.pixels[offset+1] = 0x00
		mask.pixels[offset+2] = 0xff
		mask.pixels[offset+3] = 0xff
		conflicts++
	}
	return mask, conflicts, nil
}

func rebaseReviewPixelMaskRelativePath(conflictID string) (string, error) {
	if strings.TrimSpace(conflictID) == "" {
		return "", fmt.Errorf("pixel conflict ID is required")
	}
	sum := sha256.Sum256([]byte(conflictID))
	return "pixel-conflicts/" + hex.EncodeToString(sum[:]) + ".png", nil
}

func rebaseReviewPixelMaskFile(root, id, conflictID string) (string, string, error) {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return "", "", err
	}
	relative, err := rebaseReviewPixelMaskRelativePath(conflictID)
	if err != nil {
		return "", "", err
	}
	full, err := safeRebaseContainedPath(dir, filepath.Join(dir, filepath.FromSlash(relative)))
	if err != nil {
		return "", "", fmt.Errorf("pixel conflict mask path escapes transaction: %w", err)
	}
	return relative, full, nil
}

func writeRebaseReviewPixelMask(root, id, conflictID string, data []byte) error {
	_, full, err := rebaseReviewPixelMaskFile(root, id, conflictID)
	if err != nil {
		return err
	}
	parent, err := ensureRebaseDirectory(filepath.Dir(full))
	if err != nil {
		return err
	}
	full, err = safeRebaseContainedPath(parent, filepath.Join(parent, filepath.Base(full)))
	if err != nil {
		return fmt.Errorf("pixel conflict mask path escapes transaction after directory creation: %w", err)
	}
	return writeRebaseReviewFileAtomic(full, data)
}

func readRebaseReviewPixelMask(root, id, conflictID string) ([]byte, error) {
	_, full, err := rebaseReviewPixelMaskFile(root, id, conflictID)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pixel conflict mask is not a regular file")
	}
	return os.ReadFile(full)
}

func readRebaseReviewMapDelta(root, id string, delta RebaseMapDelta) ([]byte, error) {
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		return nil, err
	}
	relative, err := normalizeRel(delta.Path)
	if err != nil || !strings.HasPrefix(strings.ToLower(filepath.ToSlash(relative)), "map-deltas/") {
		return nil, fmt.Errorf("invalid map delta path")
	}
	full, err := safeRebaseContainedPath(dir, filepath.Join(dir, filepath.FromSlash(relative)))
	if err != nil {
		return nil, fmt.Errorf("map delta path escapes transaction: %w", err)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("map delta is not a regular file")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	if hashBytes(data) != strings.ToLower(strings.TrimSpace(delta.SHA256)) {
		return nil, fmt.Errorf("map delta hash differs from the transaction")
	}
	return data, nil
}

func rebaseReviewCSP() string {
	return "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; connect-src 'self'; img-src 'self' data:; style-src 'sha256-" + rebaseReviewCSPHash(rebaseReviewStyle) + "'; script-src 'sha256-" + rebaseReviewCSPHash(rebaseReviewScript) + "'"
}

func rebaseReviewCSPHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.StdEncoding.EncodeToString(sum[:])
}

const rebaseReviewStyle = `:root{color-scheme:dark;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}body{margin:0;background:#111827;color:#e5e7eb;line-height:1.45}main{max-width:1180px;margin:0 auto;padding:28px 20px 56px}h1,h2{color:#f9fafb}h1{margin:0 0 6px}h2{margin-top:32px;font-size:1.15rem}.muted{color:#9ca3af}.status{display:inline-block;border:1px solid #4b5563;border-radius:999px;padding:2px 10px;margin:8px 0 0;font-weight:600}dl{display:grid;grid-template-columns:max-content 1fr;gap:6px 18px;background:#172033;padding:16px;border-radius:8px}dt{color:#9ca3af}dd{margin:0;overflow-wrap:anywhere}table{width:100%;border-collapse:collapse;background:#172033;border-radius:8px;overflow:hidden}th,td{padding:9px 10px;border-bottom:1px solid #374151;text-align:left;vertical-align:top;overflow-wrap:anywhere}th{color:#cbd5e1;background:#202b40}tr:last-child td{border-bottom:0}.none{padding:12px;background:#172033;border-radius:8px;color:#9ca3af}select,button{font:inherit;border-radius:6px;padding:7px 9px}select{background:#0f172a;color:#e5e7eb;border:1px solid #4b5563;min-width:145px}button{background:#315bce;color:#fff;border:1px solid #6b8cff;cursor:pointer}.actions{display:flex;align-items:center;gap:10px;margin:14px 0}.message{min-height:1.4em;color:#bfdbfe}.message.error{color:#fca5a5}.api-note{font-size:.9rem;color:#9ca3af}code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:.92em}.wide{min-width:270px}.decision-table code,.pixel-table code{word-break:break-all}.state-safe{color:#86efac}.state-review{color:#fcd34d}.pixel-mask{display:block;width:min(100%,360px);height:auto;max-height:360px;object-fit:contain;image-rendering:pixelated;background:repeating-conic-gradient(#0f172a 0 25%,#1f2937 0 50%) 50%/16px 16px;border:1px solid #4b5563;border-radius:4px}.pixel-caption{display:block;margin-top:5px;color:#9ca3af;font-size:.86rem}@media (max-width:720px){main{padding:18px 12px}dl{grid-template-columns:1fr}table{display:block;overflow-x:auto}}`

const rebaseReviewScript = `(function(){"use strict";function message(text,error){var node=document.getElementById("save-message");if(!node){return;}node.textContent=text||"";node.className=error?"message error":"message";}function token(){return new URLSearchParams(window.location.search).get("token")||"";}function capabilityHeaders(){var value=token();if(!value){throw new Error("review capability token is missing from this URL");}return {"X-CK3-Rebase-Token":value};}function applySaved(values){var map={};(values||[]).forEach(function(value){map[value.conflict_id]=value;});document.querySelectorAll("tr[data-conflict-id]").forEach(function(row){var value=map[row.dataset.conflictId];if(!value){return;}var select=row.querySelector("select.resolution-action");if(select){select.value=value.action||"";}if(value.manual_path&&value.sha256){row.dataset.manualPath=value.manual_path;row.dataset.manualSha256=value.sha256;}});}function load(){fetch("/api/resolutions",{headers:{"Accept":"application/json"}}).then(function(response){if(!response.ok){throw new Error("could not load saved resolutions");}return response.json();}).then(applySaved).catch(function(){message("Static report is available; saved resolutions require the local review server.",true);});}function uploadManual(row,conflictID){var input=row.querySelector("input.manual-file");if(!input||!input.files||!input.files[0]){if(row.dataset.manualPath&&row.dataset.manualSha256){return Promise.resolve({manual_path:row.dataset.manualPath,sha256:row.dataset.manualSha256});}return Promise.reject(new Error("manual resolution for "+conflictID+" needs a candidate file"));}var form=new FormData();form.append("conflict_id",conflictID);form.append("file",input.files[0]);return fetch("/api/manual",{method:"POST",headers:capabilityHeaders(),body:form}).then(function(response){return response.text().then(function(body){if(!response.ok){throw new Error(body||"could not import manual candidate");}return JSON.parse(body);});}).then(function(value){row.dataset.manualPath=value.manual_path;row.dataset.manualSha256=value.sha256;return value;});}function collect(){var values=[];var uploads=[];document.querySelectorAll("tr[data-conflict-id]").forEach(function(row){var select=row.querySelector("select.resolution-action");if(!select||!select.value){return;}var value={conflict_id:row.dataset.conflictId,action:select.value};if(value.action==="manual"){uploads.push(uploadManual(row,value.conflict_id).then(function(upload){value.manual_path=upload.manual_path;value.sha256=upload.sha256;values.push(value);}));}else{values.push(value);}});return Promise.all(uploads).then(function(){return values;});}function save(){collect().then(function(values){var headers=capabilityHeaders();headers["Content-Type"]="application/json";headers["Accept"]="application/json";return fetch("/api/resolutions",{method:"POST",headers:headers,body:JSON.stringify(values)}).then(function(response){return response.text().then(function(body){if(!response.ok){throw new Error(body||"could not save resolutions");}return body;});}).then(function(){message("Saved "+values.length+" resolution(s).",false);});}).catch(function(error){message(error.message,true);});}document.addEventListener("DOMContentLoaded",function(){var button=document.getElementById("save-resolutions");if(button){button.addEventListener("click",save);}load();});})();`

var rebaseReviewTemplate = template.Must(template.New("rebase-review").Funcs(template.FuncMap{
	"join":              strings.Join,
	"materializable":    rebaseMaterializableResolutionAction,
	"hasMaterializable": rebaseReviewHasMaterializableAction,
	"stateEvidence":     rebaseReviewFileStateEvidence,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="{{.CSP}}">
<title>CK3 rebase review {{.Transaction.ID}}</title>
<style>{{.Style}}</style>
</head>
<body>
<main>
<h1>CK3 semantic rebase review</h1>
<p class="muted">This report and any displayed pixel masks are transaction-scoped evidence. When opened through the local review server, selections are stored only in this transaction.</p>
<p class="status">{{.Transaction.Status}}</p>
<dl>
<dt>Transaction</dt><dd><code>{{.Transaction.ID}}</code></dd>
<dt>Created</dt><dd>{{.Transaction.CreatedAt}}</dd>
<dt>Updated</dt><dd>{{.Transaction.UpdatedAt}}</dd>
<dt>Progress</dt><dd>{{.Transaction.Progress.Phase}} — {{.Transaction.Progress.Message}} ({{.Transaction.Progress.Completed}}/{{.Transaction.Progress.Total}})</dd>
<dt>Game-version mode</dt><dd><code>{{.Transaction.GameVersion.Mode}}</code>: <code>{{.Transaction.GameVersion.BaseVersion}}</code> to <code>{{.Transaction.GameVersion.TargetVersion}}</code></dd>
<dt>Semantic compatibility</dt><dd><code>{{.Transaction.GameVersion.Status}}</code> (rules: <code>{{.Transaction.GameVersion.SemanticRuleFamily}}</code>; adapter: <code>{{.Transaction.GameVersion.CompatibilityAdapter}}</code>) - {{.Transaction.GameVersion.Reason}}</dd>
<dt>Saved resolutions</dt><dd>{{.ResolutionCount}}</dd>
</dl>
<h2>Classifications</h2>
{{if .Classifications}}
<table><thead><tr><th>Classification</th><th>Count</th></tr></thead><tbody>{{range .Classifications}}<tr><td><code>{{.Key}}</code></td><td>{{.Value}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="none">No classified file decisions have been recorded.</p>{{end}}
<h2>Plan counters</h2>
{{if .Counts}}
<table><thead><tr><th>Counter</th><th>Count</th></tr></thead><tbody>{{range .Counts}}<tr><td><code>{{.Key}}</code></td><td>{{.Value}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="none">No counters have been recorded.</p>{{end}}
<h2>Three-way decision and semantic evidence</h2>
<p class="muted">Every planned file replay is shown with its Base, Ours, Theirs and resulting content hash. “Absent” is meaningful evidence: it records a deliberate deletion or inheritance decision rather than a missing report value.</p>
{{if .Decisions}}
<table class="decision-table"><thead><tr><th>Path</th><th>Classification</th><th>Adapter</th><th>Base SHA-256</th><th>Ours SHA-256</th><th>Theirs SHA-256</th><th>Result SHA-256</th><th>Action</th><th>Semantic IDs</th><th>Reason</th></tr></thead><tbody>{{range .Decisions}}<tr><td><code>{{.Path}}</code></td><td><code>{{.Classification}}</code></td><td><code>{{.Adapter}}</code></td><td><code>{{stateEvidence .Base}}</code></td><td><code>{{stateEvidence .Project}}</code></td><td><code>{{stateEvidence .Target}}</code></td><td><code>{{stateEvidence .Result}}</code></td><td><code>{{.Action}}</code><br>{{if .Safe}}<span class="state-safe">safe</span>{{else}}<span class="state-review">review required</span>{{end}}</td><td>{{if .SemanticIDs}}{{range .SemanticIDs}}<code>{{.}}</code><br>{{end}}{{else}}<span class="muted">none</span>{{end}}{{if .ConflictIDs}}<span class="muted">conflicts:</span><br>{{range .ConflictIDs}}<code>{{.}}</code><br>{{end}}{{end}}</td><td>{{.Reason}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="none">No file decisions have been recorded.</p>{{end}}
<h2>Coordinate map deltas</h2>
<p class="muted">Each image is the transparent Base-to-Ours difference layer. Non-transparent pixels retain the project color and are replayed at the recorded origin over Theirs; the CK3-facing candidate keeps the target canvas and file type.</p>
{{if .MapDeltas}}
<table class="pixel-table"><thead><tr><th>Map raster</th><th>Origin / canvas</th><th>Changed pixels</th><th>Delta SHA-256</th><th>Transparent delta</th></tr></thead><tbody>{{range .MapDeltas}}<tr><td><code>{{.SourcePath}}</code></td><td>({{.Artifact.OriginX}},{{.Artifact.OriginY}}) / {{.Artifact.Width}}x{{.Artifact.Height}}</td><td>{{.Artifact.ChangedPixels}}</td><td><code>{{.Artifact.SHA256}}</code></td><td><a href="{{.Artifact.Path}}"><img class="pixel-mask" src="{{.Artifact.Path}}" loading="lazy" alt="Transparent coordinate delta for {{.SourcePath}}"></a><span class="pixel-caption">transparent = unchanged from Base</span></td></tr>{{end}}</tbody></table>
{{else}}<p class="none">No project map raster required a coordinate delta.</p>{{end}}
<h2>Pixel conflict spatial evidence</h2>
<p class="muted">Magenta mask pixels mark exact decoded coordinates where Ours and Theirs both changed Base differently. The mask is generated only after all three source hashes revalidate against this transaction; the report never invents geometry from a conflict count.</p>
{{if .PixelConflicts}}
<table class="pixel-table"><thead><tr><th>Raster / conflict</th><th>Conflicting pixels</th><th>Base SHA-256</th><th>Ours SHA-256</th><th>Theirs SHA-256</th><th>Spatial mask</th><th>Allowed actions</th><th>Evidence</th></tr></thead><tbody>{{range .PixelConflicts}}<tr><td><code>{{.Path}}</code><br><code>{{.ID}}</code></td><td>{{.PixelCount}}</td><td><code>{{stateEvidence .Base}}</code></td><td><code>{{stateEvidence .Project}}</code></td><td><code>{{stateEvidence .Target}}</code></td><td>{{if .MaskPath}}<a href="{{.MaskPath}}"><img class="pixel-mask" src="{{.MaskPath}}" loading="lazy" alt="Exact pixel conflict mask for {{.Path}}"></a><span class="pixel-caption">transparent = no collision; magenta = conflicting pixel</span>{{else}}<span class="muted">No mask available. Inputs could not be revalidated, are not geometrically compatible, or the transaction predates this review artifact.</span>{{end}}</td><td><code>{{join .AllowedActions ", "}}</code></td><td>{{.Message}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="none">No decoded-pixel conflicts are recorded.</p>{{end}}
<h2>Conflicts</h2>
{{if .Transaction.Conflicts}}
<table><thead><tr><th>ID</th><th>Code</th><th>Path</th><th>Message</th><th>Allowed actions</th><th>Resolution</th></tr></thead><tbody>{{range .Transaction.Conflicts}}<tr data-conflict-id="{{.ID}}"><td><code>{{.ID}}</code></td><td><code>{{.Code}}</code></td><td><code>{{.Path}}</code>{{if .SemanticID}}<br><code>{{.SemanticID}}</code>{{end}}</td><td>{{.Message}}</td><td>{{join .AllowedActions ", "}}</td><td>{{if hasMaterializable .AllowedActions}}<select class="resolution-action" aria-label="Resolution for {{.ID}}"><option value="">Select action</option>{{range .AllowedActions}}{{if materializable .}}<option value="{{.}}">{{.}}</option>{{end}}{{end}}</select><input class="manual-file" type="file" aria-label="Manual candidate for {{.ID}}">{{else}}<span class="muted">Requires a new plan</span>{{end}}</td></tr>{{end}}</tbody></table>
<div class="actions"><button id="save-resolutions" type="button">Save selected resolutions</button><span id="save-message" class="message"></span></div>
{{else}}<p class="none">No manual conflicts are currently recorded.</p>{{end}}
<p class="api-note">Local API: <code>GET /api/transaction</code>, <code>GET /api/resolutions</code>, <code>POST /api/manual</code>, <code>POST /api/resolutions</code>. Saving requires this review URL's one-time capability token.</p>
</main>
<script id="rebase-transaction-data" type="application/json">{{.TransactionJSON}}</script><script>{{.Script}}</script>
</body>
</html>`))
