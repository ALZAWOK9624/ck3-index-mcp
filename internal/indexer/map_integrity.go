package indexer

import (
	"context"
	"image"
	"image/color"
	"sort"
)

func (db *DB) mapIntegrityIssues(ctx context.Context, titleID string, provinces map[int]bool) ([]MapIntegrityIssue, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT code,title_id,province_id,message,source_name,path,line
		FROM map_integrity_issues ORDER BY code,province_id,title_id,path,line`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapIntegrityIssue
	seen := map[string]bool{}
	for rows.Next() {
		var issue MapIntegrityIssue
		if err := rows.Scan(&issue.Code, &issue.TitleID, &issue.ProvinceID, &issue.Message, &issue.Source, &issue.Path, &issue.Line); err != nil {
			return nil, err
		}
		if titleID != "" && issue.TitleID != titleID && !provinces[issue.ProvinceID] {
			continue
		}
		if titleID == "" && len(provinces) > 0 && !provinces[issue.ProvinceID] {
			continue
		}
		key := issue.Code + "\x00" + issue.Message + "\x00" + issue.TitleID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	return out, rows.Err()
}

func integrityProvinceSet(issues []MapIntegrityIssue) map[int]bool {
	out := map[int]bool{}
	for _, issue := range issues {
		if issue.ProvinceID > 0 {
			out[issue.ProvinceID] = true
		}
	}
	return out
}

func integrityMessages(issues []MapIntegrityIssue) []string {
	seen := map[string]bool{}
	var out []string
	for _, issue := range issues {
		if seen[issue.Message] {
			continue
		}
		seen[issue.Message] = true
		out = append(out, issue.Message)
	}
	sort.Strings(out)
	return out
}

func (db *DB) renderIntegrityOverlay(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA, v renderViewport, issues []MapIntegrityIssue) (int, error) {
	count := 0
	for _, pid := range sortedProvinceIDs(integrityProvinceSet(issues)) {
		runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
		if err != nil {
			return count, err
		}
		if len(runs) == 0 {
			continue
		}
		// Conflicts are hatched in red ink rather than filled, so the disputed
		// area and its rightful tint both stay readable.
		drawInkHatch(canvas, v, runs, color.RGBA{R: 168, G: 50, B: 74, A: 205}, 7)
		count++
	}
	return count, nil
}
