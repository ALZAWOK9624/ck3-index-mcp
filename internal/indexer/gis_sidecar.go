package indexer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const whiteboxRequiredVersion = "2.4.0"

const maxGISSidecarBytes = int64(512 << 20)

var whiteboxAllowedTools = map[string]bool{
	"BreachDepressions": true, "Slope": true, "Aspect": true,
	"RuggednessIndex": true, "PlanCurvature": true, "ProfileCurvature": true,
	"D8Pointer": true, "D8FlowAccumulation": true, "ExtractStreams": true, "Watershed": true,
}

type GISSidecarStatus struct {
	Enabled        bool     `json:"enabled"`
	Available      bool     `json:"available"`
	Platform       string   `json:"platform"`
	Version        string   `json:"version,omitempty"`
	SHA256         string   `json:"sha256,omitempty"`
	Analysis       string   `json:"analysis"`
	AnalysisStatus string   `json:"analysis_status,omitempty"`
	AllowedTools   []string `json:"allowed_tools"`
	Reason         string   `json:"unavailable_reason,omitempty"`
}

type verifiedGISSidecar struct {
	status     GISSidecarStatus
	executable string
}

type gisSidecarMemoEntry struct {
	ready  chan struct{}
	result verifiedGISSidecar
}

// Verifying the sidecar means hashing up to 512 MiB, publishing those exact
// bytes into a content-addressed executable cache, and starting that published
// copy with a five second timeout. The source path is only an installation
// input: request-time tools never execute it. This closes the check/use gap in
// which a sidecar could be replaced after verification but before a GIS job.
//
// Entries also act as per-content singleflight calls. Concurrent health and
// rebuild requests either receive the same immutable executable or the same
// failed attempt; failures are removed so a transient filesystem or process
// error is retried later.
var gisSidecarMemo struct {
	sync.Mutex
	entries map[string]*gisSidecarMemoEntry
}

func gisSidecarMemoKey(cfg Config) (string, bool) {
	expected := strings.ToLower(strings.TrimSpace(cfg.GISSidecarSHA256))
	cacheRoot := effectiveGISCacheRoot(cfg)
	if !cfg.GISEnabled || !validSHA256Hex(expected) || cacheRoot == "" {
		return "", false
	}
	root, err := filepath.Abs(cacheRoot)
	if err != nil {
		return "", false
	}
	return strings.Join([]string{filepath.Clean(root), expected, cfg.GISAnalysis, gisPlatform(), whiteboxRequiredVersion}, "\x00"), true
}

func effectiveGISCacheRoot(cfg Config) string {
	if root := strings.TrimSpace(cfg.GISCacheRoot); root != "" {
		return root
	}
	if configPath := strings.TrimSpace(cfg.ConfigPath); configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "cache", "gis")
	}
	return ""
}

func InspectGISSidecar(ctx context.Context, cfg Config) GISSidecarStatus {
	return resolveVerifiedGISSidecar(ctx, cfg).status
}

func resolveVerifiedGISSidecar(ctx context.Context, cfg Config) verifiedGISSidecar {
	key, memoizable := gisSidecarMemoKey(cfg)
	if !memoizable {
		return inspectGISSidecarUncached(ctx, cfg)
	}

	gisSidecarMemo.Lock()
	if gisSidecarMemo.entries == nil {
		gisSidecarMemo.entries = make(map[string]*gisSidecarMemoEntry)
	}
	if existing := gisSidecarMemo.entries[key]; existing != nil {
		select {
		case <-existing.ready:
			candidate := cloneVerifiedGISSidecar(existing.result)
			if !candidate.status.Available {
				gisSidecarMemo.Unlock()
				return candidate
			}
			if ctx.Err() != nil {
				gisSidecarMemo.Unlock()
				return cancelledGISSidecar(cfg)
			}
			// A completed memo entry skips the version subprocess, but its
			// published bytes are still mutable by a sufficiently privileged
			// local actor. Replace it with a pending entry so one caller hashes
			// the cache and concurrent callers share that exact revalidation.
			entry := &gisSidecarMemoEntry{ready: make(chan struct{})}
			gisSidecarMemo.entries[key] = entry
			gisSidecarMemo.Unlock()
			result := revalidateMemoizedGISSidecar(ctx, cfg, candidate)
			completeGISSidecarMemoEntry(key, entry, result)
			return cloneVerifiedGISSidecar(result)
		default:
			gisSidecarMemo.Unlock()
			return waitGISSidecarMemoEntry(ctx, cfg, existing)
		}
	}
	entry := &gisSidecarMemoEntry{ready: make(chan struct{})}
	gisSidecarMemo.entries[key] = entry
	gisSidecarMemo.Unlock()

	result := inspectGISSidecarUncached(ctx, cfg)
	completeGISSidecarMemoEntry(key, entry, result)
	return cloneVerifiedGISSidecar(result)
}

func waitGISSidecarMemoEntry(ctx context.Context, cfg Config, entry *gisSidecarMemoEntry) verifiedGISSidecar {
	select {
	case <-entry.ready:
		// Closing ready and cancelling the waiter can become observable at the
		// same instant. Go selects randomly among ready cases, so the waiter's
		// context must win through an explicit post-wait gate.
		if ctx.Err() != nil {
			return cancelledGISSidecar(cfg)
		}
		return cloneVerifiedGISSidecar(entry.result)
	case <-ctx.Done():
		return cancelledGISSidecar(cfg)
	}
}

func completeGISSidecarMemoEntry(key string, entry *gisSidecarMemoEntry, result verifiedGISSidecar) {
	gisSidecarMemo.Lock()
	entry.result = result
	if current := gisSidecarMemo.entries[key]; current == entry && !result.status.Available {
		delete(gisSidecarMemo.entries, key)
	}
	close(entry.ready)
	gisSidecarMemo.Unlock()
}

func revalidateMemoizedGISSidecar(ctx context.Context, cfg Config, candidate verifiedGISSidecar) verifiedGISSidecar {
	expected := strings.ToLower(strings.TrimSpace(cfg.GISSidecarSHA256))
	measured, exists, err := inspectPublishedGISExecutable(ctx, candidate.executable, expected)
	if err == nil && exists && measured == expected {
		if ctx.Err() != nil {
			return cancelledGISSidecar(cfg)
		}
		return candidate
	}

	status := newGISSidecarStatus(cfg)
	status.SHA256 = measured
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status.Reason = "WhiteboxTools verification was cancelled."
	case !exists:
		status.Reason = "The verified WhiteboxTools executable cache is missing."
	case measured != "" && measured != expected:
		status.Reason = "The WhiteboxTools binary hash does not match the release manifest."
	default:
		status.Reason = "The verified WhiteboxTools executable cache could not be verified safely."
	}
	return verifiedGISSidecar{status: status}
}

func cancelledGISSidecar(cfg Config) verifiedGISSidecar {
	status := newGISSidecarStatus(cfg)
	status.Reason = "WhiteboxTools verification was cancelled."
	return verifiedGISSidecar{status: status}
}

func cloneVerifiedGISSidecar(sidecar verifiedGISSidecar) verifiedGISSidecar {
	sidecar.status.AllowedTools = append([]string(nil), sidecar.status.AllowedTools...)
	return sidecar
}

func newGISSidecarStatus(cfg Config) GISSidecarStatus {
	status := GISSidecarStatus{Enabled: cfg.GISEnabled, Platform: gisPlatform(), Analysis: cfg.GISAnalysis, AnalysisStatus: "not_cached"}
	for name := range whiteboxAllowedTools {
		status.AllowedTools = append(status.AllowedTools, name)
	}
	sort.Strings(status.AllowedTools)
	return status
}

func inspectGISSidecarUncached(ctx context.Context, cfg Config) verifiedGISSidecar {
	status := newGISSidecarStatus(cfg)
	if !cfg.GISEnabled {
		status.Reason = "GIS analysis is disabled by configuration."
		return verifiedGISSidecar{status: status}
	}
	expected := strings.ToLower(strings.TrimSpace(cfg.GISSidecarSHA256))
	if expected == "" {
		status.Reason = "The release bundle did not configure a trusted WhiteboxTools SHA-256."
		return verifiedGISSidecar{status: status}
	}
	if !validSHA256Hex(expected) {
		status.Reason = "The configured WhiteboxTools SHA-256 is invalid."
		return verifiedGISSidecar{status: status}
	}
	executable, measured, sourceMissing, err := publishGISSidecar(ctx, cfg, expected)
	status.SHA256 = measured
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status.Reason = "WhiteboxTools verification was cancelled."
		case sourceMissing:
			status.Reason = "The verified WhiteboxTools sidecar is not installed."
		case measured != "" && measured != expected:
			status.Reason = "The WhiteboxTools binary hash does not match the release manifest."
		default:
			status.Reason = "The verified WhiteboxTools sidecar could not be published safely."
		}
		return verifiedGISSidecar{status: status}
	}
	status.SHA256 = expected
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := executeVerifiedGISProcess(versionCtx, executable, expected, filepath.Dir(executable), []string{"--version"}, 64<<10)
	if err != nil {
		status.Reason = "The verified WhiteboxTools sidecar did not report its version."
		return verifiedGISSidecar{status: status}
	}
	status.Version = strings.TrimSpace(output)
	if !strings.Contains(status.Version, whiteboxRequiredVersion) {
		status.Reason = "The WhiteboxTools version is not the pinned Open Core v" + whiteboxRequiredVersion + "."
		return verifiedGISSidecar{status: status}
	}
	if ctx.Err() != nil {
		return cancelledGISSidecar(cfg)
	}
	status.Available = true
	return verifiedGISSidecar{status: status, executable: executable}
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// publishGISSidecar copies and hashes one open source handle into a private
// directory, makes both the file and directory read-only, then atomically
// renames that complete directory to the content address. A concurrent process
// can win, but its complete result is accepted only after re-verification.
func publishGISSidecar(ctx context.Context, cfg Config, expected string) (string, string, bool, error) {
	binRoot, contentDir, destination, err := gisPublishedExecutablePath(cfg, expected)
	if err != nil {
		return "", "", false, err
	}
	if measured, exists, err := inspectPublishedGISExecutable(ctx, destination, expected); exists || err != nil {
		if err != nil {
			return "", measured, false, err
		}
		return destination, measured, false, nil
	}

	source, err := os.Open(cfg.GISSidecarPath)
	if err != nil {
		return "", "", os.IsNotExist(err), err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", "", false, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxGISSidecarBytes {
		return "", "", false, fmt.Errorf("WhiteboxTools source is not a bounded regular file")
	}

	temporaryDir, err := os.MkdirTemp(binRoot, ".whitebox-publish-*")
	if err != nil {
		return "", "", false, err
	}
	defer cleanupGISPublishDirectory(temporaryDir)
	temporaryPath := filepath.Join(temporaryDir, filepath.Base(destination))
	temporary, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return "", "", false, err
	}

	h := sha256.New()
	written, copyErr := copyGISExecutable(ctx, io.MultiWriter(temporary, h), io.LimitReader(source, maxGISSidecarBytes+1))
	measured := fmt.Sprintf("%x", h.Sum(nil))
	if copyErr == nil && written > maxGISSidecarBytes {
		copyErr = fmt.Errorf("WhiteboxTools source exceeds the executable size limit")
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	if copyErr == nil {
		copyErr = temporary.Chmod(0o555)
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", measured, false, copyErr
	}
	if closeErr != nil {
		return "", measured, false, closeErr
	}
	if measured != expected {
		return "", measured, false, fmt.Errorf("WhiteboxTools SHA-256 mismatch")
	}
	if err := os.Chmod(temporaryDir, 0o555); err != nil {
		return "", measured, false, err
	}
	if err := ctx.Err(); err != nil {
		return "", measured, false, err
	}

	if err := os.Rename(temporaryDir, contentDir); err != nil {
		measuredWinner, exists, inspectErr := inspectPublishedGISExecutable(ctx, destination, expected)
		if exists && inspectErr == nil {
			return destination, measuredWinner, false, nil
		}
		if inspectErr != nil {
			return "", measuredWinner, false, inspectErr
		}
		return "", measured, false, fmt.Errorf("atomically publish WhiteboxTools executable directory: %w", err)
	}
	return destination, measured, false, nil
}

func cleanupGISPublishDirectory(path string) {
	entries, _ := os.ReadDir(path)
	for _, entry := range entries {
		_ = os.Chmod(filepath.Join(path, entry.Name()), 0o644)
	}
	_ = os.Chmod(path, 0o755)
	_ = os.RemoveAll(path)
}

func gisPublishedExecutablePath(cfg Config, expected string) (string, string, string, error) {
	cacheRoot := effectiveGISCacheRoot(cfg)
	if cacheRoot == "" {
		return "", "", "", fmt.Errorf("GIS cache root is unavailable")
	}
	root, err := filepath.Abs(cacheRoot)
	if err != nil {
		return "", "", "", err
	}
	root = filepath.Clean(root)
	binRoot := filepath.Join(root, "gis-bin")
	contentDir := filepath.Join(binRoot, expected)
	if err := ensureContainedGISDir(root, binRoot); err != nil {
		return "", "", "", err
	}
	if _, err := validateExistingGISCacheDirectory(contentDir); err != nil {
		return "", "", "", err
	}
	name := "whitebox_tools"
	if strings.HasPrefix(gisPlatform(), "windows-") {
		name += ".exe"
	}
	return binRoot, contentDir, filepath.Join(contentDir, name), nil
}

func inspectPublishedGISExecutable(ctx context.Context, path, expected string) (string, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	reparse, err := gisPathIsReparsePoint(path)
	if err != nil {
		return "", true, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || reparse {
		return "", true, fmt.Errorf("published WhiteboxTools executable is not a regular file")
	}
	if info.Mode().Perm()&0o222 != 0 {
		return "", true, fmt.Errorf("published WhiteboxTools executable is writable")
	}
	measured, err := hashBoundedGISExecutable(ctx, path)
	if err != nil {
		return "", true, err
	}
	if measured != expected {
		return measured, true, fmt.Errorf("published WhiteboxTools executable failed its content-address check")
	}
	return measured, true, nil
}

func hashBoundedGISExecutable(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	written, err := copyGISWithContext(ctx, h, io.LimitReader(f, maxGISSidecarBytes+1))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if written > maxGISSidecarBytes {
		return "", fmt.Errorf("published WhiteboxTools executable exceeds the size limit")
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

type contextCheckingReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextCheckingReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(buffer)
	if contextErr := r.ctx.Err(); contextErr != nil && err == nil {
		return n, contextErr
	}
	return n, err
}

func copyGISWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	return io.CopyBuffer(destination, contextCheckingReader{ctx: ctx, r: source}, make([]byte, 128<<10))
}

var copyGISExecutable = copyGISWithContext

// GISSidecarStatus reports the currently verified sidecar together with the
// analysis state persisted by the last map-cache rebuild. InspectGISSidecar
// cannot determine whether a database cache exists on its own.
func (db *DB) GISSidecarStatus(ctx context.Context, cfg Config) GISSidecarStatus {
	status := InspectGISSidecar(ctx, cfg)
	if db == nil || !db.tableExists(ctx, "meta") {
		return status
	}
	cachedAnalysis := db.metaValueOrEmpty(ctx, "map_gis_analysis")
	cachedStatus := db.metaValueOrEmpty(ctx, "map_gis_advanced_status")
	if cachedStatus == "" {
		return status
	}
	if cachedAnalysis == status.Analysis {
		status.AnalysisStatus = cachedStatus
	} else {
		status.AnalysisStatus = "stale"
	}
	return status
}

func runWhiteboxTool(ctx context.Context, cfg Config, workDir, tool string, arguments []string) (string, error) {
	if !whiteboxAllowedTools[tool] {
		return "", fmt.Errorf("WhiteboxTools operation %q is not allowed", tool)
	}
	verified := resolveVerifiedGISSidecar(ctx, cfg)
	if !verified.status.Available || verified.executable == "" {
		reason := strings.TrimSpace(verified.status.Reason)
		if reason == "" {
			reason = "the configured sidecar is unavailable"
		}
		return "", fmt.Errorf("verified WhiteboxTools executable is unavailable: %s", reason)
	}
	cleanWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(cleanWork); err != nil || !info.IsDir() {
		return "", fmt.Errorf("GIS work directory is unavailable")
	}
	args := []string{"--run=" + tool, "--wd=" + cleanWork}
	for _, argument := range arguments {
		if strings.ContainsAny(argument, "\r\n\x00") {
			return "", fmt.Errorf("invalid WhiteboxTools argument")
		}
		args = append(args, argument)
	}
	timeout := time.Duration(cfg.GISTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := executeVerifiedGISProcess(toolCtx, verified.executable, verified.status.SHA256, cleanWork, args, 1<<20)
	output = strings.ReplaceAll(output, cleanWork, "<gis-workdir>")
	output = strings.ReplaceAll(output, filepath.ToSlash(cleanWork), "<gis-workdir>")
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	return output, nil
}

func executeVerifiedGISProcess(ctx context.Context, executable, expected, workDir string, args []string, outputLimit int) (string, error) {
	measured, exists, err := inspectPublishedGISExecutable(ctx, executable, expected)
	if err != nil {
		return "", fmt.Errorf("verify published WhiteboxTools executable before use: %w", err)
	}
	if !exists || measured != expected {
		return "", fmt.Errorf("verified WhiteboxTools executable disappeared before use")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return executeGISProcess(ctx, executable, workDir, args, outputLimit)
}

// Kept as a narrow test seam so trust-boundary tests can assert which exact
// executable path would be started without shipping a platform-specific fake
// binary in the repository.
var executeGISProcess = runBoundedGISProcess

type boundedBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			b.data = append(b.data, p[:remaining]...)
		} else {
			b.data = append(b.data, p...)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func runBoundedGISProcess(ctx context.Context, executable, workDir string, args []string, outputLimit int) (string, error) {
	cmd := exec.Command(executable, args...)
	cmd.Dir = workDir
	cmd.Env = []string{"PATH=" + filepath.Dir(executable), "TMPDIR=" + workDir, "TEMP=" + workDir, "TMP=" + workDir}
	if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+systemRoot)
	}
	configureGISCommand(cmd)
	buffer := &boundedBuffer{limit: outputLimit}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buffer.String(), err
	case <-ctx.Done():
		killGISProcessTree(cmd.Process)
		<-done
		return buffer.String(), ctx.Err()
	}
}
