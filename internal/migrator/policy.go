package migrator

import (
	"fmt"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
)

const (
	minimumAutomaticCoverage   = 0.95
	minimumAutomaticConfidence = 0.85
	minimumSplitTargetShare    = 0.50
)

type mapDecision struct {
	Source         int
	Targets        []int
	Kind           string
	Coverage       float64
	Confidence     float64
	SafeScalar     bool
	SafeCollection bool
}

type migrationPolicy struct {
	decisions   map[int]mapDecision
	resolutions map[string]Resolution
	bySource    map[int]Resolution
	sourceWater map[int]bool
	targetWater map[int]bool
	targetIDs   map[int]bool
}

func buildPolicy(mapping indexer.MapProvinceMappingResult, resolutions []Resolution, sourceWater, targetWater, targetIDs map[int]bool) (*migrationPolicy, error) {
	p := &migrationPolicy{decisions: map[int]mapDecision{}, resolutions: map[string]Resolution{}, bySource: map[int]Resolution{}, sourceWater: sourceWater, targetWater: targetWater, targetIDs: targetIDs}
	for _, resolution := range resolutions {
		resolution.Action = strings.ToLower(strings.TrimSpace(resolution.Action))
		switch resolution.Action {
		case "select_target", "expand", "prefer_project", "prefer_target", "drop":
		default:
			return nil, fmt.Errorf("unsupported migration resolution action %q", resolution.Action)
		}
		if strings.TrimSpace(resolution.ConflictID) == "" && resolution.SourceProvince <= 0 {
			return nil, fmt.Errorf("migration resolution requires conflict_id or source_province")
		}
		if (resolution.Action == "prefer_project" || resolution.Action == "prefer_target") && strings.TrimSpace(resolution.ConflictID) == "" {
			return nil, fmt.Errorf("%s resolution requires conflict_id", resolution.Action)
		}
		if resolution.ConflictID != "" {
			p.resolutions[resolution.ConflictID] = resolution
		}
		if resolution.SourceProvince > 0 {
			p.bySource[resolution.SourceProvince] = resolution
		}
	}
	for _, row := range mapping.Sources {
		d := mapDecision{Source: row.ProvinceID, Kind: row.Classification, Coverage: row.Coverage}
		complex := false
		for _, candidate := range row.Candidates {
			d.Targets = append(d.Targets, candidate.ProvinceID)
			if candidate.Confidence > d.Confidence {
				d.Confidence = candidate.Confidence
			}
			if candidate.Relation == "complex" {
				complex = true
			}
		}
		if complex {
			d.Kind = "complex"
		}
		sort.Ints(d.Targets)
		waterKnown := mapping.Source.WaterClassificationKnown && mapping.Target.WaterClassificationKnown
		if !complex && len(row.Candidates) == 1 && (row.Classification == "one_to_one" || row.Classification == "renumbered") {
			candidate := row.Candidates[0]
			d.SafeScalar = waterKnown && p.sourceWater[row.ProvinceID] == p.targetWater[candidate.ProvinceID] && row.Coverage >= minimumAutomaticCoverage && candidate.Confidence >= minimumAutomaticConfidence
			d.SafeCollection = d.SafeScalar
		}
		if !complex && row.Classification == "split" && row.Coverage >= minimumAutomaticCoverage && len(row.Candidates) > 1 {
			d.SafeCollection = waterKnown
			for _, candidate := range row.Candidates {
				if candidate.TargetShare < minimumSplitTargetShare || p.sourceWater[row.ProvinceID] != p.targetWater[candidate.ProvinceID] {
					d.SafeCollection = false
				}
			}
		}
		if !complex && row.Classification == "merge" && row.Coverage >= minimumAutomaticCoverage && len(row.Candidates) == 1 {
			d.SafeCollection = waterKnown && p.sourceWater[row.ProvinceID] == p.targetWater[row.Candidates[0].ProvinceID]
		}
		p.decisions[row.ProvinceID] = d
	}
	return p, nil
}

func (p *migrationPolicy) targetsFor(source int, mode, path string, line int) ([]int, *Conflict) {
	d := p.decisions[source]
	conflict := semanticConflict("province_mapping_unresolved", path, line, source,
		fmt.Sprintf("province %d has no safe %s migration (%s, coverage %.3f, confidence %.3f)", source, mode, d.Kind, d.Coverage, d.Confidence))
	if resolution, ok := p.bySource[source]; ok {
		return p.applyResolution(resolution, source, conflict)
	}
	if resolution, ok := p.resolutions[conflict.ID]; ok {
		return p.applyResolution(resolution, source, conflict)
	}
	safe := d.SafeScalar
	if mode == "collection" || mode == "record" {
		safe = d.SafeCollection
	}
	if safe && len(d.Targets) > 0 {
		for _, target := range d.Targets {
			if !p.targetIDs[target] {
				missing := semanticConflict("target_province_missing", path, line, source, fmt.Sprintf("mapped target province %d is absent from target definition.csv", target))
				return nil, &missing
			}
		}
		return append([]int(nil), d.Targets...), nil
	}
	return nil, &conflict
}

func (p *migrationPolicy) applyResolution(resolution Resolution, source int, conflict Conflict) ([]int, *Conflict) {
	switch resolution.Action {
	case "drop":
		return []int{}, nil
	case "select_target", "expand":
		if resolution.Action == "select_target" && len(resolution.TargetProvinces) != 1 {
			conflict.Message = "select_target resolution requires exactly one target province"
			return nil, &conflict
		}
		if len(resolution.TargetProvinces) == 0 {
			conflict.Message = resolution.Action + " resolution requires target_provinces"
			return nil, &conflict
		}
		seen := map[int]bool{}
		var targets []int
		for _, target := range resolution.TargetProvinces {
			if !p.targetIDs[target] {
				conflict.Message = fmt.Sprintf("resolution target province %d is absent from target definition.csv", target)
				return nil, &conflict
			}
			if p.sourceWater[source] != p.targetWater[target] && !resolution.AllowTypeChange {
				conflict.Message = fmt.Sprintf("resolution changes province %d between land and water; set allow_type_change=true to acknowledge", source)
				return nil, &conflict
			}
			if !seen[target] {
				seen[target] = true
				targets = append(targets, target)
			}
		}
		sort.Ints(targets)
		return targets, nil
	default:
		return nil, &conflict
	}
}

func semanticConflict(code, path string, line, source int, message string) Conflict {
	c := Conflict{Code: code, Path: path, Line: line, SourceProvince: source, Message: message, Severity: "error", SuggestedAction: "select_target"}
	c.ID = conflictID(c.Code, c.Path, c.Line, c.SourceProvince, c.Message)
	return c
}
