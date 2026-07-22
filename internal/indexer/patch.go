package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	slashpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

type PatchFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Op      string `json:"op,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

type PreflightPatchInput struct {
	Files []PatchFileInput `json:"files"`
	Limit int              `json:"limit,omitempty"`
}

type VirtualFileAnalysis struct {
	Record        fileRecord
	Kind          string
	Parsed        script.File
	Objects       []objectRow
	Refs          []refRow
	Locs          []locEntry
	Resources     []string
	SchemaEntries []schemaEntry
	ObjectFields  []objectFieldRow
	Diagnostics   []Diagnostic
	SavedScopes   []string
	Variables     []string
}

func AnalyzeVirtualFile(relPath, sourceName string, sourceRank int, content string) (VirtualFileAnalysis, error) {
	rel, err := normalizePatchRelPath(relPath)
	if err != nil {
		return VirtualFileAnalysis{}, err
	}
	kind := classifyVirtualPath(rel)
	if kind == "" {
		return VirtualFileAnalysis{}, fmt.Errorf("unsupported patch file path %q", relPath)
	}
	if sourceName == "" {
		sourceName = "patch"
	}
	if sourceRank <= 0 {
		sourceRank = 1
	}
	rec := fileRecord{
		ID:         -1,
		SourceName: sourceName,
		SourceRank: sourceRank,
		Path:       rel,
		RelPath:    rel,
		Kind:       kind,
	}
	a := VirtualFileAnalysis{Record: rec, Kind: kind}
	switch kind {
	case "script":
		a.Parsed = script.Parse(content)
		for _, pe := range a.Parsed.Errors {
			a.Diagnostics = append(a.Diagnostics, Diagnostic{
				Source:   "parser",
				Severity: "error",
				Code:     "parse_error",
				Message:  pe.Message,
				Path:     rel,
				Line:     pe.Line,
				Column:   pe.Col,
			})
		}
		a.Diagnostics = append(a.Diagnostics, ctxDiagnostics(rel, "compiler", checkScriptContext(a.Parsed.Nodes, rel))...)
		a.Diagnostics = append(a.Diagnostics, ctxDiagnostics(rel, "compiler", checkScriptLint(a.Parsed.Nodes, rel, sourceName))...)
		a.Diagnostics = append(a.Diagnostics, ctxDiagnostics(rel, "compiler", checkScopeTracker(a.Parsed.Nodes, rel))...)
		a.SavedScopes = collectSavedScopes(a.Parsed.Nodes)
		a.Variables = collectVariables(a.Parsed.Nodes)
		if strings.Contains(rel, "scripted_effects") {
			for _, n := range a.Parsed.Nodes {
				if n.Kind == "block" && n.Key != "" {
					a.Diagnostics = append(a.Diagnostics, ctxDiagnostics(rel, "compiler", checkScriptEffectRecursion(a.Parsed.Nodes, rel, n.Key))...)
				}
			}
		}
		a.Objects = extractObjects(rec, a.Parsed.Nodes)
		a.Refs = extractRefs(rec, a.Parsed.Nodes, a.Objects)
		a.ObjectFields = extractObjectFields(rec, a.Parsed.Nodes, a.Objects)
	case "localization":
		a.Locs = parseLocBytes(rel, []byte(content))
	case "schema":
		a.SchemaEntries = parseSchemaBytes(rel, []byte(content))
	case "resource":
		a.Resources = append(a.Resources, normalizeResource(rel))
	}
	return a, nil
}

func normalizePatchRelPath(raw string) (string, error) {
	p := strings.TrimSpace(filepathSlash(raw))
	if p == "" {
		return "", fmt.Errorf("patch file path is required")
	}
	if strings.Contains(p, "\x00") {
		return "", fmt.Errorf("patch file path contains NUL byte")
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(p, "/") || strings.Contains(strings.Split(p, "/")[0], ":") {
		return "", fmt.Errorf("patch file path must be source-root relative: %q", raw)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("patch file path must not contain ..: %q", raw)
		}
	}
	p = strings.TrimPrefix(p, "./")
	clean := slashpath.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("patch file path must be source-root relative: %q", raw)
	}
	return clean, nil
}

func classifyVirtualPath(rel string) string {
	return classifyRel(filepathSlash(rel))
}

func ctxDiagnostics(path, source string, in []ctxDiag) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		out = append(out, Diagnostic{
			Source:   source,
			Severity: d.severity,
			Code:     d.code,
			Message:  d.msg,
			Path:     path,
			Line:     d.line,
			Column:   d.col,
		})
	}
	return out
}

type activeSymbols struct {
	objects      map[string]bool
	locKeys      map[string]bool
	resources    map[string]bool
	dbObjects    map[string]bool
	dbLocKeys    map[string]bool
	dbResources  map[string]bool
	dbObjectMiss map[string]bool
}

func (db *DB) LLMPreflightPatch(ctx context.Context, files []PatchFileInput, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	if len(files) == 0 {
		return LLMResult{}, fmt.Errorf("preflight_patch requires at least one file")
	}
	symbols := activeSymbols{
		objects:      map[string]bool{},
		locKeys:      map[string]bool{},
		resources:    map[string]bool{},
		dbObjects:    map[string]bool{},
		dbLocKeys:    map[string]bool{},
		dbResources:  map[string]bool{},
		dbObjectMiss: map[string]bool{},
	}
	analyses := make([]VirtualFileAnalysis, 0, len(files))
	impact := map[string]int{"files": len(files)}
	var impactEvidence []LLMEvidence
	for i, f := range files {
		op := patchOp(f)
		switch op {
		case "delete":
			rel, err := normalizePatchRelPath(f.Path)
			if err != nil {
				return LLMResult{}, err
			}
			deleted, err := db.objectsForRelPath(ctx, rel)
			if err != nil {
				return LLMResult{}, err
			}
			impact["delete_files"]++
			impact["delete_objects"] += len(deleted)
			if len(deleted) == 0 {
				impactEvidence = append(impactEvidence, LLMEvidence{Kind: "patch_delete_file", Source: "patch", Path: evidencePath(rel), Detail: "no indexed active objects found for this rel_path"})
			}
			for _, obj := range deleted {
				incoming, err := db.refCountForName(ctx, obj.Name)
				if err != nil {
					return LLMResult{}, err
				}
				impact["incoming_refs"] += incoming
				detail := fmt.Sprintf("delete %s:%s", obj.Type, obj.Name)
				if incoming > 0 {
					detail += fmt.Sprintf("; incoming_refs=%d", incoming)
				}
				impactEvidence = append(impactEvidence, LLMEvidence{Kind: "patch_delete_object", Type: obj.Type, Name: obj.Name, Source: "patch", Path: evidencePath(rel), Line: obj.Line, Column: obj.Col, Detail: detail})
			}
			continue
		case "rename":
			if strings.TrimSpace(f.From) == "" || strings.TrimSpace(f.To) == "" {
				return LLMResult{}, fmt.Errorf("rename patch requires from and to")
			}
			impact["rename_objects"]++
			incoming, err := db.refCountForName(ctx, renameName(f.From))
			if err != nil {
				return LLMResult{}, err
			}
			impact["incoming_refs"] += incoming
			impactEvidence = append(impactEvidence, LLMEvidence{Kind: "patch_rename_object", Source: "patch", Name: f.From, Detail: fmt.Sprintf("rename to %s; incoming_refs=%d", f.To, incoming)})
			continue
		case "upsert":
			// handled below
		default:
			return LLMResult{}, fmt.Errorf("unsupported patch op %q", op)
		}
		a, err := AnalyzeVirtualFile(f.Path, "patch", 1, f.Content)
		if err != nil {
			return LLMResult{}, err
		}
		virtualID := int64(-i - 1)
		a.Record.ID = virtualID
		for j := range a.Objects {
			a.Objects[j].FileID = virtualID
		}
		for j := range a.Refs {
			a.Refs[j].FileID = virtualID
		}
		for j := range a.ObjectFields {
			a.ObjectFields[j].FileID = virtualID
		}
		analyses = append(analyses, a)
		impact["upsert_files"]++
	}

	patchObjectSeen := map[string]objectRow{}
	var duplicateDiags []Diagnostic
	replacedPaths := map[string]bool{}
	for _, file := range files {
		if rel, err := normalizePatchRelPath(file.Path); err == nil {
			replacedPaths[filepathSlash(rel)] = true
		}
	}
	counts := map[string]int{
		"files":              len(files),
		"definitions":        0,
		"refs":               0,
		"localization":       0,
		"resources":          0,
		"diagnostics":        0,
		"unresolved_refs":    0,
		"blocking_risks":     0,
		"nonblocking_risks":  0,
		"integrity_warnings": 0,
	}
	for _, a := range analyses {
		counts["definitions"] += len(a.Objects)
		counts["refs"] += len(a.Refs)
		counts["localization"] += len(a.Locs)
		counts["resources"] += len(a.Resources)
		for _, obj := range a.Objects {
			key := obj.Type + ":" + obj.Name
			if obj.Type == "title" && isTitleID(obj.Name) {
				if prev, ok := patchObjectSeen[key]; ok {
					duplicateDiags = append(duplicateDiags, Diagnostic{
						Source: "integrity", Severity: "warning", Code: "duplicate_title_id",
						Message: fmt.Sprintf("duplicate patch title %s also defined at %s:%d", obj.Name, prev.Path, prev.Line),
						Path:    obj.Path, Line: obj.Line, Column: obj.Col, Confidence: "high", Occurrences: 2,
					})
				} else {
					patchObjectSeen[key] = obj
				}
				indexed, err := db.QueryObject(ctx, obj.Name)
				if err != nil {
					return LLMResult{}, err
				}
				for _, definition := range indexed.Definitions {
					if definition.Type != "title" || definition.Rank != 1 || replacedPaths[filepathSlash(definition.LogicalPath)] {
						continue
					}
					duplicateDiags = append(duplicateDiags, Diagnostic{
						Source: "integrity", Severity: "warning", Code: "duplicate_title_id",
						Message: fmt.Sprintf("patch title %s conflicts with active project definition at %s:%d", obj.Name, definition.LogicalPath, definition.Line),
						Path:    obj.Path, Line: obj.Line, Column: obj.Col, Confidence: "high", Occurrences: 2,
					})
					break
				}
			} else if prev, ok := patchObjectSeen[key]; ok {
				_ = prev // mergeable and unclassified namespaces are not global duplicate errors
			} else {
				patchObjectSeen[key] = obj
			}
			symbols.objects[key] = true
			symbols.objects[obj.Name] = true
		}
		for _, loc := range a.Locs {
			symbols.locKeys[loc.key] = true
		}
		for _, res := range a.Resources {
			symbols.resources[res] = true
		}
	}
	activeTitles, err := collectActiveTitleOccurrences(ctx, db.sql)
	if err != nil {
		return LLMResult{}, err
	}
	bestRank := map[string]int{}
	for _, item := range activeTitles {
		if replacedPaths[filepathSlash(item.Path)] {
			continue
		}
		if rank, ok := bestRank[item.Name]; !ok || item.Rank < rank {
			bestRank[item.Name] = item.Rank
		}
	}
	activeProvinceOwner := map[int]titleOccurrence{}
	for _, item := range activeTitles {
		if replacedPaths[filepathSlash(item.Path)] || item.Rank != bestRank[item.Name] || !strings.HasPrefix(item.Name, "b_") || item.ProvinceID <= 0 {
			continue
		}
		if _, exists := activeProvinceOwner[item.ProvinceID]; !exists {
			activeProvinceOwner[item.ProvinceID] = item
		}
	}
	patchProvinceOwner := map[int]objectRow{}
	for _, analysis := range analyses {
		nodes := map[int64]*script.Node{}
		walk(analysis.Parsed.Nodes, func(n *script.Node) { nodes[n.ID] = n })
		for _, obj := range analysis.Objects {
			if obj.Type != "title" || !strings.HasPrefix(obj.Name, "b_") {
				continue
			}
			node := nodes[obj.NodeID]
			provinceID := 0
			if node != nil {
				for _, child := range node.Children {
					if child.Key == "province" {
						provinceID, _ = strconv.Atoi(strings.TrimSpace(child.Value))
						break
					}
				}
			}
			if provinceID <= 0 {
				continue
			}
			if previous, ok := patchProvinceOwner[provinceID]; ok && previous.Name != obj.Name {
				duplicateDiags = append(duplicateDiags, Diagnostic{Source: "integrity", Severity: "warning", Code: "duplicate_barony_province",
					Message: fmt.Sprintf("patch province %d is assigned to both %s and %s", provinceID, previous.Name, obj.Name),
					Path:    obj.Path, Line: obj.Line, Column: obj.Col, Confidence: "high", Occurrences: 2})
			} else {
				patchProvinceOwner[provinceID] = obj
			}
			if previous, ok := activeProvinceOwner[provinceID]; ok && previous.Name != obj.Name {
				duplicateDiags = append(duplicateDiags, Diagnostic{Source: "integrity", Severity: "warning", Code: "duplicate_barony_province",
					Message: fmt.Sprintf("patch barony %s assigns province %d already owned by %s at %s:%d", obj.Name, provinceID, previous.Name, previous.Path, previous.Line),
					Path:    obj.Path, Line: obj.Line, Column: obj.Col, Confidence: "high", Occurrences: 2})
			}
		}
	}

	var diagnostics []Diagnostic
	for _, a := range analyses {
		diagnostics = append(diagnostics, a.Diagnostics...)
	}
	diagnostics = append(diagnostics, duplicateDiags...)
	counts["diagnostics"] = len(diagnostics)
	for _, d := range diagnostics {
		if strings.HasPrefix(d.Code, "duplicate_title") || d.Code == "duplicate_barony_province" || d.Code == "invalid_title_hierarchy" {
			counts["integrity_warnings"]++
		}
	}

	type unresolvedRef struct {
		ref refRow
		rec fileRecord
	}
	var unresolved []unresolvedRef
	for _, a := range analyses {
		for _, ref := range a.Refs {
			resolved, err := db.resolvePatchRef(ctx, ref, symbols)
			if err != nil {
				return LLMResult{}, err
			}
			if resolved {
				continue
			}
			if preflightRuntimeRef(ref.Kind) {
				continue
			}
			unresolved = append(unresolved, unresolvedRef{ref: ref, rec: a.Record})
		}
	}
	counts["unresolved_refs"] = len(unresolved)

	for _, d := range diagnostics {
		if d.Severity == "error" {
			counts["blocking_risks"]++
		} else {
			counts["nonblocking_risks"]++
		}
	}
	for _, u := range unresolved {
		if preflightBlockingRef(u.ref.Kind) {
			counts["blocking_risks"]++
		} else {
			counts["nonblocking_risks"]++
		}
	}

	r := LLMResult{
		Intent:    "preflight_patch",
		Query:     fmt.Sprintf("%d file(s)", len(files)),
		Counts:    counts,
		Impact:    impact,
		NeedsScan: true,
		Guidance: []string{
			"This is a temporary patch preflight; it does not refresh SQLite.",
			"Patch-defined objects, localization keys, and resources are used as an in-memory overlay for this check only.",
			"After writing files to disk, run ck3-index scan --files (or a full scan) and then diag_stats before treating the project as clean.",
		},
		NextQueries: []LLMNextQuery{
			{Tool: "validate_project", Reason: "after writing and scanning, confirm cached diagnostics"},
		},
	}
	if counts["blocking_risks"] == 0 && counts["nonblocking_risks"] == 0 {
		r.Summary = fmt.Sprintf("Patch preflight checked %d file(s): no immediate indexed blockers. definitions=%d refs=%d loc=%d resources=%d.",
			len(analyses), counts["definitions"], counts["refs"], counts["localization"], counts["resources"])
	} else {
		r.Summary = fmt.Sprintf("Patch preflight checked %d file(s): %d blocking risk(s), %d warning risk(s), %d unresolved ref(s).",
			len(analyses), counts["blocking_risks"], counts["nonblocking_risks"], counts["unresolved_refs"])
	}

	add := func(ev LLMEvidence) {
		if len(r.Evidence) < limit {
			r.Evidence = append(r.Evidence, ev)
		}
	}
	sortDiagnostics(diagnostics)
	for _, d := range diagnostics {
		add(diagnosticEvidence(d))
	}
	for _, u := range unresolved {
		add(patchRefEvidence("unresolved_patch_ref", u.ref, u.rec))
	}
	for _, ev := range impactEvidence {
		add(ev)
	}
	for _, a := range analyses {
		for _, obj := range a.Objects {
			add(patchObjectEvidence(obj))
		}
	}
	for _, a := range analyses {
		for _, loc := range a.Locs {
			add(LLMEvidence{
				Kind:   "patch_localization",
				Name:   loc.key,
				Source: a.Record.SourceName,
				Path:   evidencePath(a.Record.RelPath),
				Line:   loc.line,
				Detail: loc.lang + ": " + trimText(loc.val, 180),
			})
		}
		for _, res := range a.Resources {
			add(LLMEvidence{
				Kind:   "patch_resource",
				Name:   res,
				Source: a.Record.SourceName,
				Path:   evidencePath(a.Record.RelPath),
				Detail: strings.TrimPrefix(strings.ToLower(filepath.Ext(res)), "."),
			})
		}
	}
	if len(unresolved) > 0 {
		first := unresolved[0].ref
		r.NextQueries = append([]LLMNextQuery{{Tool: "diagnose_key", ID: refQueryID(first), Reason: "inspect the first unresolved patch reference against the current index"}}, r.NextQueries...)
	}
	for _, a := range analyses {
		if len(a.Objects) > 0 {
			obj := a.Objects[0]
			r.NextQueries = append([]LLMNextQuery{{Tool: "prepare_edit", ID: obj.Type + ":" + obj.Name, Reason: "compare this generated object with indexed examples and rules"}}, r.NextQueries...)
			break
		}
	}
	for _, u := range unresolved {
		switch u.ref.Kind {
		case "localization":
			r.MissingLocKeys = appendUniqueString(r.MissingLocKeys, u.ref.Name)
		case "resource":
			r.MissingResources = appendUniqueString(r.MissingResources, u.ref.Name)
		}
	}
	for _, d := range diagnostics {
		if d.Code == "scope_mismatch" {
			hint := d.Message
			if strings.Contains(hint, "culture = { ... }") {
				hint = "Wrap culture-scope triggers in culture = { ... } or use valid_for_maa_trigger = { PARAMETER = unlock_maa_xxx }."
			}
			r.ScopeFixHints = appendUniqueString(r.ScopeFixHints, hint)
		}
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMImpactPatch(ctx context.Context, files []PatchFileInput, opts LLMOptions) (LLMResult, error) {
	r, err := db.LLMPreflightPatch(ctx, files, opts)
	if err != nil {
		return LLMResult{}, err
	}
	r.Intent = "impact_patch"
	r.Summary = fmt.Sprintf("Patch impact checked %d file operation(s): upsert=%d delete=%d rename=%d, defined=%d, incoming_refs=%d, unresolved_refs=%d.",
		r.Counts["files"], r.Impact["upsert_files"], r.Impact["delete_files"], r.Impact["rename_objects"], r.Counts["definitions"], r.Impact["incoming_refs"], r.Counts["unresolved_refs"])
	r.Guidance = []string{
		"Impact patch is read-only and does not refresh SQLite.",
		"Incoming refs on deleted or renamed objects are the first risk to inspect before editing.",
	}
	return r, nil
}

func (db *DB) LLMPreflightDirty(ctx context.Context, cfg Config, opts LLMOptions) (LLMResult, error) {
	dirty, err := db.DirtyPatchFiles(ctx, cfg, opts.normalizedLimit())
	if err != nil {
		return LLMResult{}, err
	}
	if dirty.Total == 0 {
		return LLMResult{
			Intent:  "preflight_dirty",
			Summary: "No dirty project files were detected against the current SQLite cache.",
			Counts:  map[string]int{"files": 0},
			Guidance: []string{
				"Dirty detection compares current project file hashes with the indexed cache.",
				"Run scan after writing files to refresh SQLite even if no dirty files are reported.",
			},
		}.withPublicFilter(opts), nil
	}
	r, err := db.LLMPreflightPatch(ctx, dirty.Files, opts)
	if err != nil {
		return LLMResult{}, err
	}
	r.Intent = "preflight_dirty"
	r.Query = fmt.Sprintf("%d dirty file(s)", dirty.Total)
	r.Counts["files"] = dirty.Total
	r.Counts["files_checked"] = len(dirty.Files)
	r.Counts["deleted_files"] = dirty.Deleted
	if dirty.Truncated {
		r.Summary = fmt.Sprintf("Dirty project file preflight checked %d of %d relevant dirty file(s); request a higher limit or narrow the edit set before treating the project as clean. %s", len(dirty.Files), dirty.Total, r.Summary)
		r.Guidance = append([]string{"Dirty totals are exact, but patch evidence and validation are capped by the requested limit."}, r.Guidance...)
	} else {
		r.Summary = "Dirty project file preflight: " + r.Summary
	}
	return r, nil
}

type DirtyPatchSet struct {
	Files     []PatchFileInput
	Total     int
	Deleted   int
	Truncated bool
}

func (db *DB) DirtyPatchFiles(ctx context.Context, cfg Config, limit int) (DirtyPatchSet, error) {
	if limit <= 0 {
		limit = defaultLLMLimit
	}
	if limit > 50 {
		limit = 50
	}
	type indexedFile struct {
		rel string
		sha string
	}
	indexed := map[string]indexedFile{}
	rows, err := db.sql.QueryContext(ctx, `SELECT path, rel_path, sha256 FROM files WHERE source_rank=1 AND overridden=0`)
	if err != nil {
		return DirtyPatchSet{}, err
	}
	for rows.Next() {
		var path, rel, sha string
		if err := rows.Scan(&path, &rel, &sha); err != nil {
			rows.Close()
			return DirtyPatchSet{}, err
		}
		indexed[path] = indexedFile{rel: filepath.ToSlash(rel), sha: sha}
	}
	if err := rows.Close(); err != nil {
		return DirtyPatchSet{}, err
	}
	type dirtyCandidate struct {
		path string
		rel  string
		op   string
	}
	var candidates []dirtyCandidate
	seen := map[string]bool{}
	for _, src := range cfg.Sources {
		if src.Rank != 1 || src.Path == "" {
			continue
		}
		err := filepath.WalkDir(src.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, relErr := filepath.Rel(src.Path, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if shouldPruneSourceDir(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			kind := classifyRel(rel)
			if kind == "" {
				return nil
			}
			seen[path] = true
			sum, err := shaFile(path)
			if err != nil {
				return nil
			}
			if prev, ok := indexed[path]; ok && prev.sha == sum {
				return nil
			}
			candidates = append(candidates, dirtyCandidate{path: path, rel: rel, op: "upsert"})
			return nil
		})
		if err != nil {
			return DirtyPatchSet{}, err
		}
	}
	deleted := 0
	for path, prev := range indexed {
		if seen[path] || classifyRel(prev.rel) == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			continue
		}
		deleted++
		candidates = append(candidates, dirtyCandidate{path: path, rel: prev.rel, op: "delete"})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rel == candidates[j].rel {
			return candidates[i].op < candidates[j].op
		}
		return candidates[i].rel < candidates[j].rel
	})
	result := DirtyPatchSet{Total: len(candidates), Deleted: deleted, Truncated: len(candidates) > limit}
	for _, candidate := range candidates {
		if len(result.Files) >= limit {
			break
		}
		input := PatchFileInput{Path: candidate.rel, Op: candidate.op}
		if candidate.op != "delete" {
			data, err := os.ReadFile(candidate.path)
			if err != nil {
				continue
			}
			input.Content = string(data)
		}
		result.Files = append(result.Files, input)
	}
	return result, nil
}

func (db *DB) resolvePatchRef(ctx context.Context, r refRow, symbols activeSymbols) (bool, error) {
	switch r.Kind {
	case "localization":
		if symbols.locKeys[r.Name] {
			return true, nil
		}
		return db.activeLocalizationExists(ctx, r.Name, symbols)
	case "resource":
		if symbols.resources[r.Name] {
			return true, nil
		}
		return db.activeResourceExists(ctx, r.Name, symbols)
	case "sound":
		return IsSound(r.Name), nil
	case "iterator":
		_, ok := iteratorScopeIn[r.Name]
		return ok, nil
	case "scope_transition":
		_, ok := engineScopeTransitionsIn[r.Name]
		return ok, nil
	case "define":
		_, ok := engineDefines[r.Name]
		return ok, nil
	case "flag", "global_var":
		return true, nil
	default:
		if symbols.objects[r.Kind+":"+r.Name] || symbols.objects[r.Name] {
			return true, nil
		}
		return db.activeObjectExists(ctx, r.Kind, r.Name, symbols)
	}
}

func (db *DB) activeLocalizationExists(ctx context.Context, key string, symbols activeSymbols) (bool, error) {
	if v, ok := symbols.dbLocKeys[key]; ok {
		return v, nil
	}
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT 1
		FROM localization l JOIN files f ON f.id=l.file_id
		WHERE l.key=? AND f.overridden=0 LIMIT 1`, key).Scan(&n)
	if err == sql.ErrNoRows {
		symbols.dbLocKeys[key] = false
		return false, nil
	}
	if err != nil {
		return false, err
	}
	symbols.dbLocKeys[key] = true
	return true, nil
}

func (db *DB) activeResourceExists(ctx context.Context, name string, symbols activeSymbols) (bool, error) {
	if v, ok := symbols.dbResources[name]; ok {
		return v, nil
	}
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT 1
		FROM resources r JOIN files f ON f.id=r.file_id
		WHERE r.resource_path=? AND f.overridden=0 LIMIT 1`, name).Scan(&n)
	if err == sql.ErrNoRows {
		symbols.dbResources[name] = false
		return false, nil
	}
	if err != nil {
		return false, err
	}
	symbols.dbResources[name] = true
	return true, nil
}

func (db *DB) activeObjectExists(ctx context.Context, kind, name string, symbols activeSymbols) (bool, error) {
	key := kind + ":" + name
	if v, ok := symbols.dbObjects[key]; ok {
		return v, nil
	}
	if symbols.dbObjectMiss[key] {
		return false, nil
	}
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT 1
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.object_type=? AND o.name=? AND f.overridden=0
		LIMIT 1`, kind, name).Scan(&n)
	if err == sql.ErrNoRows {
		err = db.sql.QueryRowContext(ctx, `SELECT 1
			FROM objects o JOIN files f ON f.id=o.file_id
			WHERE o.name=? AND f.overridden=0
			LIMIT 1`, name).Scan(&n)
	}
	if err == sql.ErrNoRows {
		symbols.dbObjectMiss[key] = true
		return false, nil
	}
	if err != nil {
		return false, err
	}
	symbols.dbObjects[key] = true
	return true, nil
}

func (db *DB) objectsForRelPath(ctx context.Context, rel string) ([]objectRow, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,o.file_id,o.node_local_id,o.source_name,o.source_rank,o.path,o.line,o.col
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.rel_path=? AND f.overridden=0
		ORDER BY o.source_rank,o.object_type,o.name`, rel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []objectRow
	for rows.Next() {
		var obj objectRow
		if err := rows.Scan(&obj.Type, &obj.Name, &obj.FileID, &obj.NodeID, &obj.SourceName, &obj.SourceRank, &obj.Path, &obj.Line, &obj.Col); err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

func (db *DB) refCountForName(ctx context.Context, name string) (int, error) {
	_, n, typed := splitTypedID(name)
	if typed {
		name = n
	}
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE r.ref_name=? AND f.overridden=0`, name).Scan(&count)
	return count, err
}

func patchOp(f PatchFileInput) string {
	op := strings.ToLower(strings.TrimSpace(f.Op))
	if op == "" {
		return "upsert"
	}
	return op
}

func renameName(id string) string {
	_, name, typed := splitTypedID(id)
	if typed {
		return name
	}
	return id
}

func appendUniqueString(in []string, s string) []string {
	if s == "" {
		return in
	}
	for _, existing := range in {
		if existing == s {
			return in
		}
	}
	return append(in, s)
}

func patchObjectEvidence(o objectRow) LLMEvidence {
	return objectEvidence("patch_defined_object", ObjectDef{
		Type:   o.Type,
		Name:   o.Name,
		Source: o.SourceName,
		Rank:   o.SourceRank,
		Path:   o.Path,
		Line:   o.Line,
		Column: o.Col,
	})
}

func patchRefEvidence(kind string, r refRow, rec fileRecord) LLMEvidence {
	name := r.FromName
	if name == "" {
		name = r.Name
	}
	detail := r.Kind
	if r.Name != "" {
		detail += " ref=" + r.Name
	}
	if r.Raw != "" && r.Raw != r.Name {
		detail += " raw=" + r.Raw
	}
	detail += " [unresolved]"
	suggestion, ruleSource := refHint(r.Kind)
	return LLMEvidence{
		Kind:       kind,
		Type:       r.FromType,
		Name:       name,
		Source:     rec.SourceName,
		Path:       evidencePath(rec.RelPath),
		Line:       r.Line,
		Column:     r.Col,
		Detail:     detail,
		Suggestion: suggestion,
		RuleSource: ruleSource,
	}
}

func refQueryID(r refRow) string {
	if r.Kind != "" && r.Name != "" {
		return r.Kind + ":" + r.Name
	}
	return r.Name
}

func sortDiagnostics(diags []Diagnostic) {
	sort.SliceStable(diags, func(i, j int) bool {
		ri, rj := diagnosticSeverityRank(diags[i].Severity), diagnosticSeverityRank(diags[j].Severity)
		if ri != rj {
			return ri < rj
		}
		if diags[i].Path != diags[j].Path {
			return diags[i].Path < diags[j].Path
		}
		if diags[i].Line != diags[j].Line {
			return diags[i].Line < diags[j].Line
		}
		return diags[i].Column < diags[j].Column
	})
}

func diagnosticSeverityRank(sev string) int {
	switch sev {
	case "error":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}
