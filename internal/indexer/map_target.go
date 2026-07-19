package indexer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type mapTargetSelector struct {
	Kind  string
	Value string
}

func parseMapTargetSelector(raw, explicitType string) (mapTargetSelector, error) {
	raw = strings.TrimSpace(raw)
	explicitType = strings.ToLower(strings.TrimSpace(explicitType))
	if raw == "" {
		return mapTargetSelector{}, fmt.Errorf("empty map target")
	}
	if explicitType == "targets" {
		explicitType = ""
	}
	if explicitType == "all" || strings.EqualFold(raw, "all") {
		if explicitType != "" && explicitType != "all" {
			return mapTargetSelector{}, fmt.Errorf("map target %q is not a %s target", raw, explicitType)
		}
		return mapTargetSelector{Kind: "all", Value: "all"}, nil
	}
	if region, ok := mapRegionTargetID(raw); ok {
		if explicitType != "" && explicitType != "region" {
			return mapTargetSelector{}, fmt.Errorf("map target %q is a region, not a %s", raw, explicitType)
		}
		return mapTargetSelector{Kind: "region", Value: region}, nil
	}
	switch explicitType {
	case "region":
		return mapTargetSelector{Kind: "region", Value: raw}, nil
	case "province":
		if _, err := strconv.Atoi(raw); err != nil {
			return mapTargetSelector{}, fmt.Errorf("province target %q is not numeric", raw)
		}
		return mapTargetSelector{Kind: "province", Value: raw}, nil
	case "title":
		if _, err := strconv.Atoi(raw); err == nil {
			return mapTargetSelector{}, fmt.Errorf("title target %q is numeric", raw)
		}
		return mapTargetSelector{Kind: "title", Value: raw}, nil
	case "":
		if _, err := strconv.Atoi(raw); err == nil {
			return mapTargetSelector{Kind: "province", Value: raw}, nil
		}
		return mapTargetSelector{Kind: "title", Value: raw}, nil
	default:
		return mapTargetSelector{}, fmt.Errorf("unsupported map target type %q", explicitType)
	}
}

func parseMapTargetSelectors(targets []string, explicitType string) ([]mapTargetSelector, error) {
	selectors := make([]mapTargetSelector, 0, len(targets))
	for _, raw := range targets {
		selector, err := parseMapTargetSelector(raw, explicitType)
		if err != nil {
			return nil, err
		}
		if selector.Kind == "all" {
			if len(targets) != 1 {
				return nil, fmt.Errorf("map target all cannot be combined with other targets")
			}
			return []mapTargetSelector{selector}, nil
		}
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

// mapTargetSelectionCTE returns a bounded, parameterized province selection.
// Callers append their query after the returned WITH clause.
func mapTargetSelectionCTE(selectors []mapTargetSelector) (string, []any, error) {
	if len(selectors) == 0 {
		return "", nil, fmt.Errorf("map target selection is empty")
	}
	parts := make([]string, 0, len(selectors))
	args := make([]any, 0, len(selectors))
	for _, selector := range selectors {
		switch selector.Kind {
		case "all":
			parts = []string{"SELECT province_id FROM map_provinces"}
			args = nil
		case "province":
			id, _ := strconv.Atoi(selector.Value)
			parts = append(parts, "SELECT ? AS province_id")
			args = append(args, id)
		case "title":
			parts = append(parts, "SELECT province_id FROM map_title_provinces WHERE title_id=?")
			args = append(args, selector.Value)
		case "region":
			parts = append(parts, "SELECT province_id FROM map_province_regions WHERE region_id=?")
			args = append(args, selector.Value)
		default:
			return "", nil, fmt.Errorf("unsupported map target selector %q", selector.Kind)
		}
	}
	return "WITH selected(province_id) AS (" + strings.Join(parts, " UNION ") + ") ", args, nil
}

func (db *DB) mapTargetProvinceIDs(ctx context.Context, selector mapTargetSelector) ([]int, error) {
	cte, args, err := mapTargetSelectionCTE([]mapTargetSelector{selector})
	if err != nil {
		return nil, err
	}
	rows, err := db.sql.QueryContext(ctx, cte+`SELECT DISTINCT s.province_id
		FROM selected s JOIN map_provinces p ON p.province_id=s.province_id
		ORDER BY s.province_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
