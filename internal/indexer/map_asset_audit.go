package indexer

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	mapAssetAuditUpstreamURL    = "https://github.com/MnTronslien/AzgaarToCK3"
	mapAssetAuditUpstreamCommit = "5c41484fecc58fc23f66a1c92544861c47f42278"
)

// MapAssetAuditResult exposes the high-value, non-duplicated part of the
// AzgaarToCK3 validation pipeline: CK3 raster encoding, palette semantics, and
// definition-to-image integrity. The parser, title graph, and map geometry stay
// owned by ck3-index rather than being duplicated from the converter.
type MapAssetAuditResult struct {
	Intent     string                  `json:"intent"`
	Operation  string                  `json:"operation"`
	Status     string                  `json:"status"`
	Counts     map[string]int          `json:"counts"`
	Assets     []MapAssetAuditAsset    `json:"assets,omitempty"`
	Findings   []MapAssetAuditFinding  `json:"findings,omitempty"`
	Provenance MapAssetAuditProvenance `json:"provenance"`
	Guidance   []string                `json:"guidance,omitempty"`
}

type MapAssetAuditAsset struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	Source      string `json:"source"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	BitDepth    int    `json:"bit_depth,omitempty"`
	ColorType   int    `json:"png_color_type,omitempty"`
	Definitions int    `json:"definitions,omitempty"`
	Colors      int    `json:"colors,omitempty"`
}

type MapAssetAuditFinding struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Path     string   `json:"path,omitempty"`
	Source   string   `json:"source,omitempty"`
	Message  string   `json:"message"`
	Count    int      `json:"count,omitempty"`
	Samples  []string `json:"samples,omitempty"`
}

type MapAssetAuditProvenance struct {
	Source          string   `json:"source"`
	URL             string   `json:"url"`
	Commit          string   `json:"commit"`
	License         string   `json:"license"`
	Absorbed        []string `json:"absorbed"`
	ExcludedOverlap []string `json:"excluded_overlap"`
}

type pngMetadata struct {
	Width, Height int
	BitDepth      byte
	ColorType     byte
	Palette       [][3]byte
}

type provinceDefinitionAudit struct {
	ColorToID       map[uint32]int
	IDToColor       map[int]uint32
	InvalidRows     int
	DuplicateIDs    int
	DuplicateColors int
	Samples         []string
}

// AuditMapAssets performs a bounded, read-only audit of the active map files.
// operation is summary, provinces, or rivers. The caller is responsible for
// removing rank=1 sources when public visibility is requested.
func AuditMapAssets(ctx context.Context, cfg Config, operation string, limit int) (MapAssetAuditResult, error) {
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation == "" {
		operation = "summary"
	}
	if operation != "summary" && operation != "provinces" && operation != "rivers" {
		return MapAssetAuditResult{}, fmt.Errorf("unknown map asset audit operation %q; expected summary, provinces, or rivers", operation)
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}

	result := MapAssetAuditResult{
		Intent:    "map_asset_audit",
		Operation: operation,
		Status:    "ok",
		Counts:    map[string]int{},
		Provenance: MapAssetAuditProvenance{
			Source:  "AzgaarToCK3 map validators, reimplemented against active CK3 assets",
			URL:     mapAssetAuditUpstreamURL,
			Commit:  mapAssetAuditUpstreamCommit,
			License: "MIT",
			Absorbed: []string{
				"provinces.png encoding and definition.csv color coverage",
				"rivers.png indexed-palette contract and orthogonal river topology",
			},
			ExcludedOverlap: []string{
				"Clausewitz parsing", "title and province graph construction", "map geometry and adjacency indexing", "converter-specific culture and faith heuristics", "AzgaarToCK3's over-strict rejection of reserved river indices 12-15",
			},
		},
		Guidance: []string{
			"Treat format and palette errors as crash or invisible-river risks; inspect the named source-root-relative asset before launching CK3.",
			"River topology findings are warnings for visual review because active upstream maps can contain deliberate junction geometry.",
			"Converter-specific terrain, culture, faith, and title-generation choices are intentionally not treated as CK3 engine rules.",
		},
	}

	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		return result, err
	}
	if operation == "summary" || operation == "provinces" {
		if err := auditProvinceAssets(ctx, active, limit, &result); err != nil {
			return result, err
		}
	}
	if operation == "summary" || operation == "rivers" {
		if err := auditRiverAsset(ctx, active, limit, &result); err != nil {
			return result, err
		}
	}

	result.Counts["assets"] = len(result.Assets)
	result.Counts["findings"] = len(result.Findings)
	for _, finding := range result.Findings {
		result.Counts[finding.Severity]++
	}
	if result.Counts["error"] > 0 {
		result.Status = "error"
	} else if result.Counts["warning"] > 0 {
		result.Status = "warning"
	} else if len(result.Assets) == 0 {
		result.Status = "unavailable"
	}
	return result, nil
}

func auditProvinceAssets(ctx context.Context, active map[string]activeMapFile, limit int, result *MapAssetAuditResult) error {
	definition := active["map_data/definition.csv"]
	provinces := active["map_data/provinces.png"]
	if definition.Path == "" && provinces.Path == "" {
		return nil
	}
	if definition.Path == "" || provinces.Path == "" {
		missing := "map_data/definition.csv"
		present := provinces
		if provinces.Path == "" {
			missing = "map_data/provinces.png"
			present = definition
		}
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_asset_missing_pair", Severity: "error", Path: present.Rel, Source: present.Src.Name, Message: "active map has only one of definition.csv and provinces.png; missing " + missing})
		return nil
	}

	defs, err := parseProvinceDefinitionsForAudit(definition.Path, limit)
	if err != nil {
		return fmt.Errorf("map asset audit %s: %w", definition.Rel, err)
	}
	result.Assets = append(result.Assets, MapAssetAuditAsset{Kind: "province_definitions", Path: definition.Rel, Source: definition.Src.Name, Definitions: len(defs.IDToColor), Colors: len(defs.ColorToID)})
	result.Counts["province_definitions"] = len(defs.IDToColor)
	if defs.InvalidRows > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_definition_invalid_rows", Severity: "error", Path: definition.Rel, Source: definition.Src.Name, Message: "definition.csv contains malformed province rows", Count: defs.InvalidRows, Samples: defs.Samples})
	}
	if defs.DuplicateIDs > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_definition_duplicate_ids", Severity: "error", Path: definition.Rel, Source: definition.Src.Name, Message: "definition.csv assigns one province id more than once", Count: defs.DuplicateIDs})
	}
	if defs.DuplicateColors > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_definition_duplicate_colors", Severity: "error", Path: definition.Rel, Source: definition.Src.Name, Message: "definition.csv assigns one RGB color to multiple province ids", Count: defs.DuplicateColors})
	}

	meta, err := readPNGMetadata(provinces.Path)
	if err != nil {
		return fmt.Errorf("map asset audit %s: %w", provinces.Rel, err)
	}
	asset := MapAssetAuditAsset{Kind: "province_image", Path: provinces.Rel, Source: provinces.Src.Name, Width: meta.Width, Height: meta.Height, BitDepth: int(meta.BitDepth), ColorType: int(meta.ColorType)}
	result.Assets = append(result.Assets, asset)
	if meta.ColorType != 2 && meta.ColorType != 3 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_provinces_encoding", Severity: "error", Path: provinces.Rel, Source: provinces.Src.Name, Message: fmt.Sprintf("provinces.png uses PNG color type %d; CK3 requires RGB truecolor (2) or indexed color (3) without alpha", meta.ColorType)})
	}

	f, err := os.Open(provinces.Path)
	if err != nil {
		return err
	}
	img, _, decodeErr := image.Decode(f)
	closeErr := f.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode %s: %w", provinces.Rel, decodeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	seen := make(map[uint32]int, len(defs.ColorToID))
	undefinedPixels, blackPixels := 0, 0
	undefinedColors := map[uint32]int{}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if y&127 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			color := uint32(uint8(r>>8))<<16 | uint32(uint8(g>>8))<<8 | uint32(uint8(b>>8))
			seen[color]++
			if color == 0 {
				blackPixels++
			}
			if _, ok := defs.ColorToID[color]; !ok {
				undefinedPixels++
				undefinedColors[color]++
			}
		}
	}
	result.Assets[len(result.Assets)-1].Colors = len(seen)
	result.Counts["province_image_colors"] = len(seen)
	result.Counts["province_image_pixels"] = bounds.Dx() * bounds.Dy()
	if blackPixels > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_provinces_black_pixels", Severity: "error", Path: provinces.Rel, Source: provinces.Src.Name, Message: "provinces.png contains black pixels, which are not a valid province color", Count: blackPixels})
	}
	if len(undefinedColors) > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_provinces_undefined_colors", Severity: "error", Path: provinces.Rel, Source: provinces.Src.Name, Message: fmt.Sprintf("provinces.png contains %d RGB colors (%d pixels) absent from definition.csv", len(undefinedColors), undefinedPixels), Count: len(undefinedColors), Samples: colorCountSamples(undefinedColors, limit)})
	}
	missing := make([]int, 0)
	for id, color := range defs.IDToColor {
		if seen[color] == 0 {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Ints(missing)
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_definition_missing_pixels", Severity: "error", Path: definition.Rel, Source: definition.Src.Name, Message: "definition.csv contains province ids with no pixels in provinces.png", Count: len(missing), Samples: intSamples(missing, limit)})
	}
	return nil
}

func auditRiverAsset(ctx context.Context, active map[string]activeMapFile, limit int, result *MapAssetAuditResult) error {
	rivers := active["map_data/rivers.png"]
	if rivers.Path == "" {
		return nil
	}
	meta, err := readPNGMetadata(rivers.Path)
	if err != nil {
		return fmt.Errorf("map asset audit %s: %w", rivers.Rel, err)
	}
	result.Assets = append(result.Assets, MapAssetAuditAsset{Kind: "river_image", Path: rivers.Rel, Source: rivers.Src.Name, Width: meta.Width, Height: meta.Height, BitDepth: int(meta.BitDepth), ColorType: int(meta.ColorType), Colors: len(meta.Palette)})
	if meta.BitDepth != 8 || meta.ColorType != 3 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_rivers_encoding", Severity: "error", Path: rivers.Rel, Source: rivers.Src.Name, Message: fmt.Sprintf("rivers.png is bit depth %d / PNG color type %d; CK3 requires an 8-bit indexed PNG", meta.BitDepth, meta.ColorType)})
	}
	paletteMismatchCount, paletteMismatches := riverPaletteMismatches(meta.Palette, limit)
	if paletteMismatchCount > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_rivers_palette_order", Severity: "error", Path: rivers.Rel, Source: rivers.Src.Name, Message: "rivers.png palette indices do not match CK3 marker, width, sea, and land semantics", Count: paletteMismatchCount, Samples: paletteMismatches})
	}

	f, err := os.Open(rivers.Path)
	if err != nil {
		return err
	}
	img, _, decodeErr := image.Decode(f)
	closeErr := f.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode %s: %w", rivers.Rel, decodeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	paletted, ok := img.(*image.Paletted)
	if !ok {
		return nil // Encoding finding above already explains why topology cannot be trusted.
	}
	invalidIndices, topology := map[byte]int{}, 0
	invalidSamples, topologySamples := []string{}, []string{}
	b := paletted.Bounds()
	body := func(index byte) bool { return index >= 3 && index <= 11 }
	neighbors := func(x, y int) int {
		count := 0
		for _, delta := range [][2]int{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
			nx, ny := x+delta[0], y+delta[1]
			if nx >= b.Min.X && nx < b.Max.X && ny >= b.Min.Y && ny < b.Max.Y && body(paletted.ColorIndexAt(nx, ny)) {
				count++
			}
		}
		return count
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if y&127 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			index := paletted.ColorIndexAt(x, y)
			if index > 15 && index != 254 && index != 255 {
				invalidIndices[index]++
				if len(invalidSamples) < limit {
					invalidSamples = append(invalidSamples, fmt.Sprintf("index %d at %d,%d", index, x, y))
				}
				continue
			}
			if index > 11 {
				continue
			}
			n := neighbors(x, y)
			bad := body(index) && (n < 1 || n > 2) || index == 0 && n != 1 || (index == 1 || index == 2) && n != 2
			if bad {
				topology++
				if len(topologySamples) < limit {
					topologySamples = append(topologySamples, fmt.Sprintf("index %d at %d,%d has %d river-body neighbors", index, x, y, n))
				}
			}
		}
	}
	result.Counts["river_pixels"] = b.Dx() * b.Dy()
	if len(invalidIndices) > 0 {
		count := 0
		for _, value := range invalidIndices {
			count += value
		}
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_rivers_invalid_indices", Severity: "error", Path: rivers.Rel, Source: rivers.Src.Name, Message: "rivers.png uses palette indices outside CK3 land, sea, marker, and river-body semantics", Count: count, Samples: invalidSamples})
	}
	if topology > 0 {
		addMapAuditFinding(result, MapAssetAuditFinding{Code: "map_rivers_topology", Severity: "warning", Path: rivers.Rel, Source: rivers.Src.Name, Message: "river body or marker pixels violate CK3 orthogonal-neighbor topology", Count: topology, Samples: topologySamples})
	}
	return nil
}

func parseProvinceDefinitionsForAudit(path string, limit int) (provinceDefinitionAudit, error) {
	result := provinceDefinitionAudit{ColorToID: map[uint32]int{}, IDToColor: map[int]uint32{}}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReader(f))
	r.Comma = ';'
	r.FieldsPerRecord = -1
	line := 0
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil || len(record) < 4 {
			result.InvalidRows++
			if len(result.Samples) < limit {
				result.Samples = append(result.Samples, fmt.Sprintf("line %d: malformed row", line))
			}
			continue
		}
		id, idErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(record[0], "\ufeff")))
		rr, rErr := strconv.Atoi(strings.TrimSpace(record[1]))
		gg, gErr := strconv.Atoi(strings.TrimSpace(record[2]))
		bb, bErr := strconv.Atoi(strings.TrimSpace(record[3]))
		if idErr != nil || rErr != nil || gErr != nil || bErr != nil {
			if line == 1 { // Header rows are common and harmless.
				continue
			}
			result.InvalidRows++
			if len(result.Samples) < limit {
				result.Samples = append(result.Samples, fmt.Sprintf("line %d: non-numeric id or RGB", line))
			}
			continue
		}
		if id == 0 { // CK3 definition.csv sentinel.
			continue
		}
		if id < 0 || rr < 0 || rr > 255 || gg < 0 || gg > 255 || bb < 0 || bb > 255 {
			result.InvalidRows++
			if len(result.Samples) < limit {
				result.Samples = append(result.Samples, fmt.Sprintf("line %d: id or RGB outside valid range", line))
			}
			continue
		}
		color := uint32(rr)<<16 | uint32(gg)<<8 | uint32(bb)
		if previous, exists := result.IDToColor[id]; exists && previous != color {
			result.DuplicateIDs++
		}
		if previous, exists := result.ColorToID[color]; exists && previous != id {
			result.DuplicateColors++
		}
		result.IDToColor[id] = color
		result.ColorToID[color] = id
	}
	return result, nil
}

func readPNGMetadata(path string) (pngMetadata, error) {
	var meta pngMetadata
	f, err := os.Open(path)
	if err != nil {
		return meta, err
	}
	defer f.Close()
	signature := make([]byte, 8)
	if _, err := io.ReadFull(f, signature); err != nil || string(signature) != "\x89PNG\r\n\x1a\n" {
		return meta, fmt.Errorf("not a PNG file")
	}
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(f, header); err != nil {
			return meta, err
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		chunkType := string(header[4:])
		if length < 0 || length > 1<<31 {
			return meta, fmt.Errorf("invalid PNG chunk length %d", length)
		}
		switch chunkType {
		case "IHDR":
			if length != 13 {
				return meta, fmt.Errorf("invalid IHDR length %d", length)
			}
			data := make([]byte, 13)
			if _, err := io.ReadFull(f, data); err != nil {
				return meta, err
			}
			meta.Width = int(binary.BigEndian.Uint32(data[:4]))
			meta.Height = int(binary.BigEndian.Uint32(data[4:8]))
			meta.BitDepth = data[8]
			meta.ColorType = data[9]
		case "PLTE":
			if length%3 != 0 || length > 768 {
				return meta, fmt.Errorf("invalid PLTE length %d", length)
			}
			data := make([]byte, length)
			if _, err := io.ReadFull(f, data); err != nil {
				return meta, err
			}
			meta.Palette = make([][3]byte, len(data)/3)
			for i := range meta.Palette {
				copy(meta.Palette[i][:], data[i*3:i*3+3])
			}
		default:
			if _, err := io.CopyN(io.Discard, f, length); err != nil {
				return meta, err
			}
		}
		if _, err := io.CopyN(io.Discard, f, 4); err != nil { // CRC
			return meta, err
		}
		if chunkType == "IDAT" || chunkType == "IEND" {
			break
		}
	}
	if meta.Width <= 0 || meta.Height <= 0 {
		return meta, fmt.Errorf("missing PNG IHDR")
	}
	return meta, nil
}

func riverPaletteMismatches(palette [][3]byte, limit int) (int, []string) {
	canonical := canonicalCK3RiverPalette()
	indices := append([]int{}, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 254, 255)
	count := 0
	var samples []string
	for _, index := range indices {
		if index >= len(palette) {
			count++
			if len(samples) < limit {
				samples = append(samples, fmt.Sprintf("index %d missing from %d-entry palette", index, len(palette)))
			}
			continue
		}
		if palette[index] != canonical[index] {
			count++
			if len(samples) < limit {
				samples = append(samples, fmt.Sprintf("index %d is #%02X%02X%02X, expected #%02X%02X%02X", index, palette[index][0], palette[index][1], palette[index][2], canonical[index][0], canonical[index][1], canonical[index][2]))
			}
		}
	}
	return count, samples
}

func canonicalCK3RiverPalette() [256][3]byte {
	var palette [256][3]byte
	for i := range palette {
		palette[i] = [3]byte{2, 0, 1}
	}
	values := map[int][3]byte{
		0: {0, 255, 0}, 1: {255, 0, 0}, 2: {255, 252, 0},
		3: {0, 225, 255}, 4: {0, 200, 255}, 5: {0, 150, 255}, 6: {0, 100, 255},
		7: {0, 0, 255}, 8: {0, 0, 225}, 9: {0, 0, 200}, 10: {0, 0, 150}, 11: {0, 0, 100},
		12: {0, 85, 0}, 13: {0, 125, 0}, 14: {0, 158, 0}, 15: {24, 206, 0},
		254: {255, 0, 128}, 255: {255, 255, 255},
	}
	for index, value := range values {
		palette[index] = value
	}
	return palette
}

func colorCountSamples(colors map[uint32]int, limit int) []string {
	keys := make([]int, 0, len(colors))
	for color := range colors {
		keys = append(keys, int(color))
	}
	sort.Ints(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, fmt.Sprintf("#%06X (%d pixels)", key, colors[uint32(key)]))
	}
	return result
}

func intSamples(values []int, limit int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strconv.Itoa(value)
	}
	return result
}

func addMapAuditFinding(result *MapAssetAuditResult, finding MapAssetAuditFinding) {
	result.Findings = append(result.Findings, finding)
}
