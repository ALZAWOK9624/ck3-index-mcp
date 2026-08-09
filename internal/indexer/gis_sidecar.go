package indexer

import (
	"context"
	"crypto/sha256"
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

// Verifying the sidecar means hashing up to 512 MiB and starting WhiteboxTools
// with a five second timeout. That is the right price to pay once, and the
// wrong price to pay on every ck3_health and every capabilities probe: the
// audited sessions show 56 health calls at a median of 1.17 s and a worst case
// of 12.6 s. The result is memoized against the identity of the file that was
// actually verified, so replacing or upgrading the sidecar still re-verifies.
var gisSidecarMemo struct {
	sync.Mutex
	identity string
	status   GISSidecarStatus
	valid    bool
}

// gisSidecarIdentity is deliberately built from the same inputs the
// verification consumes. A sidecar swapped in place changes size or mtime; a
// re-pinned release changes the configured hash; a config change changes the
// path or the analysis mode. Any of those misses the memo.
func gisSidecarIdentity(cfg Config) (string, bool) {
	info, err := os.Stat(cfg.GISSidecarPath)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s\x00%t",
		cfg.GISSidecarPath, info.Size(), info.ModTime().UnixNano(),
		cfg.GISSidecarSHA256, cfg.GISAnalysis, cfg.GISEnabled), true
}

func InspectGISSidecar(ctx context.Context, cfg Config) GISSidecarStatus {
	identity, addressable := gisSidecarIdentity(cfg)
	if addressable {
		gisSidecarMemo.Lock()
		if gisSidecarMemo.valid && gisSidecarMemo.identity == identity {
			cached := gisSidecarMemo.status
			gisSidecarMemo.Unlock()
			cached.AllowedTools = append([]string(nil), cached.AllowedTools...)
			return cached
		}
		gisSidecarMemo.Unlock()
	}
	status := inspectGISSidecarUncached(ctx, cfg)
	// Only a completed verification is worth remembering. A failure caused by a
	// transient condition -- a busy machine missing the five second version
	// timeout -- must not be pinned for the life of the process.
	if addressable && status.Available {
		gisSidecarMemo.Lock()
		gisSidecarMemo.identity, gisSidecarMemo.status, gisSidecarMemo.valid = identity, status, true
		gisSidecarMemo.Unlock()
	}
	return status
}

func inspectGISSidecarUncached(ctx context.Context, cfg Config) GISSidecarStatus {
	status := GISSidecarStatus{Enabled: cfg.GISEnabled, Platform: gisPlatform(), Analysis: cfg.GISAnalysis, AnalysisStatus: "not_cached"}
	for name := range whiteboxAllowedTools {
		status.AllowedTools = append(status.AllowedTools, name)
	}
	sort.Strings(status.AllowedTools)
	if !cfg.GISEnabled {
		status.Reason = "GIS analysis is disabled by configuration."
		return status
	}
	if strings.TrimSpace(cfg.GISSidecarSHA256) == "" {
		status.Reason = "The release bundle did not configure a trusted WhiteboxTools SHA-256."
		return status
	}
	f, err := os.Open(cfg.GISSidecarPath)
	if err != nil {
		status.Reason = "The verified WhiteboxTools sidecar is not installed."
		return status
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, io.LimitReader(f, 512<<20))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		status.Reason = "The WhiteboxTools sidecar could not be hashed."
		return status
	}
	status.SHA256 = fmt.Sprintf("%x", h.Sum(nil))
	if status.SHA256 != cfg.GISSidecarSHA256 {
		status.Reason = "The WhiteboxTools binary hash does not match the release manifest."
		return status
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := runBoundedGISProcess(versionCtx, cfg.GISSidecarPath, filepath.Dir(cfg.GISSidecarPath), []string{"--version"}, 64<<10)
	if err != nil {
		status.Reason = "The verified WhiteboxTools sidecar did not report its version."
		return status
	}
	status.Version = strings.TrimSpace(output)
	if !strings.Contains(status.Version, whiteboxRequiredVersion) {
		status.Reason = "The WhiteboxTools version is not the pinned Open Core v" + whiteboxRequiredVersion + "."
		return status
	}
	status.Available = true
	return status
}

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
	output, err := runBoundedGISProcess(toolCtx, cfg.GISSidecarPath, cleanWork, args, 1<<20)
	output = strings.ReplaceAll(output, cleanWork, "<gis-workdir>")
	output = strings.ReplaceAll(output, filepath.ToSlash(cleanWork), "<gis-workdir>")
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	return output, nil
}

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
