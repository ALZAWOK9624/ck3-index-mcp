package indexer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ck3-index/internal/script"
)

// vanillaOnActionIndex answers what the configured game layer's own copy of an
// on_action actually contains.
//
// The override check used to assert that any direct effect/trigger block in a
// vanilla-named on_action "overwrites originals". It never looked at the
// original. On a real mod tree that made the claim wrong four times out of
// five: overriding an on_action file means copying it, so the overwhelming
// majority of those blocks are byte-for-byte the vanilla block, shadowing the
// original with itself, and a handful add a block vanilla never had. Comparing
// costs one lazy pass over the game's own on_action directory.
type vanillaOnActionIndex struct {
	root        string
	once        sync.Once
	blocks      map[string]map[string]*script.Node
	fingerprint string
	err         error
}

// newVanillaOnActionIndex returns nil when no game layer is configured. A
// workspace without one has no original to overwrite, so the check that
// depends on this stays silent rather than guessing.
func vanillaOnActionsForConfig(cfg Config) *vanillaOnActionIndex {
	if game, ok := GameSource(cfg); ok {
		return newVanillaOnActionIndex(game.Path)
	}
	return nil
}

func newVanillaOnActionIndex(root string) *vanillaOnActionIndex {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return &vanillaOnActionIndex{root: root}
}

// block reports the game layer's own effect/trigger block for an on_action.
// Definitions are keyed by on_action name rather than by file, because a mod
// may declare a vanilla on_action from a path of its own choosing.
func (index *vanillaOnActionIndex) block(action, blockKey string) (*script.Node, bool) {
	if index == nil {
		return nil, false
	}
	index.once.Do(index.load)
	children, ok := index.blocks[strings.ToLower(action)]
	if !ok {
		return nil, false
	}
	node, ok := children[strings.ToLower(blockKey)]
	return node, ok
}

const vanillaOnActionFingerprintKey = "vanilla_on_action_fingerprint"

func (index *vanillaOnActionIndex) contentFingerprint() (string, error) {
	if index == nil {
		return "none", nil
	}
	index.once.Do(index.load)
	return index.fingerprint, index.err
}

func (index *vanillaOnActionIndex) load() {
	index.blocks = map[string]map[string]*script.Node{}
	hash := sha256.New()
	dir := filepath.Join(index.root, "common", "on_action")
	index.err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if path == dir && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info == nil || info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".txt") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fmt.Fprintf(hash, "%d:%s%d:", len(rel), rel, len(data))
		hash.Write(data)
		for _, node := range script.Parse(string(data)).Nodes {
			if node.Kind != "block" || node.Key == "" {
				continue
			}
			name := strings.ToLower(node.Key)
			children, ok := index.blocks[name]
			if !ok {
				children = map[string]*script.Node{}
				index.blocks[name] = children
			}
			for _, child := range node.Children {
				if onActionSingleSlotFields[strings.ToLower(child.Key)] {
					children[strings.ToLower(child.Key)] = child
				}
			}
		}
		return nil
	})
	index.fingerprint = fmt.Sprintf("%x", hash.Sum(nil))
}

// sameScriptShape compares two parsed subtrees by what they say, ignoring
// where they were written. Two blocks with the same shape produce the same
// behaviour, so replacing one with the other changes nothing.
func sameScriptShape(left, right *script.Node) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.Key != right.Key || left.Operator != right.Operator || left.Value != right.Value || left.Kind != right.Kind {
		return false
	}
	if len(left.Children) != len(right.Children) {
		return false
	}
	for i := range left.Children {
		if !sameScriptShape(left.Children[i], right.Children[i]) {
			return false
		}
	}
	return true
}
