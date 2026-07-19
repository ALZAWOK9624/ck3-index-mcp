package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	MapSubjectNotFoundCode  = "MAP_SUBJECT_NOT_FOUND"
	MapSubjectAmbiguousCode = "MAP_SUBJECT_AMBIGUOUS"
)

type MapResolvedSubject struct {
	Input      string `json:"input"`
	ProvinceID int    `json:"province_id"`
	Barony     string `json:"barony,omitempty"`
	County     string `json:"county,omitempty"`
	Duchy      string `json:"duchy,omitempty"`
	Kingdom    string `json:"kingdom,omitempty"`
	Empire     string `json:"empire,omitempty"`
	NameEN     string `json:"name_en,omitempty"`
	NameZH     string `json:"name_zh,omitempty"`
}

type MapSubjectCandidate struct {
	Input      string `json:"input"`
	TitleID    string `json:"title_id,omitempty"`
	ProvinceID int    `json:"province_id"`
	NameEN     string `json:"name_en,omitempty"`
	NameZH     string `json:"name_zh,omitempty"`
}

type MapSubjectResolutionError struct {
	Code       string                `json:"code"`
	Input      string                `json:"input"`
	Message    string                `json:"message"`
	Candidates []MapSubjectCandidate `json:"candidates,omitempty"`
}

func (e *MapSubjectResolutionError) Error() string {
	if len(e.Candidates) > 0 {
		labels := make([]string, 0, len(e.Candidates))
		for _, candidate := range e.Candidates {
			labels = append(labels, fmt.Sprintf("%s:%d", candidate.TitleID, candidate.ProvinceID))
		}
		return fmt.Sprintf("%s: %s; candidates: %s", e.Code, e.Message, strings.Join(labels, ", "))
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (db *DB) ResolveMapSubject(ctx context.Context, input string, year int) (MapResolvedSubject, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return MapResolvedSubject{}, &MapSubjectResolutionError{Code: MapSubjectNotFoundCode, Input: input, Message: "map subject is empty"}
	}
	if err := db.RequireMapDatabase(ctx); err != nil {
		return MapResolvedSubject{}, err
	}

	if provinceID, err := strconv.Atoi(input); err == nil {
		return db.resolvedSubjectForProvince(ctx, input, provinceID, year)
	}
	if isLandedTitleID(input) {
		provinceID, err := db.mapTitleCoreProvince(ctx, input)
		if err != nil {
			return MapResolvedSubject{}, err
		}
		return db.resolvedSubjectForProvince(ctx, input, provinceID, year)
	}

	candidates, err := db.mapSubjectNameCandidates(ctx, input)
	if err != nil {
		return MapResolvedSubject{}, err
	}
	if len(candidates) == 0 {
		return MapResolvedSubject{}, &MapSubjectResolutionError{Code: MapSubjectNotFoundCode, Input: input, Message: fmt.Sprintf("no province or landed title exactly matches %q", input)}
	}
	if len(candidates) > 1 {
		return MapResolvedSubject{}, &MapSubjectResolutionError{Code: MapSubjectAmbiguousCode, Input: input, Message: fmt.Sprintf("%q matches more than one map subject", input), Candidates: candidates}
	}
	return db.resolvedSubjectForProvince(ctx, input, candidates[0].ProvinceID, year)
}

func isLandedTitleID(input string) bool {
	if len(input) < 3 || input[1] != '_' {
		return false
	}
	switch input[0] {
	case 'b', 'c', 'd', 'k', 'e':
		return true
	default:
		return false
	}
}

func (db *DB) mapTitleCoreProvince(ctx context.Context, title string) (int, error) {
	seen := map[string]bool{}
	current := title
	for current != "" && !seen[current] {
		seen[current] = true
		var province sql.NullInt64
		var capital sql.NullString
		err := db.sql.QueryRowContext(ctx, `SELECT province_id,capital_title FROM map_titles WHERE title_id=?`, current).Scan(&province, &capital)
		if err == sql.ErrNoRows {
			return 0, &MapSubjectResolutionError{Code: MapSubjectNotFoundCode, Input: title, Message: fmt.Sprintf("landed title %q is not present in the map cache", title)}
		}
		if err != nil {
			return 0, err
		}
		if province.Valid && province.Int64 > 0 {
			return int(province.Int64), nil
		}
		if capital.Valid && capital.String != "" && capital.String != current {
			current = capital.String
			continue
		}
		break
	}

	column := map[byte]string{'b': "barony", 'c': "county", 'd': "duchy", 'k': "kingdom", 'e': "empire"}[title[0]]
	if column != "" {
		var provinceID int
		err := db.sql.QueryRowContext(ctx, `SELECT province_id FROM map_provinces WHERE `+column+`=? AND blocked=0 ORDER BY is_county_capital DESC, area DESC, province_id LIMIT 1`, title).Scan(&provinceID)
		if err == nil {
			return provinceID, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	var provinceID int
	err := db.sql.QueryRowContext(ctx, `SELECT tp.province_id
		FROM map_title_provinces tp JOIN map_provinces p ON p.province_id=tp.province_id
		WHERE tp.title_id=? AND p.blocked=0
		ORDER BY p.is_county_capital DESC,p.area DESC,tp.province_id LIMIT 1`, title).Scan(&provinceID)
	if err == sql.ErrNoRows {
		return 0, &MapSubjectResolutionError{Code: MapSubjectNotFoundCode, Input: title, Message: fmt.Sprintf("landed title %q has no traversable core province", title)}
	}
	return provinceID, err
}

func (db *DB) resolvedSubjectForProvince(ctx context.Context, input string, provinceID, year int) (MapResolvedSubject, error) {
	if year <= 0 {
		year = 1
	}
	province, err := db.mapProvinceAt(ctx, provinceID, yearDateKey(year))
	if err != nil {
		return MapResolvedSubject{}, err
	}
	if province.ProvinceID == 0 {
		return MapResolvedSubject{}, &MapSubjectResolutionError{Code: MapSubjectNotFoundCode, Input: input, Message: fmt.Sprintf("province %d is not present in the map cache", provinceID)}
	}
	result := MapResolvedSubject{
		Input: input, ProvinceID: province.ProvinceID, Barony: province.Barony, County: province.County,
		Duchy: province.Duchy, Kingdom: province.Kingdom, Empire: province.Empire,
	}
	for _, key := range []string{province.Barony, province.County, province.Duchy} {
		name := db.mapTitleNamesFast(ctx, key)
		if result.NameEN == "" && name.English != "" {
			result.NameEN = name.English
		}
		if result.NameZH == "" && name.Chinese != "" {
			result.NameZH = name.Chinese
		}
	}
	return result, nil
}

func (db *DB) mapSubjectNameCandidates(ctx context.Context, input string) ([]MapSubjectCandidate, error) {
	// Drive the lookup from the much smaller map_titles table and force indexed
	// key lookups into localization. Scanning localization by value made exact
	// Chinese place resolution dominate an otherwise sub-100 ms route query.
	rows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT t.title_id
		FROM map_titles t
		CROSS JOIN localization l INDEXED BY idx_loc_key ON l.key=t.title_id
		JOIN files f ON f.id=l.file_id
		WHERE f.overridden=0 AND trim(l.value)=trim(?) COLLATE NOCASE
		ORDER BY t.title_id LIMIT 64`, input)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	titles := []string{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, err
		}
		titles = append(titles, title)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byProvince := map[int]MapSubjectCandidate{}
	for _, title := range titles {
		provinceID, err := db.mapTitleCoreProvince(ctx, title)
		if err != nil {
			continue
		}
		name := db.mapTitleNamesFast(ctx, title)
		candidate := MapSubjectCandidate{Input: input, TitleID: title, ProvinceID: provinceID, NameEN: name.English, NameZH: name.Chinese}
		if existing, ok := byProvince[provinceID]; !ok || titleRank(title) < titleRank(existing.TitleID) {
			byProvince[provinceID] = candidate
		}
	}
	out := make([]MapSubjectCandidate, 0, len(byProvince))
	for _, candidate := range byProvince {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProvinceID != out[j].ProvinceID {
			return out[i].ProvinceID < out[j].ProvinceID
		}
		return out[i].TitleID < out[j].TitleID
	})
	return out, nil
}

func (db *DB) mapTitleNamesFast(ctx context.Context, title string) MapLocalizedName {
	name := MapLocalizedName{Key: title}
	if title == "" {
		return name
	}
	_ = db.sql.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT l.value FROM localization l INDEXED BY idx_loc_key JOIN files f ON f.id=l.file_id
			WHERE l.key=? AND f.overridden=0 AND lower(l.language || ' ' || l.path) LIKE '%english%'
			ORDER BY l.source_rank,l.path,l.line LIMIT 1),''),
		COALESCE((SELECT l.value FROM localization l INDEXED BY idx_loc_key JOIN files f ON f.id=l.file_id
			WHERE l.key=? AND f.overridden=0 AND (lower(l.language || ' ' || l.path) LIKE '%simp_chinese%' OR lower(l.language || ' ' || l.path) LIKE '%l_chinese%' OR lower(l.language) LIKE '%zh%')
			ORDER BY l.source_rank,l.path,l.line LIMIT 1),'')`, title, title).Scan(&name.English, &name.Chinese)
	return name
}

func titleRank(title string) int {
	if len(title) < 2 {
		return 99
	}
	return map[byte]int{'b': 0, 'c': 1, 'd': 2, 'k': 3, 'e': 4}[title[0]]
}
