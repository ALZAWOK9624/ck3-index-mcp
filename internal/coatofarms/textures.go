package coatofarms

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PathTextures resolves texture names against a caller-supplied lookup from
// bare file name to absolute path, decoding and caching each file once.
//
// A definition names only "ce_lion_passant.dds" with no directory, because CK3
// searches its own gfx/coat_of_arms subfolders. The lookup is therefore built
// by whoever knows where the assets are — for the index that is the resource
// table, which already records every indexed .dds with its owning source.
type PathTextures struct {
	paths map[string]string

	mu     sync.Mutex
	cache  map[string]*image.NRGBA
	failed map[string]bool
}

// NewPathTextures builds a source from name to absolute path. Names are matched
// case-insensitively on the base file name.
func NewPathTextures(paths map[string]string) *PathTextures {
	normalized := make(map[string]string, len(paths))
	for name, path := range paths {
		normalized[textureKey(name)] = path
	}
	return &PathTextures{
		paths:  normalized,
		cache:  map[string]*image.NRGBA{},
		failed: map[string]bool{},
	}
}

// NewDirectoryTextures indexes every .dds under the given roots. Earlier roots
// win, matching the way a higher-priority source shadows the same file name.
func NewDirectoryTextures(roots ...string) (*PathTextures, error) {
	paths := map[string]string{}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".dds") {
				return nil
			}
			key := textureKey(filepath.Base(path))
			if _, taken := paths[key]; !taken {
				paths[key] = path
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("index coat of arms textures under %q: %w", root, err)
		}
	}
	return NewPathTextures(paths), nil
}

// Texture returns nil with no error when the name is not known, which the
// renderer reports as a missing texture rather than treating as a failure.
func (t *PathTextures) Texture(name string) (*image.NRGBA, error) {
	key := textureKey(name)
	if key == "" {
		return nil, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cached, ok := t.cache[key]; ok {
		return cached, nil
	}
	if t.failed[key] {
		return nil, nil
	}
	path, known := t.paths[key]
	if !known {
		t.failed[key] = true
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read texture %q: %w", path, err)
	}
	decoded, err := DecodeDDS(data)
	if err != nil {
		return nil, fmt.Errorf("decode texture %q: %w", path, err)
	}
	t.cache[key] = decoded
	return decoded, nil
}

// Names reports every texture name the source can resolve.
func (t *PathTextures) Names() []string {
	out := make([]string, 0, len(t.paths))
	for name := range t.paths {
		out = append(out, name)
	}
	return out
}

func textureKey(name string) string {
	trimmed := strings.TrimSpace(strings.Trim(name, `"`))
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(filepath.ToSlash(trimmed)))
}
