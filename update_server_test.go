package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateServerResumesVerifiedDeploymentAfterMissingHooks(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("tools", "update_server.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "health --require-ready") {
		t.Fatal("update_server.sh must reject a rebuilt database that is not query-ready")
	}

	bash := updateTestBash(t)
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	origin := filepath.Join(root, "origin.git")
	deploy := filepath.Join(root, "deploy")
	runtimeDir := filepath.Join(root, "runtime")
	fakeBin := filepath.Join(root, "fake-bin")
	for _, dir := range []string{
		filepath.Join(seed, "tools"),
		filepath.Join(seed, "internal", "indexer"),
		runtimeDir,
		fakeBin,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	updateTestWriteFile(t, filepath.Join(seed, "tools", "update_server.sh"), script, 0o755)
	updateTestWriteFile(t, filepath.Join(seed, ".gitignore"), []byte("/bin/\n"), 0o644)
	updateTestWriteFile(t, filepath.Join(seed, "VERSION"), []byte("test-version\n"), 0o644)
	updateTestWriteFile(t, filepath.Join(seed, "internal", "indexer", "scan.go"), []byte("package indexer\n\nconst indexRuleVersion = \"test-rule\"\n"), 0o644)
	updateTestWriteFile(t, filepath.Join(seed, "revision.txt"), []byte("first\n"), 0o644)

	updateTestGit(t, seed, "init", "-b", "main")
	updateTestGit(t, seed, "add", ".")
	updateTestGit(t, seed, "-c", "user.name=ck3-index test", "-c", "user.email=test@invalid", "commit", "-m", "initial")
	updateTestGit(t, root, "clone", "--bare", seed, origin)
	updateTestGit(t, root, "clone", origin, deploy)
	updateTestGit(t, seed, "remote", "add", "origin", origin)
	updateTestWriteFile(t, filepath.Join(seed, "revision.txt"), []byte("second\n"), 0o644)
	updateTestGit(t, seed, "add", "revision.txt")
	updateTestGit(t, seed, "-c", "user.name=ck3-index test", "-c", "user.email=test@invalid", "commit", "-m", "update")
	updateTestGit(t, seed, "push", "origin", "main")

	config := filepath.Join(runtimeDir, "ck3-index.toml")
	database := filepath.Join(runtimeDir, "active.sqlite")
	updateTestWriteFile(t, config, []byte("database = \"active.sqlite\"\n"), 0o644)
	updateTestWriteFile(t, database, []byte("live database\n"), 0o644)
	updateTestWriteFile(t, filepath.Join(deploy, "bin", "ck3-index"), []byte("#!/usr/bin/env bash\nprintf 'old binary\\n'\n"), 0o755)
	updateTestWriteFile(t, filepath.Join(fakeBin, "go"), []byte(`#!/usr/bin/env bash
set -euo pipefail
out=""
while [ "$#" -gt 0 ]; do
    if [ "$1" = "-o" ]; then
        out="$2"
        shift 2
        continue
    fi
    shift
done
[ -n "$out" ]
printf '#!/usr/bin/env bash\nexit 0\n' > "$out"
chmod +x "$out"
`), 0o755)

	firstOutput, firstErr := updateTestRunScript(bash, deploy, fakeBin, config, nil)
	var exitErr *exec.ExitError
	if !errors.As(firstErr, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("first update error = %v, want exit 3; output:\n%s", firstErr, firstOutput)
	}
	if !strings.Contains(firstOutput, "verified deployment remains staged") {
		t.Fatalf("first update did not explain the staged deployment:\n%s", firstOutput)
	}
	statePath := filepath.Join(deploy, "bin", "ck3-index.new.state")
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read staged deployment state: %v", err)
	}
	if !strings.Contains(string(state), "phase=verified") {
		t.Fatalf("staged deployment phase = %q, want verified", state)
	}
	if got := strings.TrimSpace(updateTestGit(t, deploy, "rev-parse", "HEAD")); got != strings.TrimSpace(updateTestGit(t, deploy, "rev-parse", "origin/main")) {
		t.Fatalf("source was not fast-forwarded before the staged pause: HEAD=%s", got)
	}

	hookLog := filepath.Join(runtimeDir, "hooks.log")
	hookPath := updateTestShellQuote(filepath.ToSlash(hookLog))
	hooks := []string{
		"CK3_STOP_CMD=printf 'stop\\n' >> " + hookPath,
		"CK3_START_CMD=printf 'start\\n' >> " + hookPath,
	}
	secondOutput, secondErr := updateTestRunScript(bash, deploy, fakeBin, config, hooks)
	if secondErr != nil {
		t.Fatalf("resumed update failed: %v\n%s", secondErr, secondOutput)
	}
	if !strings.Contains(secondOutput, "resuming staged deployment") {
		t.Fatalf("second update rebuilt or exited instead of resuming:\n%s", secondOutput)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("deployment state was not removed after success: %v", err)
	}
	hookOutput, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(hookOutput), "stop\nstart\n"; got != want {
		t.Fatalf("agent hooks = %q, want %q", got, want)
	}
}

func updateTestBash(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("bash"); err == nil {
		return path
	}
	if runtime.GOOS == "windows" {
		gitPath, err := exec.LookPath("git")
		if err == nil {
			root := filepath.Dir(filepath.Dir(gitPath))
			for _, path := range []string{
				filepath.Join(root, "bin", "bash.exe"),
				filepath.Join(root, "usr", "bin", "bash.exe"),
			} {
				if _, err := os.Stat(path); err == nil {
					return path
				}
			}
		}
	}
	t.Skip("bash is not installed")
	return ""
}

func updateTestRunScript(bash, deploy, fakeBin, config string, extraEnv []string) (string, error) {
	script := filepath.ToSlash(filepath.Join(deploy, "tools", "update_server.sh"))
	command := exec.Command(
		bash,
		"-c",
		`export PATH="$1:$PATH"; shift; exec bash "$@"`,
		"ck3-update-test",
		updateTestBashPath(fakeBin),
		script,
		"--config",
		filepath.ToSlash(config),
	)
	command.Dir = deploy
	command.Env = append(os.Environ(), extraEnv...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func updateTestBashPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS != "windows" {
		return filepath.ToSlash(path)
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return filepath.ToSlash(path)
	}
	rest := strings.TrimPrefix(path, volume)
	return "/" + strings.ToLower(volume[:1]) + filepath.ToSlash(rest)
}

func updateTestShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func updateTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func updateTestWriteFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
