package migrator

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"ck3-index/internal/indexer"
)

var defaultWaterList = regexp.MustCompile(`(?s)\b(?:sea_zones|lakes|impassable_seas|river_provinces)\s*=\s*(?:LIST\s*)?\{(.*?)\}`)

func mappingForMigration(ctx context.Context, snapshotRoot string, target indexer.Source, points []indexer.MapControlPoint) (indexer.MapProvinceMappingResult, error) {
	cfg := indexer.Config{Sources: []indexer.Source{
		{Name: "migration_snapshot", Path: filepath.Join(snapshotRoot, "active_map"), Rank: 1},
		{Name: "migration_target", Path: target.Path, Rank: 2},
	}}
	return indexer.MapProvinceMapping(ctx, cfg, indexer.MapProvinceMappingSpec{
		Source: "migration_snapshot", Target: "migration_target", ControlPoints: points,
		MinShare: 0.05, MaxCandidates: 50,
	})
}

func definitionIDs(root string) (map[int]bool, error) {
	file, err := os.Open(filepath.Join(root, "map_data", "definition.csv"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.Comma = ';'
	reader.FieldsPerRecord = -1
	ids := map[int]bool{}
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) == 0 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(record[0], "\ufeff")))
		if err == nil && id > 0 {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("map_data/definition.csv contains no province ids")
	}
	return ids, nil
}

func waterProvinceIDs(root string) (map[int]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "map_data", "default.map"))
	if os.IsNotExist(err) {
		return map[int]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	water := map[int]bool{}
	for _, match := range defaultWaterList.FindAllStringSubmatch(string(data), -1) {
		if len(match) < 2 {
			continue
		}
		var uncommented strings.Builder
		lineScanner := bufio.NewScanner(strings.NewReader(match[1]))
		for lineScanner.Scan() {
			line := lineScanner.Text()
			if index := strings.IndexByte(line, '#'); index >= 0 {
				line = line[:index]
			}
			uncommented.WriteString(line)
			uncommented.WriteByte('\n')
		}
		scanner := bufio.NewScanner(strings.NewReader(uncommented.String()))
		scanner.Split(bufio.ScanWords)
		for scanner.Scan() {
			token := scanner.Text()
			id, err := strconv.Atoi(strings.Trim(token, "{}\r\n\t "))
			if err == nil && id > 0 {
				water[id] = true
			}
		}
	}
	return water, nil
}

func targetGeometryAuthority(rel string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	if !strings.HasPrefix(lower, "map_data/") {
		return false
	}
	base := filepath.Base(lower)
	if base == "definition.csv" || base == "provinces.png" || base == "provinces.bmp" || base == "rivers.png" {
		return true
	}
	return strings.Contains(base, "heightmap") || strings.Contains(base, "topology") || strings.Contains(base, "indirection") ||
		strings.HasPrefix(base, "packed_") || strings.HasPrefix(base, "nodes") || strings.Contains(base, "flatmap")
}
