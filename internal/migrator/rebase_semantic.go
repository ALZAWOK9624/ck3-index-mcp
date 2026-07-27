package migrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"ck3-index/internal/indexer"
	"ck3-index/internal/script"
)

// rebaseSemanticIndexes is intentionally lazy. Hash classification is cheap
// and must finish before the engine parses a whole Mod tree; the complete
// semantic lookup table is only materialized when a real both-changed Jomini
// file needs it.
type rebaseSemanticIndexes struct {
	base    rebaseSemanticLayer
	project rebaseSemanticLayer
	target  rebaseSemanticLayer
	once    sync.Once
	err     error
}

type rebaseSemanticLayer struct {
	root          string
	files         []SnapshotFile
	recordsByID   map[string][]rebaseSemanticRecord
	recordsByPath map[string][]rebaseSemanticRecord
	parseErrors   map[string]string
}

type rebaseSemanticRecord struct {
	ID     string
	Path   string
	Raw    string
	Digest string
	Node   *script.Node
}

func buildRebaseSemanticIndexes(_ context.Context, base, project, target indexer.Source, baseFiles, projectFiles, targetFiles []SnapshotFile) (*rebaseSemanticIndexes, error) {
	return &rebaseSemanticIndexes{
		base:    rebaseSemanticLayer{root: base.Path, files: append([]SnapshotFile(nil), baseFiles...)},
		project: rebaseSemanticLayer{root: project.Path, files: append([]SnapshotFile(nil), projectFiles...)},
		target:  rebaseSemanticLayer{root: target.Path, files: append([]SnapshotFile(nil), targetFiles...)},
	}, nil
}

func (indexes *rebaseSemanticIndexes) ensure(ctx context.Context) error {
	if indexes == nil {
		return fmt.Errorf("semantic index is unavailable")
	}
	indexes.once.Do(func() {
		for _, layer := range []*rebaseSemanticLayer{&indexes.base, &indexes.project, &indexes.target} {
			if indexes.err != nil {
				return
			}
			indexes.err = layer.load(ctx)
		}
	})
	return indexes.err
}

// applyRebaseCrossFileSemanticSafety catches a case that file-level hashes
// cannot see: an object relocated into a project-owned file while the target
// still exposes the same ID from its old file. CK3 loads both files, so a
// seemingly harmless `inherit_target + keep_project` plan would create a
// duplicate live definition. V1 deliberately stops for review instead of
// guessing which physical file owns the object.
func applyRebaseCrossFileSemanticSafety(ctx context.Context, artifactRoot, transactionID string, indexes *rebaseSemanticIndexes, transaction *RebaseTransaction, baseFiles, projectFiles, targetFiles map[string]*SnapshotFile) error {
	if err := indexes.ensure(ctx); err != nil {
		return err
	}
	for pathKey, message := range indexes.project.parseErrors {
		projectFile := projectFiles[pathKey]
		if projectFile == nil {
			continue
		}
		conflict := newRebaseConflict(
			"project_parse_error", projectFile.Path, "", message,
			[]string{"manual"}, "manual", baseFiles[pathKey], projectFile, targetFiles[pathKey],
		)
		appendRebaseCrossFileConflict(transaction, conflict, projectFile.Path)
	}
	ids := map[string]bool{}
	for _, layer := range []*rebaseSemanticLayer{&indexes.base, &indexes.project, &indexes.target} {
		for id := range layer.recordsByID {
			ids[id] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		projectRecords := indexes.project.recordsByID[id]
		if len(projectRecords) == 0 {
			continue
		}
		baseRecord, baseUnique := indexes.base.unique(id)
		targetRecord, targetUnique := indexes.target.unique(id)
		for _, projectRecord := range projectRecords {
			// Duplicate project records never have a single semantic owner. The
			// file-level adapter may already have caught them; this covers an
			// added-only file that otherwise would bypass it.
			if len(projectRecords) != 1 {
				conflict := newRebaseConflict(
					"semantic_duplicate_id", projectRecord.Path, projectRecord.ID,
					"project contains multiple records with the same semantic ID",
					[]string{"keep_project", "use_target", "manual"}, "manual",
					lookupRebaseSnapshot(baseFiles, baseRecord.Path, baseUnique), lookupRebaseSnapshot(projectFiles, projectRecord.Path, true), lookupRebaseSnapshot(targetFiles, targetRecord.Path, targetUnique),
				)
				appendRebaseCrossFileConflict(transaction, conflict, projectRecord.Path)
				continue
			}
			if !targetUnique || targetRecord.Path == projectRecord.Path {
				continue
			}
			// An added project record collides with a same-ID target record in
			// another physical file. A base record that was explicitly relocated
			// is the same hazard.
			if !baseUnique || projectRecord.Path != baseRecord.Path {
				conflict, err := planRebaseCrossFileMoveConflict(
					artifactRoot, transactionID, indexes, transaction,
					projectRecord, targetRecord,
					lookupRebaseSnapshot(baseFiles, baseRecord.Path, baseUnique), lookupRebaseSnapshot(projectFiles, projectRecord.Path, true), lookupRebaseSnapshot(targetFiles, targetRecord.Path, true),
				)
				if err != nil {
					return err
				}
				appendRebaseCrossFileConflict(transaction, conflict, projectRecord.Path, targetRecord.Path)
			}
		}
	}
	return nil
}

func lookupRebaseSnapshot(files map[string]*SnapshotFile, rel string, exists bool) *SnapshotFile {
	if !exists {
		return nil
	}
	return files[strings.ToLower(rel)]
}

func appendRebaseCrossFileConflict(transaction *RebaseTransaction, conflict RebaseConflict, paths ...string) {
	transaction.Conflicts = append(transaction.Conflicts, conflict)
	for _, rel := range paths {
		for index := range transaction.Files {
			decision := &transaction.Files[index]
			if !strings.EqualFold(decision.Path, rel) {
				continue
			}
			decision.Action = "conflict"
			decision.Safe = false
			decision.Reason = conflict.Message
			decision.ConflictIDs = append(decision.ConflictIDs, conflict.ID)
		}
	}
}

func (layer *rebaseSemanticLayer) load(ctx context.Context) error {
	layer.recordsByID = map[string][]rebaseSemanticRecord{}
	layer.recordsByPath = map[string][]rebaseSemanticRecord{}
	layer.parseErrors = map[string]string{}
	for _, file := range layer.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		adapter := rebaseAdapterForPath(file.Path)
		if adapter != "jomini_objects" && adapter != "locator_objects" {
			continue
		}
		data, err := readSourceFile(layer.root, file.Path)
		if err != nil {
			return err
		}
		parsed := parseRebaseJomini(file.Path, string(data))
		if len(parsed.Errors) > 0 {
			layer.parseErrors[strings.ToLower(file.Path)] = parsed.Errors[0].Message
			continue
		}
		for _, node := range parsed.Nodes {
			start, end, ok := nodeRuneSpan(string(data), node)
			if !ok {
				layer.parseErrors[strings.ToLower(file.Path)] = "could not locate parsed object span"
				break
			}
			raw := string([]rune(string(data))[start:end])
			record := rebaseSemanticRecord{
				ID: semanticNodeID(file.Path, node), Path: file.Path, Raw: raw,
				Digest: semanticNodeDigest(node), Node: node,
			}
			key := strings.ToLower(record.ID)
			layer.recordsByID[key] = append(layer.recordsByID[key], record)
			pathKey := strings.ToLower(file.Path)
			layer.recordsByPath[pathKey] = append(layer.recordsByPath[pathKey], record)
		}
	}
	return nil
}

func parseRebaseJomini(path, text string) script.File {
	if strings.EqualFold(filepath.Ext(path), ".gui") {
		return script.ParseGUI(text)
	}
	return script.Parse(text)
}

func semanticDomain(path string) string {
	lower := strings.ToLower(filepath.ToSlash(path))
	dir := strings.TrimSuffix(lower, "/"+pathpkgBase(lower))
	switch {
	case strings.HasPrefix(dir, "common/culture/cultures"):
		return "culture"
	case strings.HasPrefix(dir, "history/provinces"):
		return "province_history"
	case strings.HasPrefix(dir, "history/titles"):
		return "title_history"
	case strings.HasPrefix(dir, "common/landed_titles"):
		return "landed_title"
	case strings.HasPrefix(dir, "common/province_terrain"):
		return "province_terrain"
	case strings.HasPrefix(dir, "events/"):
		return "event"
	case strings.HasPrefix(dir, "gui/"):
		return "gui"
	default:
		return dir
	}
}

// pathpkgBase deliberately uses slash semantics after filepath.ToSlash.  The
// migration format is source-root relative on every host, including Windows.
func pathpkgBase(value string) string { return path.Base(value) }

func semanticNodeID(path string, node *script.Node) string {
	name := strings.ToLower(strings.TrimSpace(node.Key))
	if node.Kind == "block" && strings.TrimSpace(node.Value) != "" {
		name += "=" + strings.ToLower(strings.TrimSpace(node.Value))
	}
	if name == "" {
		name = "anonymous@" + fmt.Sprintf("%d", node.Line)
	}
	return semanticDomain(path) + ":" + name
}

func semanticNodeDigest(node *script.Node) string {
	var builder strings.Builder
	var visit func(*script.Node)
	visit = func(current *script.Node) {
		builder.WriteString(current.Key)
		builder.WriteByte('\x00')
		builder.WriteString(current.Operator)
		builder.WriteByte('\x00')
		builder.WriteString(current.Kind)
		builder.WriteByte('\x00')
		builder.WriteString(current.Value)
		builder.WriteByte('\x00')
		for _, child := range current.Children {
			visit(child)
		}
		builder.WriteByte('\x01')
	}
	visit(node)
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func (layer *rebaseSemanticLayer) unique(id string) (rebaseSemanticRecord, bool) {
	records := layer.recordsByID[strings.ToLower(id)]
	if len(records) != 1 {
		return rebaseSemanticRecord{}, false
	}
	return records[0], true
}

func (layer *rebaseSemanticLayer) pathRecords(path string) []rebaseSemanticRecord {
	return append([]rebaseSemanticRecord(nil), layer.recordsByPath[strings.ToLower(path)]...)
}

func buildRebaseCandidate(
	ctx context.Context,
	rel string,
	project, base, target indexer.Source,
	baseFile, projectFile, targetFile *SnapshotFile,
	adapter string,
	semantic *rebaseSemanticIndexes,
) ([]byte, []string, []RebaseConflict, error) {
	projectData, err := readSourceFile(project.Path, rel)
	if err != nil {
		return nil, nil, nil, err
	}
	var baseData, targetData []byte
	if baseFile != nil {
		baseData, err = readSourceFile(base.Path, rel)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if targetFile != nil {
		targetData, err = readSourceFile(target.Path, rel)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	switch adapter {
	case "png_pixels", "tga_pixels":
		candidate, conflicts, err := buildRebaseRasterCandidate(rel, baseData, projectData, targetData, baseFile, projectFile, targetFile)
		return candidate, nil, conflicts, err
	case "localization":
		candidate, ids, conflicts := mergeLocalizationThreeWay(rel, string(baseData), string(projectData), string(targetData), baseFile, projectFile, targetFile)
		return []byte(candidate), ids, conflicts, nil
	case "jomini_objects", "locator_objects":
		if err := semantic.ensure(ctx); err != nil {
			return nil, nil, nil, err
		}
		candidate, ids, conflicts := mergeJominiThreeWay(rel, string(baseData), string(projectData), string(targetData), baseFile, projectFile, targetFile, semantic)
		return []byte(candidate), ids, conflicts, nil
	default:
		return nil, nil, nil, fmt.Errorf("no rebase adapter for %s", adapter)
	}
}

func mergeJominiThreeWay(rel, baseText, projectText, targetText string, baseFile, projectFile, targetFile *SnapshotFile, semantic *rebaseSemanticIndexes) (string, []string, []RebaseConflict) {
	pathKey := strings.ToLower(rel)
	if message := semantic.base.parseErrors[pathKey]; message != "" {
		return targetText, nil, []RebaseConflict{newRebaseConflict("base_parse_error", rel, "", message, []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile)}
	}
	if message := semantic.project.parseErrors[pathKey]; message != "" {
		return targetText, nil, []RebaseConflict{newRebaseConflict("project_parse_error", rel, "", message, []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile)}
	}
	if message := semantic.target.parseErrors[pathKey]; message != "" {
		return targetText, nil, []RebaseConflict{newRebaseConflict("target_parse_error", rel, "", message, []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile)}
	}
	baseRecords := semantic.base.pathRecords(rel)
	projectRecords := semantic.project.pathRecords(rel)
	targetRecords := semantic.target.pathRecords(rel)
	baseByID, projectByID, targetByID := recordsByID(baseRecords), recordsByID(projectRecords), recordsByID(targetRecords)
	var conflicts []RebaseConflict
	var ids []string
	replacements := map[string]string{}
	var additions []string

	for _, projectRecord := range projectRecords {
		ids = append(ids, projectRecord.ID)
		baseRecord, baseOK := semantic.base.unique(projectRecord.ID)
		targetRecord, targetOK := semantic.target.unique(projectRecord.ID)
		if !baseOK {
			if targetOK {
				if projectRecord.Digest == targetRecord.Digest {
					continue
				}
				conflicts = append(conflicts, newRebaseConflict("semantic_add_conflict", rel, projectRecord.ID, "project and target added the same semantic object differently", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile))
				continue
			}
			additions = append(additions, projectRecord.Raw)
			continue
		}
		if projectRecord.Digest == baseRecord.Digest {
			continue
		}
		if !targetOK {
			conflicts = append(conflicts, newRebaseConflict("semantic_delete_modify_conflict", rel, projectRecord.ID, "target deleted or ambiguously moved a semantic object that the project changed", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile))
			continue
		}
		merged := targetRecord.Raw
		switch {
		case targetRecord.Digest == baseRecord.Digest:
			merged = projectRecord.Raw
		case projectRecord.Digest == targetRecord.Digest:
			merged = targetRecord.Raw
		default:
			value, ok := mergeJominiObject(baseRecord.Raw, projectRecord.Raw, targetRecord.Raw, strings.EqualFold(filepath.Ext(rel), ".gui"))
			if !ok {
				conflicts = append(conflicts, newRebaseConflict("semantic_field_conflict", rel, projectRecord.ID, "project and target changed overlapping semantic fields", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile))
				continue
			}
			merged = value
		}
		if strings.EqualFold(targetRecord.Path, rel) {
			replacements[targetRecord.ID] = merged
		} else {
			additions = append(additions, merged)
		}
	}

	// A project same-path override can intentionally remove a base object. This
	// is only safe when the target object is still in the target file and did
	// not independently change.
	for id, baseRecord := range baseByID {
		if _, exists := projectByID[id]; exists {
			continue
		}
		targetRecord, targetOK := semantic.target.unique(baseRecord.ID)
		if !targetOK {
			continue
		}
		if targetRecord.Path != rel {
			conflicts = append(conflicts, newRebaseConflict("semantic_move_delete_conflict", rel, baseRecord.ID, "project deleted an object that target moved to another file", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile))
			continue
		}
		if targetRecord.Digest != baseRecord.Digest {
			conflicts = append(conflicts, newRebaseConflict("semantic_delete_modify_conflict", rel, baseRecord.ID, "project deleted an object that target modified", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile))
			continue
		}
		replacements[targetRecord.ID] = ""
		ids = append(ids, baseRecord.ID)
	}

	_ = targetByID // keeps the target-file object map intentionally explicit in the audit path above.
	result := applyJominiReplacements(targetText, targetRecords, replacements, additions)
	sort.Strings(ids)
	return result, uniqueStrings(ids), dedupeRebaseConflicts(conflicts)
}

func recordsByID(records []rebaseSemanticRecord) map[string]rebaseSemanticRecord {
	out := map[string]rebaseSemanticRecord{}
	ambiguous := map[string]bool{}
	for _, record := range records {
		key := strings.ToLower(record.ID)
		if ambiguous[key] {
			continue
		}
		if _, exists := out[key]; exists {
			delete(out, key)
			ambiguous[key] = true
			continue
		}
		out[key] = record
	}
	return out
}

func applyJominiReplacements(targetText string, targetRecords []rebaseSemanticRecord, replacements map[string]string, additions []string) string {
	type replacement struct {
		start, end int
		value      string
	}
	var changes []replacement
	for _, record := range targetRecords {
		value, exists := replacements[record.ID]
		if !exists || value == record.Raw {
			continue
		}
		start, end, ok := nodeRuneSpan(targetText, record.Node)
		if !ok {
			continue
		}
		changes = append(changes, replacement{start: start, end: end, value: value})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].start > changes[j].start })
	runes := []rune(targetText)
	for _, change := range changes {
		replacementRunes := []rune(change.value)
		runes = append(runes[:change.start], append(replacementRunes, runes[change.end:]...)...)
	}
	result := string(runes)
	if len(additions) == 0 {
		return result
	}
	separator := "\n"
	if strings.Contains(result, "\r\n") {
		separator = "\r\n"
	}
	if result != "" && !strings.HasSuffix(result, "\n") && !strings.HasSuffix(result, "\r") {
		result += separator
	}
	return result + strings.Join(additions, separator) + separator
}

func mergeJominiObject(baseRaw, projectRaw, targetRaw string, gui bool) (string, bool) {
	base := parseRebaseJomini("object"+map[bool]string{true: ".gui", false: ".txt"}[gui], baseRaw)
	project := parseRebaseJomini("object"+map[bool]string{true: ".gui", false: ".txt"}[gui], projectRaw)
	target := parseRebaseJomini("object"+map[bool]string{true: ".gui", false: ".txt"}[gui], targetRaw)
	if len(base.Errors) > 0 || len(project.Errors) > 0 || len(target.Errors) > 0 || len(base.Nodes) != 1 || len(project.Nodes) != 1 || len(target.Nodes) != 1 {
		return "", false
	}
	if semanticNodeDigest(project.Nodes[0]) == semanticNodeDigest(base.Nodes[0]) {
		return targetRaw, true
	}
	if semanticNodeDigest(target.Nodes[0]) == semanticNodeDigest(base.Nodes[0]) || semanticNodeDigest(project.Nodes[0]) == semanticNodeDigest(target.Nodes[0]) {
		return projectRaw, true
	}
	if base.Nodes[0].Kind != "block" || project.Nodes[0].Kind != "block" || target.Nodes[0].Kind != "block" || base.Nodes[0].Key != project.Nodes[0].Key || base.Nodes[0].Key != target.Nodes[0].Key {
		return "", false
	}
	return mergeJominiBlock(baseRaw, base.Nodes[0], projectRaw, project.Nodes[0], targetRaw, target.Nodes[0], gui)
}

type rebaseChild struct {
	ID     string
	Raw    string
	Node   *script.Node
	Digest string
}

func childMap(text string, node *script.Node) (map[string]rebaseChild, bool) {
	out := map[string]rebaseChild{}
	for _, child := range node.Children {
		id := childIdentity(child)
		if _, exists := out[id]; exists {
			return nil, false
		}
		start, end, ok := nodeRuneSpan(text, child)
		if !ok {
			return nil, false
		}
		out[id] = rebaseChild{ID: id, Raw: string([]rune(text)[start:end]), Node: child, Digest: semanticNodeDigest(child)}
	}
	return out, true
}

func childIdentity(node *script.Node) string {
	if node.Kind == "bare" {
		return "bare:" + strings.ToLower(node.Key)
	}
	if node.Key == "" {
		return "anonymous:" + semanticNodeDigest(node)
	}
	return "key:" + strings.ToLower(node.Key)
}

func mergeJominiBlock(baseText string, baseNode *script.Node, projectText string, projectNode *script.Node, targetText string, targetNode *script.Node, gui bool) (string, bool) {
	base, ok := childMap(baseText, baseNode)
	if !ok {
		return "", false
	}
	project, ok := childMap(projectText, projectNode)
	if !ok {
		return "", false
	}
	target, ok := childMap(targetText, targetNode)
	if !ok {
		return "", false
	}
	keys := map[string]bool{}
	for key := range base {
		keys[key] = true
	}
	for key := range project {
		keys[key] = true
	}
	for key := range target {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	replacements := map[string]string{}
	var additions []string
	for _, key := range ordered {
		baseChild, baseOK := base[key]
		projectChild, projectOK := project[key]
		targetChild, targetOK := target[key]
		value, exists, merged := mergeJominiChild(baseChild, baseOK, projectChild, projectOK, targetChild, targetOK, gui)
		if !merged {
			return "", false
		}
		if targetOK {
			if !exists {
				replacements[key] = ""
			} else if value != targetChild.Raw {
				replacements[key] = value
			}
		} else if exists {
			additions = append(additions, value)
		}
	}

	type replacement struct {
		start, end int
		value      string
	}
	var changes []replacement
	for _, child := range target {
		value, exists := replacements[child.ID]
		if !exists {
			continue
		}
		start, end, ok := nodeRuneSpan(targetText, child.Node)
		if !ok {
			return "", false
		}
		changes = append(changes, replacement{start: start, end: end, value: value})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].start > changes[j].start })
	runes := []rune(targetText)
	for _, change := range changes {
		runes = append(runes[:change.start], append([]rune(change.value), runes[change.end:]...)...)
	}
	if len(additions) == 0 {
		return string(runes), true
	}
	insert := len(runes)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] == '}' {
			insert = index
			break
		}
	}
	if insert == len(runes) {
		return "", false
	}
	newline := "\n"
	if strings.Contains(targetText, "\r\n") {
		newline = "\r\n"
	}
	indent := "\t"
	addition := newline + indent + strings.Join(additions, newline+indent) + newline
	runes = append(runes[:insert], append([]rune(addition), runes[insert:]...)...)
	return string(runes), true
}

func mergeJominiChild(base rebaseChild, baseOK bool, project rebaseChild, projectOK bool, target rebaseChild, targetOK bool, gui bool) (string, bool, bool) {
	if !baseOK {
		switch {
		case !projectOK:
			return target.Raw, targetOK, true
		case !targetOK:
			return project.Raw, true, true
		case project.Digest == target.Digest:
			return target.Raw, true, true
		default:
			return "", false, false
		}
	}
	if !projectOK {
		if !targetOK || target.Digest == base.Digest {
			return "", false, true
		}
		return "", false, false
	}
	if !targetOK {
		if project.Digest == base.Digest {
			return "", false, true
		}
		return "", false, false
	}
	switch {
	case project.Digest == base.Digest:
		return target.Raw, true, true
	case target.Digest == base.Digest:
		return project.Raw, true, true
	case project.Digest == target.Digest:
		return target.Raw, true, true
	}
	ext := ".txt"
	if gui {
		ext = ".gui"
	}
	baseParsed := parseRebaseJomini("child"+ext, base.Raw)
	projectParsed := parseRebaseJomini("child"+ext, project.Raw)
	targetParsed := parseRebaseJomini("child"+ext, target.Raw)
	if len(baseParsed.Nodes) != 1 || len(projectParsed.Nodes) != 1 || len(targetParsed.Nodes) != 1 || len(baseParsed.Errors) > 0 || len(projectParsed.Errors) > 0 || len(targetParsed.Errors) > 0 {
		return "", false, false
	}
	if baseParsed.Nodes[0].Kind != "block" || projectParsed.Nodes[0].Kind != "block" || targetParsed.Nodes[0].Kind != "block" {
		return "", false, false
	}
	merged, ok := mergeJominiBlock(base.Raw, baseParsed.Nodes[0], project.Raw, projectParsed.Nodes[0], target.Raw, targetParsed.Nodes[0], gui)
	return merged, ok, ok
}

func mergeLocalizationThreeWay(rel, baseText, projectText, targetText string, baseFile, projectFile, targetFile *SnapshotFile) (string, []string, []RebaseConflict) {
	base, baseErr := parseRebaseLocalizationDocument(baseText)
	project, projectErr := parseRebaseLocalizationDocument(projectText)
	target, targetErr := parseRebaseLocalizationDocument(targetText)
	if baseErr != nil || projectErr != nil || targetErr != nil {
		var reasons []string
		if baseErr != nil {
			reasons = append(reasons, "base: "+baseErr.Error())
		}
		if projectErr != nil {
			reasons = append(reasons, "project: "+projectErr.Error())
		}
		if targetErr != nil {
			reasons = append(reasons, "target: "+targetErr.Error())
		}
		conflict := newRebaseConflict("localization_parse_conflict", rel, "", "localization cannot be safely parsed ("+strings.Join(reasons, "; ")+")", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile)
		return targetText, nil, []RebaseConflict{conflict}
	}
	keys := map[string]bool{}
	for key := range base.Records {
		keys[key] = true
	}
	for key := range project.Records {
		keys[key] = true
	}
	for key := range target.Records {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var conflicts []RebaseConflict
	var ids []string
	type localizationResult struct {
		Value  string
		Exists bool
	}
	results := make(map[string]localizationResult, len(ordered))
	for _, key := range ordered {
		baseRecord, baseExists := base.Records[key]
		projectRecord, projectExists := project.Records[key]
		targetRecord, targetExists := target.Records[key]
		baseValue := ""
		if baseExists {
			baseValue = baseRecord.Value
		}
		projectValue := ""
		if projectExists {
			projectValue = projectRecord.Value
		}
		targetValue := ""
		if targetExists {
			targetValue = targetRecord.Value
		}
		value, exists, ok := mergePlainThreeWay(baseValue, baseExists, projectValue, projectExists, targetValue, targetExists)
		if !ok {
			conflicts = append(conflicts, newRebaseConflict("localization_key_conflict", rel, "localization:"+key, "project and target changed localization key differently", []string{"keep_project", "use_target", "manual"}, "manual", baseFile, projectFile, targetFile))
			continue
		}
		results[key] = localizationResult{Value: value, Exists: exists}
		if projectExists || baseExists {
			ids = append(ids, "localization:"+key)
		}
	}
	if len(conflicts) > 0 {
		return targetText, uniqueStrings(ids), dedupeRebaseConflicts(conflicts)
	}

	// Start from the target byte-for-byte. A localization file often carries
	// hand-maintained comments, blank separators, non-alphabetic ordering and
	// language-specific spacing. Re-rendering it from a map would discard all
	// of that evidence. Automatic changes therefore replace only the quoted
	// value span of an existing target key, delete an exact key line, or append
	// a newly project-owned key after the untouched target text.
	type localizationMutation struct {
		Start       int
		End         int
		Replacement string
	}
	mutations := make([]localizationMutation, 0, len(ordered))
	additions := make([]rebaseLocalizationRecord, 0)
	for _, key := range ordered {
		result := results[key]
		targetRecord, targetExists := target.Records[key]
		switch {
		case !result.Exists && targetExists:
			mutations = append(mutations, localizationMutation{Start: targetRecord.LineStart, End: targetRecord.LineEnd})
		case result.Exists && targetExists && targetRecord.Value != result.Value:
			mutations = append(mutations, localizationMutation{Start: targetRecord.ValueStart, End: targetRecord.ValueEnd, Replacement: result.Value})
		case result.Exists && !targetExists:
			projectRecord, projectExists := project.Records[key]
			if !projectExists {
				conflict := newRebaseConflict("localization_materialization_conflict", rel, "localization:"+key, "merged localization key has no project record to append", []string{"manual"}, "manual", baseFile, projectFile, targetFile)
				return targetText, uniqueStrings(ids), []RebaseConflict{conflict}
			}
			additions = append(additions, projectRecord)
		}
	}
	sort.Slice(mutations, func(i, j int) bool {
		return mutations[i].Start > mutations[j].Start
	})
	merged := target.Raw
	for _, mutation := range mutations {
		merged = merged[:mutation.Start] + mutation.Replacement + merged[mutation.End:]
	}
	sort.Slice(additions, func(i, j int) bool {
		if additions[i].Order == additions[j].Order {
			return additions[i].Key < additions[j].Key
		}
		return additions[i].Order < additions[j].Order
	})
	for _, addition := range additions {
		merged = appendRebaseLocalizationRecord(merged, addition.LineBody, target.LineEnding)
	}
	return merged, uniqueStrings(ids), nil
}

type rebaseLocalizationDocument struct {
	Raw        string
	Header     string
	Records    map[string]rebaseLocalizationRecord
	Order      []string
	LineEnding string
}

type rebaseLocalizationRecord struct {
	Key string
	// Value is the quoted localization literal, including its double quotes.
	// Version syntax and an inline comment deliberately remain outside this
	// span, so a project wording update cannot rewrite target-side formatting.
	Value      string
	ValueStart int
	ValueEnd   int
	LineStart  int
	LineEnd    int
	LineBody   string
	Order      int
}

// parseRebaseLocalizationDocument accepts the conservative, line-oriented
// portion of CK3 localization syntax that V1 can safely change in place. It
// intentionally refuses any line whose key/value boundary cannot be located
// exactly: interpreting an exotic localization construct and then rendering a
// replacement would be less safe than asking for a manual decision.
func parseRebaseLocalizationDocument(text string) (*rebaseLocalizationDocument, error) {
	document := &rebaseLocalizationDocument{
		Raw:        text,
		Records:    map[string]rebaseLocalizationRecord{},
		LineEnding: "\n",
	}
	headerCount := 0
	for start := 0; start < len(text); {
		end := start + strings.IndexByte(text[start:], '\n') + 1
		if end == start { // No trailing newline on the final record.
			end = len(text)
		}
		line := text[start:end]
		lineBody, ending := splitRebaseLocalizationLineEnding(line)
		if ending != "" {
			document.LineEnding = ending
		}
		parseBody := lineBody
		parseOffset := 0
		if start == 0 && strings.HasPrefix(parseBody, "\ufeff") {
			parseOffset = len("\ufeff")
			parseBody = parseBody[parseOffset:]
		}
		trimmed, trimStart := trimRebaseLocalizationHorizontalSpace(parseBody)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			start = end
			continue
		}
		if isRebaseLocalizationHeader(trimmed) {
			if len(document.Order) > 0 {
				return nil, fmt.Errorf("localization header appears after key records")
			}
			headerCount++
			if headerCount > 1 {
				return nil, fmt.Errorf("multiple localization headers")
			}
			document.Header = trimmed
			start = end
			continue
		}
		if headerCount == 0 {
			return nil, fmt.Errorf("localization header must precede key records")
		}
		record, err := parseRebaseLocalizationRecord(lineBody, parseOffset+trimStart, start, end, len(document.Order))
		if err != nil {
			return nil, fmt.Errorf("unsupported localization line %d: %w", len(document.Order)+headerCount+1, err)
		}
		if _, exists := document.Records[record.Key]; exists {
			return nil, fmt.Errorf("duplicate localization key %q", record.Key)
		}
		document.Records[record.Key] = record
		document.Order = append(document.Order, record.Key)
		start = end
	}
	if headerCount != 1 {
		return nil, fmt.Errorf("expected exactly one localization header")
	}
	return document, nil
}

func splitRebaseLocalizationLineEnding(line string) (string, string) {
	if strings.HasSuffix(line, "\r\n") {
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	return line, ""
}

func trimRebaseLocalizationHorizontalSpace(value string) (string, int) {
	start := 0
	for start < len(value) && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	end := len(value)
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end], start
}

func isRebaseLocalizationHeader(value string) bool {
	if len(value) < len("l_a:") || !strings.HasPrefix(strings.ToLower(value), "l_") || !strings.HasSuffix(value, ":") {
		return false
	}
	for _, character := range value[2 : len(value)-1] {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func parseRebaseLocalizationRecord(lineBody string, trimStart, lineStart, lineEnd, order int) (rebaseLocalizationRecord, error) {
	trimmed, _ := trimRebaseLocalizationHorizontalSpace(lineBody[trimStart:])
	colon := strings.IndexByte(trimmed, ':')
	if colon <= 0 {
		return rebaseLocalizationRecord{}, fmt.Errorf("missing key/value separator")
	}
	key := trimmed[:colon]
	if !isRebaseLocalizationKey(key) {
		return rebaseLocalizationRecord{}, fmt.Errorf("invalid key")
	}
	valueOffset := trimStart + colon + 1
	for valueOffset < len(lineBody) && (lineBody[valueOffset] == ' ' || lineBody[valueOffset] == '\t') {
		valueOffset++
	}
	for valueOffset < len(lineBody) && lineBody[valueOffset] >= '0' && lineBody[valueOffset] <= '9' {
		valueOffset++
	}
	for valueOffset < len(lineBody) && (lineBody[valueOffset] == ' ' || lineBody[valueOffset] == '\t') {
		valueOffset++
	}
	if valueOffset >= len(lineBody) || lineBody[valueOffset] != '"' {
		return rebaseLocalizationRecord{}, fmt.Errorf("value is not a quoted literal")
	}
	valueEnd, ok := findRebaseLocalizationQuotedLiteralEnd(lineBody, valueOffset)
	if !ok {
		return rebaseLocalizationRecord{}, fmt.Errorf("unterminated quoted literal")
	}
	tail := lineBody[valueEnd:]
	tailTrimmed, _ := trimRebaseLocalizationHorizontalSpace(tail)
	if tailTrimmed != "" && !strings.HasPrefix(tailTrimmed, "#") {
		return rebaseLocalizationRecord{}, fmt.Errorf("unsupported content after quoted literal")
	}
	return rebaseLocalizationRecord{
		Key:        key,
		Value:      lineBody[valueOffset:valueEnd],
		ValueStart: lineStart + valueOffset,
		ValueEnd:   lineStart + valueEnd,
		LineStart:  lineStart,
		LineEnd:    lineEnd,
		LineBody:   lineBody,
		Order:      order,
	}, nil
}

func isRebaseLocalizationKey(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character == ':' || character == '"' || character == '#' || character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			return false
		}
	}
	return true
}

func findRebaseLocalizationQuotedLiteralEnd(value string, start int) (int, bool) {
	for index := start + 1; index < len(value); index++ {
		switch value[index] {
		case '\\':
			index++
		case '"':
			return index + 1, true
		}
	}
	return 0, false
}

func appendRebaseLocalizationRecord(target, lineBody, lineEnding string) string {
	if lineEnding == "" {
		lineEnding = "\n"
	}
	if target == "" {
		return lineBody
	}
	if strings.HasSuffix(target, "\n") {
		return target + lineBody + lineEnding
	}
	return target + lineEnding + lineBody
}

func mergePlainThreeWay(base string, baseExists bool, project string, projectExists bool, target string, targetExists bool) (string, bool, bool) {
	if !baseExists {
		switch {
		case !projectExists:
			return target, targetExists, true
		case !targetExists:
			return project, true, true
		case project == target:
			return target, true, true
		default:
			return "", false, false
		}
	}
	if !projectExists {
		if !targetExists || target == base {
			return "", false, true
		}
		return "", false, false
	}
	if !targetExists {
		if project == base {
			return "", false, true
		}
		return "", false, false
	}
	switch {
	case project == base:
		return target, true, true
	case target == base:
		return project, true, true
	case project == target:
		return target, true, true
	default:
		return "", false, false
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
