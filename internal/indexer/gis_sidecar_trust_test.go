package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type cancelingGISWriter struct {
	cancel context.CancelFunc
	writes int
}

type cancelingGISReader struct {
	reader io.Reader
	cancel context.CancelFunc
	read   bool
}

func (r *cancelingGISReader) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	if !r.read {
		r.read = true
		r.cancel()
	}
	return n, err
}

func (w *cancelingGISWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		w.cancel()
	}
	return len(data), nil
}

func resetGISSidecarTestState(t *testing.T) {
	t.Helper()
	originalRunner := executeGISProcess
	originalCopy := copyGISExecutable
	gisSidecarMemo.Lock()
	gisSidecarMemo.entries = nil
	gisSidecarMemo.Unlock()
	t.Cleanup(func() {
		executeGISProcess = originalRunner
		copyGISExecutable = originalCopy
		gisSidecarMemo.Lock()
		gisSidecarMemo.entries = nil
		gisSidecarMemo.Unlock()
	})
}

func writePinnedGISSidecar(t *testing.T, content string) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "installed", "whitebox_tools")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	return Config{
		GISEnabled:        true,
		GISAnalysis:       "full",
		GISCacheRoot:      filepath.Join(dir, "cache"),
		GISTimeoutSeconds: 30,
		GISSidecarPath:    source,
		GISSidecarSHA256:  hash,
		GISCacheMaxGiB:    1,
	}, source
}

func TestWhiteboxExecutionUsesPublishedBytesAfterSourceReplacement(t *testing.T) {
	resetGISSidecarTestState(t)
	cfg, source := writePinnedGISSidecar(t, "trusted-original-bytes")

	var mu sync.Mutex
	var executed []string
	executeGISProcess = func(_ context.Context, executable, _ string, args []string, _ int) (string, error) {
		content, err := os.ReadFile(executable)
		if err != nil {
			return "", err
		}
		mu.Lock()
		executed = append(executed, executable)
		mu.Unlock()
		if len(args) == 1 && args[0] == "--version" {
			return "WhiteboxTools v" + whiteboxRequiredVersion, nil
		}
		return string(content), nil
	}

	status := InspectGISSidecar(context.Background(), cfg)
	if !status.Available {
		t.Fatalf("trusted sidecar was not published: %+v", status)
	}
	if err := os.WriteFile(source, []byte("untrusted-replacement"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Force resolution through the on-disk cache rather than the in-process
	// memo. This models a process restart after the installation path changed.
	gisSidecarMemo.Lock()
	gisSidecarMemo.entries = nil
	gisSidecarMemo.Unlock()

	workDir := t.TempDir()
	output, err := runWhiteboxTool(context.Background(), cfg, workDir, "Slope", []string{"--dem=input.dep", "--output=slope.dep"})
	if err != nil {
		t.Fatalf("run published sidecar: %v", err)
	}
	if output != "trusted-original-bytes" {
		t.Fatalf("sidecar execution used mutable source bytes: output=%q", output)
	}

	mu.Lock()
	paths := append([]string(nil), executed...)
	mu.Unlock()
	if len(paths) != 3 {
		t.Fatalf("expected two version checks and one tool call, got %d", len(paths))
	}
	for _, executable := range paths {
		if filepath.Clean(executable) == filepath.Clean(source) {
			t.Fatalf("mutable installation path was executed: %q", executable)
		}
		if !strings.Contains(filepath.Clean(executable), filepath.Join("gis-bin", cfg.GISSidecarSHA256)) {
			t.Fatalf("execution escaped the content-addressed cache: %q", executable)
		}
	}
}

func TestConcurrentGISSidecarPublicationIsSingleflightAndAtomic(t *testing.T) {
	resetGISSidecarTestState(t)
	cfg, _ := writePinnedGISSidecar(t, "concurrently-published-bytes")

	var mu sync.Mutex
	versionCalls := 0
	executeGISProcess = func(_ context.Context, executable, _ string, args []string, _ int) (string, error) {
		if _, err := os.ReadFile(executable); err != nil {
			return "", err
		}
		if len(args) == 1 && args[0] == "--version" {
			mu.Lock()
			versionCalls++
			mu.Unlock()
			time.Sleep(25 * time.Millisecond)
			return "WhiteboxTools v" + whiteboxRequiredVersion, nil
		}
		return "", nil
	}

	const callers = 16
	statuses := make(chan GISSidecarStatus, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			statuses <- InspectGISSidecar(context.Background(), cfg)
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if !status.Available || status.SHA256 != cfg.GISSidecarSHA256 {
			t.Fatalf("concurrent caller observed an incomplete publication: %+v", status)
		}
	}
	mu.Lock()
	calls := versionCalls
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("concurrent verification started the published binary %d times, want 1", calls)
	}

	binRoot, _, destination, err := gisPublishedExecutablePath(cfg, cfg.GISSidecarSHA256)
	if err != nil {
		t.Fatal(err)
	}
	measured, exists, err := inspectPublishedGISExecutable(context.Background(), destination, cfg.GISSidecarSHA256)
	if err != nil || !exists || measured != cfg.GISSidecarSHA256 {
		t.Fatalf("published executable is not complete and immutable: exists=%v sha=%q err=%v", exists, measured, err)
	}
	entries, err := os.ReadDir(binRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".whitebox-publish-") {
			t.Fatalf("publication left a temporary executable behind: %q", entry.Name())
		}
	}
}

func TestGISSidecarHashMismatchNeverPublishesOrExecutes(t *testing.T) {
	resetGISSidecarTestState(t)
	cfg, source := writePinnedGISSidecar(t, "untrusted-bytes")
	cfg.GISSidecarSHA256 = strings.Repeat("a", sha256.Size*2)
	executeGISProcess = func(context.Context, string, string, []string, int) (string, error) {
		t.Fatal("hash-mismatched sidecar was executed")
		return "", nil
	}

	status := InspectGISSidecar(context.Background(), cfg)
	if status.Available || !strings.Contains(status.Reason, "hash does not match") {
		t.Fatalf("hash mismatch did not fail closed: %+v", status)
	}
	_, _, destination, err := gisPublishedExecutablePath(cfg, cfg.GISSidecarSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("hash-mismatched source reached the executable cache: %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	missing := InspectGISSidecar(context.Background(), cfg)
	if missing.SHA256 != "" || !strings.Contains(missing.Reason, "not installed") {
		t.Fatalf("failed verification was memoized after the source disappeared: %+v", missing)
	}
}

func TestGISSidecarRejectsTamperedPublishedExecutable(t *testing.T) {
	resetGISSidecarTestState(t)
	cfg, _ := writePinnedGISSidecar(t, "trusted-cache-bytes")
	runnerCalls := 0
	executeGISProcess = func(_ context.Context, executable, _ string, _ []string, _ int) (string, error) {
		runnerCalls++
		if _, err := os.ReadFile(executable); err != nil {
			return "", err
		}
		return "WhiteboxTools v" + whiteboxRequiredVersion, nil
	}
	if status := InspectGISSidecar(context.Background(), cfg); !status.Available {
		t.Fatalf("initial publication failed: %+v", status)
	}
	_, _, destination, err := gisPublishedExecutablePath(cfg, cfg.GISSidecarSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("tampered-cache-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runWhiteboxTool(context.Background(), cfg, t.TempDir(), "Slope", []string{"--dem=input.dep", "--output=slope.dep"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("memoized tampered executable did not fail closed: %v", err)
	}
	if runnerCalls != 1 {
		t.Fatalf("tampered executable reached the process runner: calls=%d, want initial version call only", runnerCalls)
	}
}

func TestGISSidecarStatusRevalidatesMemoizedPublishedExecutable(t *testing.T) {
	inspectors := []struct {
		name    string
		inspect func(context.Context, Config) GISSidecarStatus
	}{
		{name: "inspect", inspect: InspectGISSidecar},
		{name: "deep health", inspect: func(ctx context.Context, cfg Config) GISSidecarStatus {
			var db *DB
			return db.GISSidecarStatus(ctx, cfg)
		}},
	}
	mutations := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "tampered", mutate: func(t *testing.T, destination string) {
			if err := os.Chmod(destination, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, []byte("tampered-after-memo"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "deleted", mutate: func(t *testing.T, destination string) {
			if err := os.Chmod(destination, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(destination); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, inspector := range inspectors {
		for _, mutation := range mutations {
			t.Run(inspector.name+"/"+mutation.name, func(t *testing.T) {
				resetGISSidecarTestState(t)
				cfg, _ := writePinnedGISSidecar(t, "trusted-status-bytes")
				runnerCalls := 0
				executeGISProcess = func(context.Context, string, string, []string, int) (string, error) {
					runnerCalls++
					return "WhiteboxTools v" + whiteboxRequiredVersion, nil
				}
				if status := inspector.inspect(context.Background(), cfg); !status.Available {
					t.Fatalf("initial publication failed: %+v", status)
				}
				_, _, destination, err := gisPublishedExecutablePath(cfg, cfg.GISSidecarSHA256)
				if err != nil {
					t.Fatal(err)
				}
				mutation.mutate(t, destination)

				status := inspector.inspect(context.Background(), cfg)
				if status.Available || status.Reason == "" {
					t.Fatalf("memoized %s cache remained available: %+v", mutation.name, status)
				}
				key, _ := gisSidecarMemoKey(cfg)
				gisSidecarMemo.Lock()
				_, memoized := gisSidecarMemo.entries[key]
				gisSidecarMemo.Unlock()
				if memoized {
					t.Fatal("failed cache revalidation remained memoized")
				}
				if runnerCalls != 1 {
					t.Fatalf("invalid cache reached process runner: calls=%d, want initial version call only", runnerCalls)
				}
			})
		}
	}
}

func TestGISSidecarMemoWaiterCancellationWinsReadyRace(t *testing.T) {
	cfg := Config{GISEnabled: true, GISAnalysis: "full"}
	entry := &gisSidecarMemoEntry{
		ready: make(chan struct{}),
		result: verifiedGISSidecar{status: GISSidecarStatus{
			Enabled: true, Available: true, Analysis: "full",
		}},
	}
	close(entry.ready)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Both select cases are ready. Repetition makes this fail reliably against
	// the old implementation, which randomly returned the successful entry.
	for i := 0; i < 1_000; i++ {
		result := waitGISSidecarMemoEntry(ctx, cfg, entry)
		if result.status.Available || !strings.Contains(strings.ToLower(result.status.Reason), "cancel") {
			t.Fatalf("iteration %d returned a completed result after waiter cancellation: %+v", i, result.status)
		}
	}
}

func TestCopyGISWithContextStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelingGISWriter{cancel: cancel}
	source := bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20))
	written, err := copyGISWithContext(ctx, writer, source)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context-aware GIS copy error = %v, want context.Canceled", err)
	}
	if written <= 0 || written >= 1<<20 || writer.writes != 1 {
		t.Fatalf("cancelled GIS copy continued reading: written=%d writes=%d", written, writer.writes)
	}
}

func TestGISSidecarCancellationDoesNotPublishOrExecute(t *testing.T) {
	resetGISSidecarTestState(t)
	cfg, _ := writePinnedGISSidecar(t, strings.Repeat("trusted", 1<<15))
	executeGISProcess = func(context.Context, string, string, []string, int) (string, error) {
		t.Fatal("cancelled sidecar verification reached the process runner")
		return "", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status := InspectGISSidecar(ctx, cfg)
	if status.Available || !strings.Contains(strings.ToLower(status.Reason), "cancel") {
		t.Fatalf("cancelled sidecar verification did not fail closed: %+v", status)
	}
	assertNoGISPublication(t, cfg)
}

func TestGISSidecarCancellationDuringCopyCleansStaging(t *testing.T) {
	resetGISSidecarTestState(t)
	cfg, _ := writePinnedGISSidecar(t, strings.Repeat("trusted-copy", 1<<15))
	ctx, cancel := context.WithCancel(context.Background())
	copyGISExecutable = func(copyCtx context.Context, destination io.Writer, source io.Reader) (int64, error) {
		return copyGISWithContext(copyCtx, destination, &cancelingGISReader{reader: source, cancel: cancel})
	}
	executeGISProcess = func(context.Context, string, string, []string, int) (string, error) {
		t.Fatal("copy-cancelled sidecar verification reached the process runner")
		return "", nil
	}
	status := InspectGISSidecar(ctx, cfg)
	if status.Available || !strings.Contains(strings.ToLower(status.Reason), "cancel") {
		t.Fatalf("mid-copy cancellation did not fail closed: %+v", status)
	}
	assertNoGISPublication(t, cfg)
}

func assertNoGISPublication(t *testing.T, cfg Config) {
	t.Helper()
	binRoot, _, destination, err := gisPublishedExecutablePath(cfg, cfg.GISSidecarSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("cancelled verification published an executable: %v", err)
	}
	entries, err := os.ReadDir(binRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".whitebox-publish-") {
			t.Fatalf("cancelled verification left staging directory %q", entry.Name())
		}
	}
}
