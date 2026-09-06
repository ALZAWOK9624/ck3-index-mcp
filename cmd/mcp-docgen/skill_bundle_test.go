package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSkillBundleIsPortableAndSynchronized(t *testing.T) {
	root := repositoryRoot(t)
	canonicalRoot := filepath.Join(root, "skill", "ck3-coding")
	pluginRoot := filepath.Join(root, "plugin", "ck3-index", "skills", "ck3-coding")
	readBundle := func(base string) map[string][]byte {
		t.Helper()
		files := map[string][]byte{}
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			relative, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			files[relative], err = os.ReadFile(path)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	canonical, plugin := readBundle(canonicalRoot), readBundle(pluginRoot)
	if len(canonical) != len(plugin) {
		t.Fatalf("skill bundle file counts differ: canonical=%d plugin=%d", len(canonical), len(plugin))
	}
	links := regexp.MustCompile(`\]\(([^)]+)\)`)
	for relative, content := range canonical {
		packaged, exists := plugin[relative]
		if !exists || !sameDocumentContent(content, packaged) {
			t.Errorf("packaged skill resource missing or stale: %s", relative)
		}
		if filepath.Ext(relative) != ".md" {
			continue
		}
		for _, match := range links.FindAllSubmatch(content, -1) {
			target := strings.SplitN(string(match[1]), "#", 2)[0]
			if target == "" || strings.Contains(target, "://") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(relative), filepath.FromSlash(target)))
			if filepath.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				t.Errorf("skill link escapes installed bundle: %s -> %s", relative, target)
				continue
			}
			if _, exists := canonical[resolved]; !exists {
				t.Errorf("skill link targets an absent resource: %s -> %s", relative, target)
			}
		}
	}
}
