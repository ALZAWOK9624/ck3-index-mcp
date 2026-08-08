package savefile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveUnderRootsConfinesUntrustedPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	save := filepath.Join(root, "upload.ck3")
	if err := os.WriteFile(save, []byte("SAV"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	nested := filepath.Join(root, "group", "nested.ck3")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatalf("creating nested dir: %v", err)
	}
	if err := os.WriteFile(nested, []byte("SAV"), 0o600); err != nil {
		t.Fatalf("writing nested fixture: %v", err)
	}
	secret := filepath.Join(outside, "secret.ck3")
	if err := os.WriteFile(secret, []byte("SAV"), 0o600); err != nil {
		t.Fatalf("writing outside fixture: %v", err)
	}

	roots := []string{root}

	t.Run("accepts a name relative to the root", func(t *testing.T) {
		got, err := ResolveUnderRoots(roots, "upload.ck3")
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		if got != save {
			t.Fatalf("resolved %q, want %q", got, save)
		}
	})

	t.Run("accepts a subdirectory", func(t *testing.T) {
		if _, err := ResolveUnderRoots(roots, filepath.Join("group", "nested.ck3")); err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
	})

	t.Run("accepts an absolute path already inside the root", func(t *testing.T) {
		if _, err := ResolveUnderRoots(roots, save); err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
	})

	refusals := []struct {
		name      string
		candidate string
	}{
		{"absolute path outside every root", secret},
		{"parent traversal", filepath.Join("..", filepath.Base(outside), "secret.ck3")},
		{"traversal that climbs and returns", filepath.Join("group", "..", "..", "escape.ck3")},
		{"the root itself", "."},
		{"a missing file", "absent.ck3"},
		{"a directory", "group"},
		{"an empty name", ""},
		{"a null byte", "upload\x00.ck3"},
	}
	for _, testCase := range refusals {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ResolveUnderRoots(roots, testCase.candidate); err == nil {
				t.Fatalf("expected a refusal")
			} else if KindOf(err) != ErrPath {
				t.Fatalf("error kind = %q (%v)", KindOf(err), err)
			}
		})
	}

	t.Run("no configured roots", func(t *testing.T) {
		if _, err := ResolveUnderRoots(nil, "upload.ck3"); KindOf(err) != ErrPath {
			t.Fatalf("expected a path refusal, got %v", err)
		}
	})
}

func TestResolveUnderRootsRejectsSymlinkedPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.ck3")
	if err := os.WriteFile(secret, []byte("SAV"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	fileLink := filepath.Join(root, "link.ck3")
	if err := os.Symlink(secret, fileLink); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks needs elevation on this host")
		}
		t.Fatalf("creating symlink: %v", err)
	}
	dirLink := filepath.Join(root, "linkdir")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatalf("creating directory symlink: %v", err)
	}

	roots := []string{root}
	for _, candidate := range []string{"link.ck3", filepath.Join("linkdir", "secret.ck3")} {
		if _, err := ResolveUnderRoots(roots, candidate); err == nil {
			t.Fatalf("%s: a symlink was followed out of the root", candidate)
		} else if KindOf(err) != ErrPath {
			t.Fatalf("%s: error kind = %q (%v)", candidate, KindOf(err), err)
		}
	}
}

func TestResolveUnderRootsSearchesEveryRoot(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	save := filepath.Join(second, "upload.ck3")
	if err := os.WriteFile(save, []byte("SAV"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	got, err := ResolveUnderRoots([]string{first, second}, "upload.ck3")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if got != save {
		t.Fatalf("resolved %q, want %q", got, save)
	}
}
