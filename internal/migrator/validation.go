package migrator

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"ck3-index/internal/indexer"
	"ck3-index/internal/script"
)

var defaultNamedList = regexp.MustCompile(`(?s)\b(sea_zones|lakes|impassable_seas|impassable_mountains|river_provinces)\s*=\s*(?:LIST\s*)?\{(.*?)\}`)

func validateMigration(ctx context.Context, root string, targetIDs map[int]bool, patchFiles []indexer.PatchFileInput, db *indexer.DB) Validation {
	result := Validation{PreflightCounts: map[string]int{}}
	historySeen, terrainSeen, baronySeen := map[int]bool{}, map[int]bool{}, map[int]bool{}
	walkErr := filepath.WalkDir(root, func(full string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isTextPath(rel) {
			return nil
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		text := string(data)
		lower := strings.ToLower(rel)
		if isMigrationScriptPath(lower) {
			parsed := script.Parse(text)
			if strings.HasSuffix(lower, ".gui") {
				parsed = script.ParseGUI(text)
			}
			result.ParseErrors += len(parsed.Errors)
		}
		switch {
		case strings.HasPrefix(lower, "history/provinces/"):
			for _, node := range script.Parse(text).Nodes {
				id, err := strconv.Atoi(node.Key)
				if err != nil || node.Kind != "block" {
					continue
				}
				if historySeen[id] {
					result.DuplicateHistory++
				}
				historySeen[id] = true
				if !targetIDs[id] {
					result.MissingTargets++
				}
			}
		case strings.HasPrefix(lower, "common/province_terrain/"):
			for _, line := range splitLinesKeepEndings(text) {
				match := numericKeyLine.FindStringSubmatchIndex(line)
				if match == nil || commentBefore(line, match[4]) {
					continue
				}
				id, _ := strconv.Atoi(line[match[4]:match[5]])
				if terrainSeen[id] {
					result.DuplicateTerrain++
				}
				terrainSeen[id] = true
				if !targetIDs[id] {
					result.MissingTargets++
				}
			}
		case strings.HasPrefix(lower, "common/landed_titles/"):
			for _, match := range provinceAtom.FindAllStringSubmatchIndex(text, -1) {
				if insideCommentOrString(text, match[2]) {
					continue
				}
				id, _ := strconv.Atoi(text[match[2]:match[3]])
				if baronySeen[id] {
					result.DuplicateBarony++
				}
				baronySeen[id] = true
				if !targetIDs[id] {
					result.MissingTargets++
				}
			}
		case lower == "map_data/adjacencies.csv":
			validateAdjacencyText(text, targetIDs, &result)
		case lower == "map_data/default.map":
			validateDefaultMapText(text, targetIDs, &result)
		}
		if isMigrationScriptPath(lower) {
			for _, match := range typedProvince.FindAllStringSubmatchIndex(text, -1) {
				if insideCommentOrString(text, match[2]) {
					continue
				}
				id, _ := strconv.Atoi(text[match[2]:match[3]])
				if !targetIDs[id] {
					result.MissingTargets++
				}
			}
		}
		if strings.Contains(lower, "geographical_region") || lower == "map_data/island_region.txt" || lower == "map_data/climate.txt" {
			patterns := []*regexp.Regexp{provinceBlock}
			for _, pattern := range patterns {
				for _, match := range pattern.FindAllStringSubmatch(text, -1) {
					if len(match) < 2 {
						continue
					}
					seen := map[int]bool{}
					for _, id := range numericIDsOutsideComments(match[1]) {
						if seen[id] {
							result.InvalidRegion++
						}
						seen[id] = true
						if !targetIDs[id] {
							result.MissingTargets++
						}
					}
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		result.PreflightCounts["execution_errors"]++
	}

	if db != nil && len(patchFiles) > 0 {
		var supported []indexer.PatchFileInput
		for _, file := range patchFiles {
			if _, err := indexer.AnalyzeVirtualFile(file.Path, "migration", 1, file.Content); err == nil {
				supported = append(supported, file)
			}
		}
		if len(supported) > 0 {
			preflight, err := db.LLMPreflightPatch(ctx, supported, indexer.LLMOptions{Limit: 20, AllowProject: true})
			if err != nil {
				result.PreflightCounts["execution_errors"]++
			} else {
				for key, value := range preflight.Counts {
					result.PreflightCounts[key] = value
				}
			}
		}
	}
	mapCfg := indexer.Config{Sources: []indexer.Source{{Name: "migration_output", Path: root, Rank: 1}}}
	if audit, err := indexer.AuditMapAssets(ctx, mapCfg, "summary", 20); err != nil {
		result.MapAuditErrors++
	} else {
		result.MapAuditErrors = audit.Counts["error"]
		result.MapAuditWarnings = audit.Counts["warning"]
	}
	result.Blocked = result.ParseErrors > 0 || result.MissingTargets > 0 || result.DuplicateHistory > 0 ||
		result.DuplicateTerrain > 0 || result.DuplicateBarony > 0 || result.InvalidAdjacency > 0 ||
		result.InvalidDefaultMap > 0 || result.InvalidRegion > 0 ||
		result.MapAuditErrors > 0 || result.PreflightCounts["blocking_risks"] > 0 || result.PreflightCounts["execution_errors"] > 0
	return result
}

func isMigrationScriptPath(path string) bool {
	for _, extension := range []string{".txt", ".map", ".gui", ".asset", ".settings", ".mod"} {
		if strings.HasSuffix(path, extension) {
			return true
		}
	}
	return false
}

func validateDefaultMapText(text string, targetIDs map[int]bool, result *Validation) {
	water, land := map[int]bool{}, map[int]bool{}
	for _, match := range defaultNamedList.FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		kind := match[1]
		seen := map[int]bool{}
		for _, id := range numericIDsOutsideComments(match[2]) {
			if seen[id] {
				result.InvalidDefaultMap++
			}
			seen[id] = true
			if !targetIDs[id] {
				result.MissingTargets++
			}
			if kind == "impassable_mountains" {
				land[id] = true
			} else {
				water[id] = true
			}
		}
	}
	for id := range water {
		if land[id] {
			result.InvalidDefaultMap++
		}
	}
}

func numericIDsOutsideComments(text string) []int {
	var ids []int
	quoted, escaped, comment := false, false, false
	for i := 0; i < len(text); {
		c := text[i]
		if comment {
			if c == '\n' {
				comment = false
			}
			i++
			continue
		}
		if escaped {
			escaped = false
			i++
			continue
		}
		if c == '\\' && quoted {
			escaped = true
			i++
			continue
		}
		if c == '"' {
			quoted = !quoted
			i++
			continue
		}
		if c == '#' && !quoted {
			comment = true
			i++
			continue
		}
		if !quoted && c >= '0' && c <= '9' {
			end := i + 1
			for end < len(text) && text[end] >= '0' && text[end] <= '9' {
				end++
			}
			id, _ := strconv.Atoi(text[i:end])
			ids = append(ids, id)
			i = end
			continue
		}
		i++
	}
	return ids
}

func validateAdjacencyText(text string, targetIDs map[int]bool, result *Validation) {
	seen := map[string]bool{}
	for _, line := range splitLinesKeepEndings(text) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(strings.ToLower(trimmed), "from;") {
			continue
		}
		fields := strings.Split(strings.TrimRight(line, "\r\n"), ";")
		if len(fields) < 4 {
			result.InvalidAdjacency++
			continue
		}
		key := strings.Join(fields[:4], ";")
		if seen[key] {
			result.InvalidAdjacency++
		}
		seen[key] = true
		for _, column := range []int{0, 1, 3} {
			id, err := strconv.Atoi(strings.TrimSpace(fields[column]))
			if err != nil || id < 0 {
				continue
			}
			if id == 0 || !targetIDs[id] {
				result.InvalidAdjacency++
			}
		}
	}
}
