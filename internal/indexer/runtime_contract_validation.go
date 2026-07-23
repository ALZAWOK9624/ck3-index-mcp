package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

// refreshGovernmentRegistrationDiagnostics validates the one global contract
// that cannot be decided from a government file alone: a custom government id
// must also be present in NGovernment.GOVERNMENT_TYPES. CK3 uses that list when
// it materializes government-dependent modifier formats.
//
// The check deliberately does not require a fixed set of modifier-format
// suffixes. The 1.19 _definitions.info and vanilla files do not expose a
// complete, uniform suffix set, so such a rule would reject valid upstream
// configurations. Unknown format names are handled by the local modifier
// definition contract instead.
func refreshGovernmentRegistrationDiagnostics(ctx context.Context, tx *sql.Tx, projectRank int) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code='unregistered_government_type'`); err != nil {
		return err
	}
	governments, err := activeProjectGovernments(ctx, tx, projectRank)
	if err != nil {
		return err
	}
	if len(governments) == 0 {
		return nil
	}
	registered, err := activeGovernmentTypes(ctx, tx)
	if err != nil {
		return err
	}
	for name, location := range governments {
		if registered[name] {
			continue
		}
		insertDiag(ctx, tx, "validator", "error", "unregistered_government_type",
			fmt.Sprintf("government %q is not registered in NGovernment.GOVERNMENT_TYPES; CK3 cannot materialize its government-dependent modifier formats", name),
			location.fileID, location.path, location.line, location.col)
	}
	return nil
}

// refreshGovernmentFallbackDiagnostics validates the global fallback contract
// from common/governments/_governments.info. A government with fallback = 0
// is an ordinary non-fallback type; at least one active government must have a
// positive priority. This cannot be decided from a single virtual patch, so it
// is refreshed from the active object/field tables after indexing.
func refreshGovernmentFallbackDiagnostics(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code='government_missing_fallback'`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name,o.file_id,o.path,o.line,o.col,
		COALESCE(of.field,''),COALESCE(of.value_shape,''),COALESCE(of.raw,'')
		FROM objects o JOIN files f ON f.id=o.file_id
		LEFT JOIN object_fields of ON of.object_type=o.object_type
			AND of.object_name=o.name AND of.file_id=o.file_id AND of.field='fallback'
		WHERE o.object_type='government' AND f.overridden=0
		ORDER BY f.source_rank DESC,f.path,o.line,o.col`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var first *runtimeContractLocation
	hasFallback := false
	for rows.Next() {
		var name, path, field, shape, raw string
		var location runtimeContractLocation
		if err := rows.Scan(&name, &location.fileID, &path, &location.line, &location.col, &field, &shape, &raw); err != nil {
			return err
		}
		location.path = path
		if first == nil {
			copy := location
			first = &copy
		}
		if field == "fallback" && governmentFallbackValueIsPositive(shape, raw) {
			hasFallback = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if first == nil || hasFallback {
		return nil
	}
	insertDiag(ctx, tx, "validator", "error", "government_missing_fallback",
		"no active government defines a positive fallback priority; CK3 requires at least one fallback government", first.fileID, first.path, first.line, first.col)
	return nil
}

// refreshGovernmentMechanicDefaultDiagnostics validates the per-mechanic-type
// default rule documented in _governments.info. Only literal mechanic_type and
// literal yes/no default values are judged; a scripted value leaves the group
// to the engine/runtime because static analysis cannot know its result.
func refreshGovernmentMechanicDefaultDiagnostics(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code IN ('government_duplicate_mechanic_default','government_missing_mechanic_default')`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name,o.file_id,o.path,o.line,o.col,
		COALESCE(mt.value_shape,''),COALESCE(mt.raw,''),
		COALESCE(df.value_shape,''),COALESCE(df.raw,'')
		FROM objects o JOIN files f ON f.id=o.file_id
		LEFT JOIN object_fields mt ON mt.object_type=o.object_type
			AND mt.object_name=o.name AND mt.file_id=o.file_id AND mt.field='mechanic_type'
		LEFT JOIN object_fields df ON df.object_type=o.object_type
			AND df.object_name=o.name AND df.file_id=o.file_id AND df.field='is_mechanic_type_default'
		WHERE o.object_type='government' AND f.overridden=0
		ORDER BY f.source_rank DESC,f.path,o.line,o.col`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type mechanicDefaultGroup struct {
		first          runtimeContractLocation
		defaults       []runtimeContractLocation
		dynamicDefault bool
	}
	groups := map[string]*mechanicDefaultGroup{}
	for rows.Next() {
		var name, path, mechanicShape, mechanicRaw, defaultShape, defaultRaw string
		var location runtimeContractLocation
		if err := rows.Scan(&name, &location.fileID, &path, &location.line, &location.col, &mechanicShape, &mechanicRaw, &defaultShape, &defaultRaw); err != nil {
			return err
		}
		location.path = path
		if strings.TrimSpace(mechanicShape) == "" && strings.TrimSpace(mechanicRaw) == "" {
			continue
		}
		mechanicType, knownMechanic := governmentFieldValue(mechanicShape, mechanicRaw)
		if !knownMechanic || mechanicType == "" {
			continue
		}
		group := groups[mechanicType]
		if group == nil {
			group = &mechanicDefaultGroup{first: location}
			groups[mechanicType] = group
		}
		defaultValue, knownDefault := governmentFieldValue(defaultShape, defaultRaw)
		if !knownDefault {
			group.dynamicDefault = true
			continue
		}
		if strings.EqualFold(defaultValue, "yes") {
			group.defaults = append(group.defaults, location)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for mechanicType, group := range groups {
		if group.dynamicDefault {
			continue
		}
		if len(group.defaults) == 0 {
			insertDiag(ctx, tx, "validator", "error", "government_missing_mechanic_default",
				fmt.Sprintf("mechanic_type %q has no government with is_mechanic_type_default = yes", mechanicType), group.first.fileID, group.first.path, group.first.line, group.first.col)
			continue
		}
		if len(group.defaults) <= 1 {
			continue
		}
		for _, location := range group.defaults {
			insertDiag(ctx, tx, "validator", "error", "government_duplicate_mechanic_default",
				fmt.Sprintf("mechanic_type %q has more than one government with is_mechanic_type_default = yes", mechanicType), location.fileID, location.path, location.line, location.col)
		}
	}
	return nil
}

// refreshCourtTypeDefaultDiagnostics validates the cross-file court-type
// singleton. The _court_types.info contract permits at most one default court
// type across the active database. A nonliteral default is left alone because
// static analysis cannot prove whether it evaluates to yes at load time.
func refreshCourtTypeDefaultDiagnostics(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code='court_type_duplicate_default'`); err != nil {
		return err
	}
	files, err := activeRuntimeContractFiles(ctx, tx, "common/court_types/", 0)
	if err != nil {
		return err
	}
	var defaults []runtimeContractLocation
	dynamicDefault := false
	for _, file := range files {
		nodes, err := parseRuntimeContractFile(file)
		if err != nil {
			return err
		}
		for _, courtType := range nodes {
			if courtType.Kind != "block" || courtType.Key == "" {
				continue
			}
			for _, child := range courtType.Children {
				if child.Key != "default" {
					continue
				}
				if child.Kind == "atom" && strings.EqualFold(atomValue(child), "yes") {
					defaults = append(defaults, runtimeContractLocation{
						fileID: file.id,
						path:   file.path,
						line:   child.Line,
						col:    child.Col,
					})
				} else if child.Kind != "atom" || !strings.EqualFold(atomValue(child), "no") {
					dynamicDefault = true
				}
			}
		}
	}
	if dynamicDefault || len(defaults) <= 1 {
		return nil
	}
	for _, location := range defaults {
		insertDiag(ctx, tx, "validator", "error", "court_type_duplicate_default",
			"more than one active court type defines default = yes; CK3 permits only one default court type", location.fileID, location.path, location.line, location.col)
	}
	return nil
}

func governmentFieldValue(shape, raw string) (string, bool) {
	if strings.TrimSpace(shape) == "" && strings.TrimSpace(raw) == "" {
		// is_mechanic_type_default defaults to no when the field is absent. The
		// caller also treats an empty mechanic_type as an ungrouped government.
		return "no", true
	}
	if shape != "atom" && shape != "bool" && shape != "number" {
		return "", false
	}
	value := strings.TrimSpace(raw)
	if index := strings.LastIndex(value, "="); index >= 0 {
		value = strings.TrimSpace(value[index+1:])
	}
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" || strings.HasPrefix(value, "@") || strings.Contains(value, "[") {
		return "", false
	}
	return value, true
}

func governmentFallbackValueIsPositive(shape, raw string) bool {
	if shape != "number" {
		// A define/scripted value is not statically provable, but its presence is
		// enough to avoid claiming that the fallback contract is definitely absent.
		return strings.TrimSpace(raw) != ""
	}
	value := strings.TrimSpace(raw)
	if index := strings.LastIndex(value, "="); index >= 0 {
		value = strings.TrimSpace(value[index+1:])
	}
	number, err := strconv.ParseFloat(value, 64)
	return err != nil || number > 0
}

type runtimeContractLocation struct {
	fileID int64
	path   string
	line   int
	col    int
}

type runtimeContractFile struct {
	id   int64
	path string
}

func activeRuntimeContractFiles(ctx context.Context, tx *sql.Tx, relPrefix string, sourceRank int) ([]runtimeContractFile, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,path FROM files
		WHERE kind='script' AND overridden=0 AND rel_path LIKE ? AND (?=0 OR source_rank=?)`, relPrefix+"%", sourceRank, sourceRank)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []runtimeContractFile
	for rows.Next() {
		var file runtimeContractFile
		if err := rows.Scan(&file.id, &file.path); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func parseRuntimeContractFile(file runtimeContractFile) ([]*script.Node, error) {
	data, err := os.ReadFile(file.path)
	if err != nil {
		return nil, fmt.Errorf("read indexed script %q for runtime-contract validation: %w", file.path, err)
	}
	// Parse errors are already emitted against the source file during the
	// indexing pass. Returning the partial AST here keeps a malformed source
	// from turning a diagnostic refresh into a second, unrelated scan failure.
	return script.Parse(string(data)).Nodes, nil
}

func walkRuntimeContractNodes(nodes []*script.Node, parent *script.Node, visit func(*script.Node, *script.Node)) {
	for _, node := range nodes {
		visit(node, parent)
		walkRuntimeContractNodes(node.Children, node, visit)
	}
}

func activeProjectGovernments(ctx context.Context, tx *sql.Tx, projectRank int) (map[string]runtimeContractLocation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.name,o.file_id,o.path,o.line,o.col
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.object_type='government' AND f.source_rank=? AND f.overridden=0`, projectRank)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	governments := map[string]runtimeContractLocation{}
	for rows.Next() {
		var name string
		var location runtimeContractLocation
		if err := rows.Scan(&name, &location.fileID, &location.path, &location.line, &location.col); err != nil {
			return nil, err
		}
		if name != "" {
			governments[name] = location
		}
	}
	return governments, rows.Err()
}

func activeGovernmentTypes(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	files, err := activeRuntimeContractFiles(ctx, tx, "common/defines/", 0)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, file := range files {
		nodes, err := parseRuntimeContractFile(file)
		if err != nil {
			return nil, err
		}
		walkRuntimeContractNodes(nodes, nil, func(node, parent *script.Node) {
			if parent == nil || parent.Key != "GOVERNMENT_TYPES" || node.Kind != "bare" {
				return
			}
			registered[strings.Trim(node.Key, `"`)] = true
		})
	}
	return registered, nil
}
