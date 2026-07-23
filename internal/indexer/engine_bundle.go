package indexer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// EngineBundle is one immutable view of all engine-log inputs used by the
// in-memory rule registry, the fingerprint guard, and SQLite ingestion.
// Loading a bundle reads each input exactly once.
type EngineBundle struct {
	Fingerprint string

	ScopeRules  map[string]map[string][]string
	RuleOutputs map[string]map[string][]string
	Targets     map[string][]string
	Modifiers   map[string]ModifierInfo

	Datatypes []DatatypeInfo
	ScopeRows []ScopeEvidence
}

// EngineInputFile is the cheap process-cache invalidation key. Missing
// optional files are represented with Size=-1.
type EngineInputFile struct {
	Path      string
	Size      int64
	MTimeNano int64
}

type EngineInputManifest struct {
	Root  string
	Files []EngineInputFile
}

type engineInputDescriptor struct {
	label    string
	path     string
	kind     string
	optional bool
	missing  bool
}

type cachedEngineBundle struct {
	Manifest EngineInputManifest
	Bundle   *EngineBundle
}

var engineBundleCache = struct {
	sync.Mutex
	entries map[string]cachedEngineBundle
	order   []string
}{
	entries: map[string]cachedEngineBundle{},
}

const maxCachedEngineBundles = 8

// LoadEngineBundle performs a strict content read. Scan and scan --files use
// this path so a preserved mtime/size cannot conceal an engine-rule change.
func LoadEngineBundle(ctx context.Context, logs string) (*EngineBundle, error) {
	return loadEngineBundle(ctx, logs, false)
}

// loadCachedEngineBundle is intended for frequent read-only status checks.
// The next real refresh still performs LoadEngineBundle's strict content read.
func loadCachedEngineBundle(ctx context.Context, logs string) (*EngineBundle, error) {
	return loadEngineBundle(ctx, logs, true)
}

func loadEngineBundle(ctx context.Context, logs string, allowManifestCache bool) (*EngineBundle, error) {
	for attempt := 0; attempt < 2; attempt++ {
		manifest, inputs, err := engineInputPlan(ctx, logs)
		if err != nil {
			return nil, err
		}
		if allowManifestCache {
			if bundle := cachedBundleForManifest(manifest); bundle != nil {
				return bundle, nil
			}
		}
		bundle, err := readEngineBundle(ctx, manifest, inputs)
		if err != nil {
			return nil, err
		}
		after, _, err := engineInputPlan(ctx, logs)
		if err != nil {
			return nil, err
		}
		if engineManifestsEqual(manifest, after) {
			cacheEngineBundle(manifest, bundle)
			return bundle, nil
		}
	}
	return nil, fmt.Errorf("engine log inputs changed while loading")
}

func engineInputPlan(ctx context.Context, logs string) (EngineInputManifest, []engineInputDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return EngineInputManifest{}, nil, err
	}
	if strings.TrimSpace(logs) == "" {
		return EngineInputManifest{}, nil, nil
	}
	root, err := filepath.Abs(logs)
	if err != nil {
		return EngineInputManifest{}, nil, err
	}
	root = filepath.Clean(root)
	manifest := EngineInputManifest{Root: root}
	var inputs []engineInputDescriptor
	add := func(label, path, kind string, optional bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			if optional && os.IsNotExist(err) {
				manifest.Files = append(manifest.Files, EngineInputFile{Path: path, Size: -1})
				inputs = append(inputs, engineInputDescriptor{label: label, path: path, kind: kind, optional: true, missing: true})
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("engine input %s is not a regular file", label)
		}
		manifest.Files = append(manifest.Files, EngineInputFile{Path: path, Size: info.Size(), MTimeNano: info.ModTime().UnixNano()})
		inputs = append(inputs, engineInputDescriptor{label: label, path: path, kind: kind, optional: optional})
		return nil
	}
	for _, spec := range engineScopeLogSpecs {
		if err := add(spec.name, filepath.Join(root, spec.name), spec.kind, spec.optional); err != nil {
			return EngineInputManifest{}, nil, fmt.Errorf("inspect engine log %s: %w", spec.name, err)
		}
	}
	if err := add("modifiers.log", filepath.Join(root, "modifiers.log"), "modifier", true); err != nil {
		return EngineInputManifest{}, nil, fmt.Errorf("inspect engine modifiers: %w", err)
	}
	dataTypesDir := filepath.Join(root, "data_types")
	entries, err := os.ReadDir(dataTypesDir)
	if err != nil {
		return EngineInputManifest{}, nil, fmt.Errorf("inspect engine data_types: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		label := filepath.ToSlash(filepath.Join("data_types", name))
		if err := add(label, filepath.Join(dataTypesDir, name), "datatype", false); err != nil {
			return EngineInputManifest{}, nil, fmt.Errorf("inspect engine datatype %s: %w", name, err)
		}
	}
	return manifest, inputs, nil
}

func readEngineBundle(ctx context.Context, manifest EngineInputManifest, inputs []engineInputDescriptor) (*EngineBundle, error) {
	if manifest.Root == "" {
		return &EngineBundle{Fingerprint: noEngineDataFingerprint}, nil
	}
	bundle := &EngineBundle{
		ScopeRules:  map[string]map[string][]string{},
		RuleOutputs: map[string]map[string][]string{},
		Targets:     map[string][]string{},
		Modifiers:   map[string]ModifierInfo{},
	}
	hash := sha256.New()
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, _ = hash.Write([]byte(input.label + "\x00"))
		if input.missing {
			_, _ = hash.Write([]byte("<missing>\x00"))
			continue
		}
		data, err := os.ReadFile(input.path)
		if err != nil {
			return nil, fmt.Errorf("read engine input %s: %w", input.label, err)
		}
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
		switch input.kind {
		case "modifier":
			bundle.Modifiers = parseEngineModifiers(data)
		case "datatype":
			blocks, err := parseDocBlocks(data)
			if err != nil {
				return nil, fmt.Errorf("parse engine datatype %s: %w", input.label, err)
			}
			appendEngineDatatypeRows(bundle, input.path, blocks)
		default:
			blocks, err := parseDocBlocks(data)
			if err != nil {
				return nil, fmt.Errorf("parse engine %s log: %w", input.kind, err)
			}
			appendEngineScopeRows(bundle, input.path, input.kind, blocks)
		}
	}
	bundle.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	return bundle, nil
}

func appendEngineScopeRows(bundle *EngineBundle, path, kind string, blocks [][]string) {
	for _, lines := range blocks {
		if len(lines) == 0 {
			continue
		}
		head := strings.TrimSpace(lines[0])
		if head == "" || strings.HasSuffix(head, "Documentation:") {
			continue
		}
		name, desc := head, ""
		if a, b, ok := strings.Cut(head, " - "); ok {
			name, desc = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		name = strings.TrimSuffix(strings.TrimSpace(name), ":")
		input, output := "", ""
		var extra []string
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "Supported Scopes:"), strings.HasPrefix(line, "Input Scopes:"), strings.HasPrefix(line, "Expected Scope:"):
				input, _ = inputScopeLine(line)
			case strings.HasPrefix(line, "Output Scopes:"), strings.HasPrefix(line, "Supported Targets:"):
				output, _ = outputScopeLine(line)
			default:
				if strings.TrimSpace(line) != "" && !strings.Contains(line, ": yes") && !strings.Contains(line, ": no") {
					extra = append(extra, strings.TrimSpace(line))
				}
			}
		}
		if desc == "" {
			desc = strings.Join(extra, " ")
		}
		key := strings.ToLower(name)
		if input != "" {
			rules := bundle.ScopeRules[key]
			if rules == nil {
				rules = map[string][]string{}
				bundle.ScopeRules[key] = rules
			}
			rules[kind] = splitScopesWithNone(input, kind == "on_action")
		}
		if output != "" {
			outputs := bundle.RuleOutputs[key]
			if outputs == nil {
				outputs = map[string][]string{}
				bundle.RuleOutputs[key] = outputs
			}
			outputs[kind] = splitScopes(output)
			if kind == "target" {
				bundle.Targets[key] = outputs[kind]
			}
		}
		bundle.ScopeRows = append(bundle.ScopeRows, ScopeEvidence{
			Key:          name,
			RuleKind:     kind,
			InputScopes:  splitScopesWithNone(input, kind == "on_action"),
			OutputScopes: splitScopes(output),
			Description:  desc,
			RuleSource:   filepathSlash(path),
			Confidence:   "high",
		})
	}
}

func appendEngineDatatypeRows(bundle *EngineBundle, path string, blocks [][]string) {
	name := filepath.Base(path)
	category := strings.TrimSuffix(strings.TrimPrefix(name, "data_types_"), filepath.Ext(name))
	for _, lines := range blocks {
		if len(lines) == 0 {
			continue
		}
		signature := strings.TrimSpace(lines[0])
		if signature == "" {
			continue
		}
		name := signature
		if index := strings.Index(name, "("); index >= 0 {
			name = strings.TrimSpace(name[:index])
		}
		info := map[string]string{}
		var description []string
		for _, line := range lines[1:] {
			if key, value, ok := strings.Cut(line, ":"); ok && (key == "Description" || key == "Definition type" || key == "Return type") {
				info[key] = strings.TrimSpace(value)
			} else if strings.TrimSpace(line) != "" {
				description = append(description, strings.TrimSpace(line))
			}
		}
		if info["Description"] == "" {
			info["Description"] = strings.Join(description, " ")
		}
		bundle.Datatypes = append(bundle.Datatypes, DatatypeInfo{
			Name:           name,
			Signature:      signature,
			Description:    info["Description"],
			DefinitionType: info["Definition type"],
			ReturnType:     info["Return type"],
			Category:       category,
			Source:         filepathSlash(path),
		})
	}
}

func parseEngineModifiers(data []byte) map[string]ModifierInfo {
	out := map[string]ModifierInfo{}
	var tag string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Tag:"):
			tag = strings.TrimSpace(strings.TrimPrefix(line, "Tag:"))
		case tag != "" && strings.HasPrefix(line, "Use areas:"):
			out[tag] = ModifierInfo{
				UseAreas: parseModifierUseAreas(strings.TrimSpace(strings.TrimPrefix(line, "Use areas:"))),
				Source:   "engine_log",
			}
			tag = ""
		}
	}
	return out
}

func parseDocBlocks(data []byte) ([][]string, error) {
	var out [][]string
	var current []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "----------------") {
			if len(current) > 0 {
				out = append(out, current)
				current = nil
			}
			continue
		}
		if line != "" {
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out, scanner.Err()
}

func engineManifestsEqual(left, right EngineInputManifest) bool {
	if left.Root != right.Root || len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if left.Files[index] != right.Files[index] {
			return false
		}
	}
	return true
}

func cachedBundleForManifest(manifest EngineInputManifest) *EngineBundle {
	engineBundleCache.Lock()
	defer engineBundleCache.Unlock()
	entry, ok := engineBundleCache.entries[manifest.Root]
	if !ok || !engineManifestsEqual(entry.Manifest, manifest) {
		return nil
	}
	return entry.Bundle
}

func cacheEngineBundle(manifest EngineInputManifest, bundle *EngineBundle) {
	engineBundleCache.Lock()
	defer engineBundleCache.Unlock()
	if _, exists := engineBundleCache.entries[manifest.Root]; !exists {
		engineBundleCache.order = append(engineBundleCache.order, manifest.Root)
	}
	engineBundleCache.entries[manifest.Root] = cachedEngineBundle{Manifest: manifest, Bundle: bundle}
	for len(engineBundleCache.order) > maxCachedEngineBundles {
		oldest := engineBundleCache.order[0]
		engineBundleCache.order = engineBundleCache.order[1:]
		delete(engineBundleCache.entries, oldest)
	}
}
