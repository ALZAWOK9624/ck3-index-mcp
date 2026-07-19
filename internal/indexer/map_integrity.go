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

func (db *DB) renderIntegrityOverlay(ctx context.Context, canvas *image.RGBA, v renderViewport, issues []MapIntegrityIssue) (int, error) {
	count := 0
	for pid := range integrityProvinceSet(issues) {
		runs, err := db.mapProvinceRuns(ctx, pid, false)
		if err != nil {
			return count, err
		}
		if len(runs) == 0 {
			continue
		}
		drawRunPattern(canvas, v, runs, color.RGBA{R: 255, B: 255, A: 220}, 14, 4)
		count++
	}
	return count, nil
}
