package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

// integrityQueryExecer is deliberately satisfied by both *sql.DB and *sql.Tx.
// Title integrity must be identical for full scans, incremental scans and the
// explicit validate command.
type integrityQueryExecer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type titleOccurrence struct {
	FileID       int64
	NodeID       int64
	ParentNodeID int64
	Name         string
	Source       string
	Rank         int
	Path         string
	Line         int
	EndLine      int
	Column       int
	ProvinceID   int
}

func collectActiveTitleOccurrences(ctx context.Context, q integrityQueryExecer) ([]titleOccurrence, error) {
	rows, err := q.QueryContext(ctx, `SELECT f.id,f.source_name,f.source_rank,f.path,f.rel_path
		FROM files f WHERE f.overridden=0 AND lower(f.rel_path) LIKE 'common/landed_titles/%'
		ORDER BY f.source_rank,f.rel_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []titleOccurrence
	for rows.Next() {
		var fileID int64
		var source, fullPath, relPath string
		var rank int
		if err := rows.Scan(&fileID, &source, &rank, &fullPath, &relPath); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, err
		}
		parsed := script.Parse(string(data))
		var visit func(*script.Node, int64)
		visit = func(node *script.Node, parent int64) {
			if node.Kind != "block" || !isTitleID(node.Key) {
				return
			}
			item := titleOccurrence{FileID: fileID, NodeID: node.ID, ParentNodeID: parent, Name: node.Key, Source: source, Rank: rank,
				Path: relPath, Line: node.Line, EndLine: node.EndLine, Column: node.Col}
			for _, child := range node.Children {
				if child.Key == "province" {
					item.ProvinceID, _ = strconv.Atoi(strings.TrimSpace(child.Value))
				}
			}
			out = append(out, item)
			for _, child := range node.Children {
				if child.Kind == "block" && isTitleID(child.Key) {
					visit(child, node.ID)
				}
			}
		}
		for _, node := range parsed.Nodes {
			visit(node, 0)
		}
	}
	return out, rows.Err()
}

func titleLocation(item titleOccurrence) string {
	return fmt.Sprintf("%s:%d", filepathSlash(item.Path), item.Line)
}

func titleDiagnostic(code, message string, item titleOccurrence, occurrences int) Diagnostic {
	return Diagnostic{
		Source: "integrity", Severity: "warning", Code: code, Message: message,
		Path: item.Path, Line: item.Line, Column: item.Column, SourceLayer: item.Source,
		Confidence: "high", Occurrences: occurrences,
		Fingerprint: fmt.Sprintf("%s:%s:%d", code, filepathSlash(item.Path), item.Line),
	}
}

func collectTitleIntegrityDiagnostics(ctx context.Context, q integrityQueryExecer) ([]Diagnostic, map[string]int64, error) {
	items, err := collectActiveTitleOccurrences(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	fileIDs := map[string]int64{}
	for _, item := range items {
		fileIDs[item.Path] = item.FileID
	}
	var diagnostics []Diagnostic

	bySourceAndName := map[string][]titleOccurrence{}
	for _, item := range items {
		key := item.Source + "\x00" + item.Name
		bySourceAndName[key] = append(bySourceAndName[key], item)
	}
	for _, group := range bySourceAndName {
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Path != group[j].Path {
				return group[i].Path < group[j].Path
			}
			return group[i].Line < group[j].Line
		})
		locations := make([]string, 0, len(group))
		for _, item := range group {
			locations = append(locations, titleLocation(item))
		}
		message := fmt.Sprintf("title %q has %d active definitions in source %q: %s", group[0].Name, len(group), group[0].Source, strings.Join(locations, ", "))
		for _, item := range group {
			diagnostics = append(diagnostics, titleDiagnostic("duplicate_title_id", message, item, len(group)))
		}
	}

	bestRank := map[string]int{}
	for _, item := range items {
		if rank, ok := bestRank[item.Name]; !ok || item.Rank < rank {
			bestRank[item.Name] = item.Rank
		}
	}
	effective := make([]titleOccurrence, 0, len(items))
	for _, item := range items {
		if item.Rank == bestRank[item.Name] {
			effective = append(effective, item)
		}
	}
	byProvince := map[int][]titleOccurrence{}
	for _, item := range effective {
		if strings.HasPrefix(item.Name, "b_") && item.ProvinceID > 0 {
			byProvince[item.ProvinceID] = append(byProvince[item.ProvinceID], item)
		}
	}
	for provinceID, group := range byProvince {
		names := map[string]bool{}
		for _, item := range group {
			names[item.Name] = true
		}
		if len(names) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Path != group[j].Path {
				return group[i].Path < group[j].Path
			}
			return group[i].Line < group[j].Line
		})
		locations := make([]string, 0, len(group))
		for _, item := range group {
			locations = append(locations, fmt.Sprintf("%s at %s", item.Name, titleLocation(item)))
		}
		message := fmt.Sprintf("province %d is assigned to multiple active baronies: %s", provinceID, strings.Join(locations, ", "))
		for _, item := range group {
			diagnostics = append(diagnostics, titleDiagnostic("duplicate_barony_province", message, item, len(group)))
		}
	}

	byNode := map[[2]int64]titleOccurrence{}
	for _, item := range items {
		byNode[[2]int64{item.FileID, item.NodeID}] = item
	}
	expectedParent := map[byte]byte{'b': 'c', 'c': 'd', 'd': 'k', 'k': 'e'}
	for _, item := range effective {
		if item.Name[0] == 'b' && item.ProvinceID <= 0 {
			message := fmt.Sprintf("barony title %q has no valid province assignment", item.Name)
			diagnostics = append(diagnostics, titleDiagnostic("invalid_title_hierarchy", message, item, 1))
		}
		if item.ParentNodeID == 0 {
			continue
		}
		parent, ok := byNode[[2]int64{item.FileID, item.ParentNodeID}]
		if !ok || len(parent.Name) == 0 {
			continue
		}
		want, constrained := expectedParent[item.Name[0]]
		if !constrained {
			continue
		}
		valid := parent.Name[0] == want
		if item.Name[0] == 'k' && parent.Name[0] == 'h' {
			valid = true
		}
		if !valid {
			message := fmt.Sprintf("title %q is nested under %q; expected parent rank %c_", item.Name, parent.Name, want)
			diagnostics = append(diagnostics, titleDiagnostic("invalid_title_hierarchy", message, item, 1))
		}
	}
	return diagnostics, fileIDs, nil
}

func refreshTitleIntegrityDiagnostics(ctx context.Context, q integrityQueryExecer) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='integrity' OR (source='compiler' AND code='duplicate_object')`); err != nil {
		return err
	}
	diagnostics, fileIDs, err := collectTitleIntegrityDiagnostics(ctx, q)
	if err != nil {
		return err
	}
	for _, d := range diagnostics {
		if _, err := q.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col,source_layer,confidence,fingerprint,occurrences)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, d.Source, d.Severity, d.Code, d.Message, fileIDs[d.Path], d.Path, d.Line, d.Column,
			d.SourceLayer, d.Confidence, d.Fingerprint, d.Occurrences); err != nil {
			return err
		}
	}
	return nil
}

func titleAmbiguityDiagnostics(query ObjectQuery) []Diagnostic {
	ambiguous := false
	for _, resolution := range query.Resolution {
		if resolution.Type == "title" && resolution.Status == "ambiguous" {
			ambiguous = true
			break
		}
	}
	if !ambiguous {
		return nil
	}
	var candidates []ObjectDef
	for _, definition := range query.Definitions {
		if definition.Type == "title" && definition.Status == "ambiguous_top_priority" {
			candidates = append(candidates, definition)
		}
	}
	if len(candidates) < 2 {
		return nil
	}
	locations := make([]string, 0, len(candidates))
	for _, definition := range candidates {
		locations = append(locations, fmt.Sprintf("%s:%d", definition.LogicalPath, definition.Line))
	}
	message := fmt.Sprintf("title %q has %d ambiguous top-priority definitions: %s", candidates[0].Name, len(candidates), strings.Join(locations, ", "))
	out := make([]Diagnostic, 0, len(candidates))
	for _, definition := range candidates {
		out = append(out, Diagnostic{Source: "integrity-live", Severity: "warning", Code: "duplicate_title_id", Message: message,
			Path: definition.LogicalPath, Line: definition.Line, Column: definition.Column, SourceLayer: definition.Source,
			Confidence: "high", Occurrences: len(candidates), Fingerprint: fmt.Sprintf("duplicate_title_id:%s:%d", definition.LogicalPath, definition.Line)})
	}
	return out
}
