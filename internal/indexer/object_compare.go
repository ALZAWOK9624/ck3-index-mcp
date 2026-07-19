package indexer

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"ck3-index/internal/script"
)

// ObjectCompareOptions selects the two configured layers used for a read-only
// object comparison. Both names must come from Config.Sources. When Source is
// omitted, the project source (or the highest-priority source) is selected;
// when Base is omitted, the nearest lower-precedence source that actually
// declares the identity is selected. The compared id is intentionally a
// separate typed argument so a caller cannot accidentally request a broad
// filesystem comparison.
type ObjectCompareOptions struct {
	Source string `json:"source,omitempty"`
	Base   string `json:"base,omitempty"`
	// Limit applies one caller-provided output bound to each source/base
	// candidate list and to field changes. A non-positive value preserves the
	// intentionally asymmetric P0 defaults (16 candidates, 64 fields).
	Limit int `json:"limit,omitempty"`
}

// ObjectCompareResult is a bounded, source-root-relative comparison of one
// exact object identity. It deliberately contains no physical source paths,
// raw script text, diagnostics, or write instructions.
//
// P0 supports normal .txt script objects under common/ and events/. Candidates
// must occur in the same source-root-relative directory before they can be
// paired; this permits the useful Vanilla Compare case where a definition moved
// between files without pretending an arbitrary cross-directory match is safe.
type ObjectCompareResult struct {
	Intent                string                     `json:"intent"`
	Status                string                     `json:"status"`
	ID                    string                     `json:"id"`
	Type                  string                     `json:"type"`
	Name                  string                     `json:"name"`
	Scope                 string                     `json:"scope"`
	Source                string                     `json:"source"`
	SourceRank            int                        `json:"source_rank"`
	Base                  string                     `json:"base,omitempty"`
	BaseRank              int                        `json:"base_rank,omitempty"`
	BaseSelection         string                     `json:"base_selection,omitempty"`
	ComparedDir           string                     `json:"compared_directory,omitempty"`
	Reason                string                     `json:"reason,omitempty"`
	SourceCandidates      []ObjectCompareCandidate   `json:"source_candidates,omitempty"`
	BaseCandidates        []ObjectCompareCandidate   `json:"base_candidates,omitempty"`
	SourceTruncated       bool                       `json:"source_candidates_truncated,omitempty"`
	BaseTruncated         bool                       `json:"base_candidates_truncated,omitempty"`
	SourceParseErrorFiles int                        `json:"source_parse_error_files,omitempty"`
	BaseParseErrorFiles   int                        `json:"base_parse_error_files,omitempty"`
	AST                   *ObjectCompareAST          `json:"ast,omitempty"`
	FieldChanges          []ObjectCompareFieldChange `json:"field_changes,omitempty"`
	FieldsTruncated       bool                       `json:"field_changes_truncated,omitempty"`
	Guidance              []string                   `json:"guidance"`
}

// ObjectCompareCandidate identifies one parsed declaration without exposing
// its contents. CanonicalHash is comment/whitespace-insensitive and lets a
// caller distinguish exact structural equality from a matched identity.
type ObjectCompareCandidate struct {
	Path          string `json:"path"`
	Directory     string `json:"directory"`
	Line          int    `json:"line"`
	Column        int    `json:"column"`
	EndLine       int    `json:"end_line"`
	EndColumn     int    `json:"end_column"`
	CanonicalHash string `json:"canonical_hash"`
	ParseErrors   int    `json:"parse_errors,omitempty"`
}

// ObjectCompareAST is the whole-object canonical comparison. Equal means the
// parsed ASTs match after comments and whitespace have been discarded.
type ObjectCompareAST struct {
	Status     string `json:"status"`
	Equal      bool   `json:"equal"`
	SourceHash string `json:"source_hash"`
	BaseHash   string `json:"base_hash"`
}

// ObjectCompareFieldChange summarizes a direct child field relative to the
// selected source. added means source-only; removed means base-only; changed
// means both sides contain one field with different canonical subtrees.
// Hashes intentionally stand in for raw values so this core remains safe for
// later public/read-only adapters.
type ObjectCompareFieldChange struct {
	Field             string `json:"field"`
	Classification    string `json:"classification"`
	SourceOccurrences int    `json:"source_occurrences"`
	BaseOccurrences   int    `json:"base_occurrences"`
	SourceHash        string `json:"source_hash,omitempty"`
	BaseHash          string `json:"base_hash,omitempty"`
}

type objectCompareParsedCandidate struct {
	public ObjectCompareCandidate
	nodeID int64
	node   *script.Node
}

type objectCompareScan struct {
	candidates []objectCompareParsedCandidate
	truncated  bool
	// parseErrorFiles intentionally counts only files that contain a parsed
	// candidate or lexically mention this exact requested identity. A broken
	// unrelated common/events file must not turn a reliable presence result
	// into unsupported.
	parseErrorFiles int
}

const (
	objectCompareCandidateLimit = 16
	objectCompareFieldLimit     = 64
	objectCompareLimitMaximum   = 500
	objectCompareScope          = "common_events_txt_same_directory"
)

type objectCompareIndexedCandidate struct {
	public ObjectCompareCandidate
	nodeID int64
}

type objectCompareIndexedFile struct {
	mtimeNanos  int64
	size        int64
	candidates  map[string][]objectCompareIndexedCandidate
	parseErrors int
}

// objectCompareSourceCache keeps a compact, source-local declaration index.
// It deliberately does not retain parsed ASTs: the index makes repeated
// object queries avoid reparsing every common/events file, while a matched
// comparison reparses only its one candidate file per layer for the AST view.
// Filesystem metadata refreshes the affected file entries before every query.
type objectCompareSourceCache struct {
	mu           sync.Mutex
	files        map[string]objectCompareIndexedFile
	candidates   map[string][]objectCompareIndexedCandidate
	indexedFiles int
}

var objectCompareSourceCaches = struct {
	sync.Mutex
	byKey map[string]*objectCompareSourceCache
}{byKey: map[string]*objectCompareSourceCache{}}

// CompareObjectAgainstBase compares exactly one typed object id between two
// configured source layers. It reads source files on demand instead of using
// the active-object database because normal indexing intentionally skips
// lower-priority files hidden by same-path and replace_path overrides.
//
// This is an analysis primitive only. It never changes the index, emits no
// diagnostics, and never writes or migrates source text.
func CompareObjectAgainstBase(ctx context.Context, cfg Config, typedID string, options ObjectCompareOptions) (ObjectCompareResult, error) {
	if err := validateSources(cfg.Sources); err != nil {
		return ObjectCompareResult{}, err
	}
	typ, name, typed := splitTypedID(strings.TrimSpace(typedID))
	if !typed {
		return ObjectCompareResult{}, fmt.Errorf("object compare requires an exact typed id such as trait:brave")
	}
	typ = strings.ToLower(strings.TrimSpace(typ))
	name = strings.TrimSpace(name)
	if typ == "" || name == "" {
		return ObjectCompareResult{}, fmt.Errorf("object compare requires a non-empty typed id")
	}

	source, err := overrideAuditSource(cfg, options.Source)
	if err != nil {
		return ObjectCompareResult{}, err
	}
	result := ObjectCompareResult{
		Intent:     "object_upstream_compare",
		Status:     "unavailable",
		ID:         typ + ":" + name,
		Type:       typ,
		Name:       name,
		Scope:      objectCompareScope,
		Source:     source.Name,
		SourceRank: source.Rank,
		Guidance: []string{
			"This is read-only comparison evidence. It never writes, merges, migrates, or changes diagnostics.",
			"P0 pairs exact typed identities only when both declarations occur in the same source-root-relative common/ or events/ directory; different filenames are allowed.",
			"Canonical hashes ignore comments and whitespace. Field added/removed are relative to the selected source, and a hash difference is not proof of runtime behavior.",
			"When base is omitted, comparison falls through lower-precedence layers until it finds the nearest layer that actually declares the requested identity. An explicit base never falls through.",
		},
	}
	baseCandidates, err := overrideAuditBaseCandidates(cfg, source, options.Base)
	if err != nil {
		return ObjectCompareResult{}, err
	}
	if len(baseCandidates) == 0 {
		result.BaseSelection = "unavailable"
		result.Reason = "no lower-precedence configured base source is available"
		return result, nil
	}

	if !objectCompareTypeSupported(typ) {
		result.Status = "unsupported"
		result.Reason = "P0 supports parsed .txt script objects under common/ and events/ only; this object type requires a later directory-specific comparator"
		return result, nil
	}
	if typ == "on_action" {
		result.Status = "unsupported"
		result.Reason = "on_action declarations use CK3 merge semantics and require the dedicated contract/merge view instead of a unique-object comparator"
		return result, nil
	}

	candidateLimit, fieldLimit := objectCompareLimits(options.Limit)
	sourceScan, err := scanObjectCompareSource(ctx, source, typ, name, candidateLimit)
	if err != nil {
		return result, err
	}
	base, baseScan, baseSelection, err := selectObjectCompareBase(ctx, baseCandidates, typ, name, candidateLimit, strings.TrimSpace(options.Base) != "")
	if err != nil {
		return result, err
	}
	result.BaseSelection = baseSelection
	if base.Name != "" {
		result.Base, result.BaseRank = base.Name, base.Rank
	}
	result.SourceCandidates = objectComparePublicCandidates(sourceScan.candidates)
	result.BaseCandidates = objectComparePublicCandidates(baseScan.candidates)
	result.SourceTruncated = sourceScan.truncated
	result.BaseTruncated = baseScan.truncated
	result.SourceParseErrorFiles = sourceScan.parseErrorFiles
	result.BaseParseErrorFiles = baseScan.parseErrorFiles

	switch {
	case sourceScan.truncated || baseScan.truncated:
		result.Status = "ambiguous"
		result.Reason = "candidate output reached its bounded limit; no unique comparison is claimed"
	case len(sourceScan.candidates) > 1 || len(baseScan.candidates) > 1:
		result.Status = "ambiguous"
		result.Reason = "more than one declaration matched the exact typed identity in a selected source layer"
	case sourceScan.parseErrorFiles > 0 || baseScan.parseErrorFiles > 0:
		result.Status = "unsupported"
		result.Reason = "a file that contains or may contain the requested identity did not parse cleanly, so a deterministic presence result is not reliable"
	case len(sourceScan.candidates) == 0 && len(baseScan.candidates) == 0:
		result.Status = "not_found"
		if baseSelection == "explicit" {
			result.Reason = "the exact typed identity was not found in the selected source or explicitly selected base layer"
		} else {
			result.Reason = "the exact typed identity was not found in the selected source or any lower-precedence configured layer"
		}
	case len(sourceScan.candidates) == 1 && len(baseScan.candidates) == 0:
		result.Status = "source_only"
		if baseSelection == "explicit" {
			result.Reason = "the exact typed identity is present in the selected source but absent from the explicitly selected base layer"
		} else {
			result.Reason = "the exact typed identity is present in the selected source and absent from every lower-precedence configured layer"
		}
	case len(sourceScan.candidates) == 0 && len(baseScan.candidates) == 1:
		result.Status = "base_only"
		result.Reason = "the exact typed identity is absent from the selected source and present in the selected base layer"
	default:
		left := sourceScan.candidates[0]
		right := baseScan.candidates[0]
		if left.public.Directory != right.public.Directory {
			result.Status = "ambiguous"
			result.Reason = "matching identities occur in different directories; P0 deliberately does not infer a cross-directory equivalence"
			return result, nil
		}
		left, err = hydrateObjectCompareCandidate(ctx, source, left)
		if err != nil {
			return result, err
		}
		right, err = hydrateObjectCompareCandidate(ctx, base, right)
		if err != nil {
			return result, err
		}
		result.Status = "matched"
		result.ComparedDir = left.public.Directory
		result.AST = compareObjectAST(left.node, right.node)
		result.FieldChanges, result.FieldsTruncated = compareObjectFieldsWithLimit(left.node, right.node, fieldLimit)
	}
	return result, nil
}

func objectCompareLimits(requested int) (candidateLimit, fieldLimit int) {
	if requested <= 0 {
		return objectCompareCandidateLimit, objectCompareFieldLimit
	}
	if requested > objectCompareLimitMaximum {
		requested = objectCompareLimitMaximum
	}
	return requested, requested
}

// selectObjectCompareBase makes omitted-base comparison follow the configured
// source order until it reaches the nearest lower-precedence declaration. A
// clean lower layer that does not contain the object is not itself a baseline.
// Conversely, a lower layer with a relevant parse failure must stop the search:
// silently skipping it could invent a farther upstream baseline.
func selectObjectCompareBase(ctx context.Context, candidates []Source, typ, name string, candidateLimit int, explicit bool) (Source, objectCompareScan, string, error) {
	for _, candidate := range candidates {
		scan, err := scanObjectCompareSource(ctx, candidate, typ, name, candidateLimit)
		if err != nil {
			return Source{}, objectCompareScan{}, "", err
		}
		if explicit {
			return candidate, scan, "explicit", nil
		}
		if scan.truncated || len(scan.candidates) > 0 {
			return candidate, scan, "nearest_lower_definition", nil
		}
		if scan.parseErrorFiles > 0 {
			return candidate, scan, "uncertain_lower_definition", nil
		}
	}
	return Source{}, objectCompareScan{}, "no_lower_definition", nil
}

func objectCompareTypeSupported(typ string) bool {
	// These namespaces are known to originate outside common/ or events/ and
	// must not be silently searched in an unrelated directory during P0.
	switch typ {
	case "character", "province_history", "war", "artifact_history", "gui", "gui_template", "localization", "resource":
		return false
	default:
		return true
	}
}

func scanObjectCompareSource(ctx context.Context, source Source, typ, name string, candidateLimit int) (objectCompareScan, error) {
	roots := objectCompareRootsForType(typ)
	cache := objectCompareCacheFor(source, roots)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := cache.refresh(ctx, source, roots); err != nil {
		return objectCompareScan{}, err
	}
	return cache.lookup(ctx, source, typ, name, candidateLimit)
}

func objectCompareRootsForType(typ string) []string {
	switch typ {
	case "event":
		return []string{"events"}
	case "scripted_variable":
		// Scripted variables are legal substitutions in both ordinary common
		// definitions and events. Searching only common silently loses the
		// event-side baseline.
		return []string{"common", "events"}
	default:
		return []string{"common"}
	}
}

func objectCompareCacheFor(source Source, roots []string) *objectCompareSourceCache {
	key := strings.Join([]string{filepath.Clean(source.Path), strings.ToLower(source.Name), fmt.Sprintf("%d", source.Rank), strings.Join(roots, ",")}, "\x00")
	objectCompareSourceCaches.Lock()
	defer objectCompareSourceCaches.Unlock()
	if cache := objectCompareSourceCaches.byKey[key]; cache != nil {
		return cache
	}
	cache := &objectCompareSourceCache{
		files:      map[string]objectCompareIndexedFile{},
		candidates: map[string][]objectCompareIndexedCandidate{},
	}
	objectCompareSourceCaches.byKey[key] = cache
	return cache
}

// refresh tracks all candidate files through metadata and reparses only new
// or changed files. The unavoidable directory walk is much cheaper than
// reparsing every script on each interactive compare request, and preserves
// correctness for files hidden by normal active-index override rules.
func (cache *objectCompareSourceCache) refresh(ctx context.Context, source Source, roots []string) error {
	seen := make(map[string]bool)
	for _, root := range roots {
		rootPath := filepath.Join(source.Path, root)
		info, err := os.Stat(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read object compare source %q: %w", source.Name, err)
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(source.Path, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if entry.IsDir() {
				if shouldPruneSourceDir(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			if !isObjectCompareScriptPath(rel) {
				return nil
			}
			fileInfo, err := entry.Info()
			if err != nil {
				return err
			}
			seen[rel] = true
			cached, ok := cache.files[rel]
			if ok && cached.size == fileInfo.Size() && cached.mtimeNanos == fileInfo.ModTime().UnixNano() {
				return nil
			}
			indexed, err := indexObjectCompareFile(source, path, rel, fileInfo.Size(), fileInfo.ModTime().UnixNano())
			if err != nil {
				return err
			}
			if ok {
				cache.removeFile(rel, cached)
			}
			cache.files[rel] = indexed
			cache.addFile(indexed)
			cache.indexedFiles++
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan object compare source %q: %w", source.Name, err)
		}
	}
	for rel, indexed := range cache.files {
		if !seen[rel] {
			cache.removeFile(rel, indexed)
			delete(cache.files, rel)
		}
	}
	return nil
}

func indexObjectCompareFile(source Source, path, rel string, size, mtimeNanos int64) (objectCompareIndexedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return objectCompareIndexedFile{}, err
	}
	parsed := script.Parse(string(data))
	nodes := make(map[int64]*script.Node)
	walk(parsed.Nodes, func(node *script.Node) { nodes[node.ID] = node })
	indexed := objectCompareIndexedFile{
		mtimeNanos:  mtimeNanos,
		size:        size,
		parseErrors: len(parsed.Errors),
		candidates:  map[string][]objectCompareIndexedCandidate{},
	}
	record := fileRecord{SourceName: source.Name, SourceRank: source.Rank, Path: path, RelPath: rel}
	for _, object := range extractObjects(record, parsed.Nodes) {
		node := nodes[object.NodeID]
		if node == nil {
			continue
		}
		identity := objectCompareIdentity(object.Type, object.Name)
		indexed.candidates[identity] = append(indexed.candidates[identity], objectCompareIndexedCandidate{
			public: ObjectCompareCandidate{
				Path:          rel,
				Directory:     filepath.ToSlash(filepath.Dir(rel)),
				Line:          node.Line,
				Column:        node.Col,
				EndLine:       node.EndLine,
				EndColumn:     node.EndCol,
				CanonicalHash: objectCompareNodeHash(node),
				ParseErrors:   len(parsed.Errors),
			},
			nodeID: object.NodeID,
		})
	}
	return indexed, nil
}

func (cache *objectCompareSourceCache) addFile(indexed objectCompareIndexedFile) {
	for identity, candidates := range indexed.candidates {
		cache.candidates[identity] = append(cache.candidates[identity], candidates...)
	}
}

func (cache *objectCompareSourceCache) removeFile(rel string, indexed objectCompareIndexedFile) {
	for identity := range indexed.candidates {
		current := cache.candidates[identity]
		kept := current[:0]
		for _, candidate := range current {
			if candidate.public.Path != rel {
				kept = append(kept, candidate)
			}
		}
		if len(kept) == 0 {
			delete(cache.candidates, identity)
			continue
		}
		cache.candidates[identity] = kept
	}
}

func (cache *objectCompareSourceCache) lookup(ctx context.Context, source Source, typ, name string, candidateLimit int) (objectCompareScan, error) {
	identity := objectCompareIdentity(typ, name)
	all := append([]objectCompareIndexedCandidate(nil), cache.candidates[identity]...)
	sort.Slice(all, func(i, j int) bool {
		left, right := all[i].public, all[j].public
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		return left.Column < right.Column
	})
	out := objectCompareScan{}
	if len(all) > candidateLimit {
		out.truncated = true
		all = all[:candidateLimit]
	}
	for _, candidate := range all {
		out.candidates = append(out.candidates, objectCompareParsedCandidate{public: candidate.public, nodeID: candidate.nodeID})
	}

	// Candidate-file parse failures are always relevant. For malformed files
	// where extraction lost the declaration entirely, use lexer tokens rather
	// than a raw substring so an unrelated comment does not poison the result.
	relevantParseFiles := map[string]bool{}
	for _, candidate := range cache.candidates[identity] {
		if candidate.public.ParseErrors > 0 {
			relevantParseFiles[candidate.public.Path] = true
		}
	}
	for rel, indexed := range cache.files {
		if indexed.parseErrors == 0 || relevantParseFiles[rel] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		path := filepath.Join(source.Path, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			return out, fmt.Errorf("read object compare source %q: %w", source.Name, err)
		}
		if objectCompareParseErrorMayAffectIdentity(string(data), typ, name) {
			relevantParseFiles[rel] = true
		}
	}
	out.parseErrorFiles = len(relevantParseFiles)
	return out, nil
}

func objectCompareIdentity(typ, name string) string {
	return strings.ToLower(strings.TrimSpace(typ)) + "\x00" + strings.TrimSpace(name)
}

func objectCompareParseErrorMayAffectIdentity(text, _ string, name string) bool {
	tokens := script.Lex(text)
	for i, token := range tokens {
		if (token.Kind != script.TokenIdent && token.Kind != script.TokenString) || token.Text != name {
			continue
		}
		// A value occurrence such as `description = brave` does not make a
		// malformed file a plausible definition of trait:brave. Require the
		// requested token to be in key position, which CK3 syntax marks with an
		// assignment operator or an implicit block opener.
		if i+1 < len(tokens) && (tokens[i+1].Kind == script.TokenOperator || tokens[i+1].Kind == script.TokenLBrace) {
			return true
		}
	}
	return false
}

// hydrateObjectCompareCandidate deliberately reparses only the uniquely
// selected candidate file. The hash check turns a source edit racing an
// interactive request into a retryable error instead of a stale comparison.
func hydrateObjectCompareCandidate(ctx context.Context, source Source, candidate objectCompareParsedCandidate) (objectCompareParsedCandidate, error) {
	if err := ctx.Err(); err != nil {
		return candidate, err
	}
	path := filepath.Join(source.Path, filepath.FromSlash(candidate.public.Path))
	data, err := os.ReadFile(path)
	if err != nil {
		return candidate, fmt.Errorf("read object compare source %q: %w", source.Name, err)
	}
	parsed := script.Parse(string(data))
	if len(parsed.Errors) > 0 {
		return candidate, fmt.Errorf("object compare source %q changed during candidate read; retry the comparison", source.Name)
	}
	var node *script.Node
	walk(parsed.Nodes, func(current *script.Node) {
		if current.ID == candidate.nodeID {
			node = current
		}
	})
	if node == nil || objectCompareNodeHash(node) != candidate.public.CanonicalHash {
		return candidate, fmt.Errorf("object compare source %q changed during candidate read; retry the comparison", source.Name)
	}
	candidate.node = node
	return candidate, nil
}

func isObjectCompareScriptPath(rel string) bool {
	rel = filepath.ToSlash(strings.ToLower(rel))
	if !strings.HasSuffix(rel, ".txt") || classifyRel(rel) != "script" {
		return false
	}
	return strings.HasPrefix(rel, "common/") || strings.HasPrefix(rel, "events/")
}

func objectComparePublicCandidates(candidates []objectCompareParsedCandidate) []ObjectCompareCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]ObjectCompareCandidate, len(candidates))
	for i := range candidates {
		out[i] = candidates[i].public
	}
	return out
}

func compareObjectAST(source, base *script.Node) *ObjectCompareAST {
	sourceHash := objectCompareNodeHash(source)
	baseHash := objectCompareNodeHash(base)
	status := "changed"
	if sourceHash == baseHash {
		status = "identical"
	}
	return &ObjectCompareAST{Status: status, Equal: sourceHash == baseHash, SourceHash: sourceHash, BaseHash: baseHash}
}

func compareObjectFields(source, base *script.Node) ([]ObjectCompareFieldChange, bool) {
	return compareObjectFieldsWithLimit(source, base, objectCompareFieldLimit)
}

func compareObjectFieldsWithLimit(source, base *script.Node, limit int) ([]ObjectCompareFieldChange, bool) {
	sourceFields := objectCompareDirectFields(source)
	baseFields := objectCompareDirectFields(base)
	keys := make(map[string]bool, len(sourceFields)+len(baseFields))
	for key := range sourceFields {
		keys[key] = true
	}
	for key := range baseFields {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	var out []ObjectCompareFieldChange
	for _, key := range ordered {
		sourceNodes, baseNodes := sourceFields[key], baseFields[key]
		change := ObjectCompareFieldChange{Field: key, SourceOccurrences: len(sourceNodes), BaseOccurrences: len(baseNodes)}
		switch {
		case len(sourceNodes) == 0:
			change.Classification = "removed"
			if len(baseNodes) == 1 {
				change.BaseHash = objectCompareNodeHash(baseNodes[0])
			}
		case len(baseNodes) == 0:
			change.Classification = "added"
			if len(sourceNodes) == 1 {
				change.SourceHash = objectCompareNodeHash(sourceNodes[0])
			}
		case objectCompareNodeSequencesEqual(sourceNodes, baseNodes):
			// Repeated direct fields are common in CK3. Equal repeated
			// occurrences are not an ambiguity: their complete ordered
			// canonical sequence is the same on both sides.
			continue
		case len(sourceNodes) != 1 || len(baseNodes) != 1:
			// A direct field can legally be repeated in some CK3 blocks. Do not
			// manufacture a positional pairing; retain it as an explicit review
			// finding while the object-level AST result stays authoritative.
			change.Classification = "ambiguous"
		case objectCompareNodeHash(sourceNodes[0]) != objectCompareNodeHash(baseNodes[0]):
			change.Classification = "changed"
			change.SourceHash = objectCompareNodeHash(sourceNodes[0])
			change.BaseHash = objectCompareNodeHash(baseNodes[0])
		default:
			continue
		}
		if len(out) >= limit {
			return out, true
		}
		out = append(out, change)
	}
	return out, false
}

func objectCompareNodeSequencesEqual(left, right []*script.Node) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if objectCompareNodeHash(left[i]) != objectCompareNodeHash(right[i]) {
			return false
		}
	}
	return true
}

func objectCompareDirectFields(node *script.Node) map[string][]*script.Node {
	out := make(map[string][]*script.Node)
	if node == nil {
		return out
	}
	for _, child := range node.Children {
		if child == nil || child.Key == "" {
			continue
		}
		out[child.Key] = append(out[child.Key], child)
	}
	return out
}

func objectCompareNodeHash(node *script.Node) string {
	if node == nil {
		return ""
	}
	sum := canonicalOverrideNodeHash(node)
	return hex.EncodeToString(sum[:])
}
