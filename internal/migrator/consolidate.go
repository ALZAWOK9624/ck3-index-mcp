package migrator

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ck3-index/internal/indexer"
	"ck3-index/internal/script"
)

type semanticRecordOwner struct {
	canonical string
	project   bool
}

// consolidateIdenticalRecords removes only byte-equivalent records introduced
// by project replay. Different records remain in place so strict duplicate
// validation can block the artifact rather than guessing precedence.
func consolidateIdenticalRecords(root string, files []FileResult, patches []indexer.PatchFileInput) ([]FileResult, []indexer.PatchFileInput, error) {
	order := make([]int, len(files))
	for i := range files {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := files[order[i]], files[order[j]]
		if (a.Origin == "target") != (b.Origin == "target") {
			return a.Origin == "target"
		}
		return a.Path < b.Path
	})
	historySeen, terrainSeen := map[int]semanticRecordOwner{}, map[int]semanticRecordOwner{}
	for _, index := range order {
		file := &files[index]
		lower := strings.ToLower(filepath.ToSlash(file.Path))
		project := file.Origin == "project"
		if !strings.HasPrefix(lower, "history/provinces/") && !strings.HasPrefix(lower, "common/province_terrain/") {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(file.Path))
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, nil, err
		}
		text := string(data)
		updated, removed := text, 0
		if strings.HasPrefix(lower, "history/provinces/") {
			updated, removed = consolidateHistoryText(text, project, historySeen)
		} else {
			updated, removed = consolidateTerrainText(text, project, terrainSeen)
		}
		if removed == 0 {
			continue
		}
		if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
			return nil, nil, err
		}
		file.Replacements += removed
		file.Merge += "+dedupe"
		file.SHA256, file.Size = hashBytes([]byte(updated)), int64(len(updated))
		patches = upsertPatch(patches, file.Path, updated)
	}
	return files, patches, nil
}

func consolidateHistoryText(text string, project bool, seen map[int]semanticRecordOwner) (string, int) {
	parsed := script.Parse(text)
	type removal struct{ start, end int }
	var removals []removal
	for _, node := range parsed.Nodes {
		id, err := strconv.Atoi(node.Key)
		start, end, ok := nodeRuneSpan(text, node)
		if err != nil || node.Kind != "block" || !ok {
			continue
		}
		block := string([]rune(text)[start:end])
		canonical := replaceLeadingNumericKey(block, id, 0)
		if owner, exists := seen[id]; exists {
			if owner.canonical == canonical && project {
				removals = append(removals, removal{start, end})
			}
			continue
		}
		seen[id] = semanticRecordOwner{canonical: canonical, project: project}
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].start > removals[j].start })
	runes := []rune(text)
	for _, item := range removals {
		runes = append(runes[:item.start], runes[item.end:]...)
	}
	return string(runes), len(removals)
}

func consolidateTerrainText(text string, project bool, seen map[int]semanticRecordOwner) (string, int) {
	lines := splitLinesKeepEndings(text)
	removed := 0
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		match := numericKeyLine.FindStringSubmatchIndex(line)
		if match == nil || commentBefore(line, match[4]) {
			out = append(out, line)
			continue
		}
		id, _ := strconv.Atoi(line[match[4]:match[5]])
		canonical := line[match[5]:]
		if owner, exists := seen[id]; exists {
			if owner.canonical == canonical && project {
				removed++
				continue
			}
			out = append(out, line)
			continue
		}
		seen[id] = semanticRecordOwner{canonical: canonical, project: project}
		out = append(out, line)
	}
	return strings.Join(out, ""), removed
}

func upsertPatch(patches []indexer.PatchFileInput, path, content string) []indexer.PatchFileInput {
	for i := range patches {
		if strings.EqualFold(patches[i].Path, path) {
			patches[i].Content = content
			return patches
		}
	}
	return append(patches, indexer.PatchFileInput{Path: path, Content: content})
}
