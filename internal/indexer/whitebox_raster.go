package indexer

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const whiteboxNoData = float32(-32768)

type whiteboxRaster struct {
	Rows, Cols int
	NoData     float64
	Values     []float64
}

func applyWhiteboxHydrology(ctx context.Context, cfg Config, cacheKey string, heightmap image.Image, fillRuns map[int][]MapRun, provinces map[int]*mapProvinceBuild, stats map[int]*mapGISProvinceStats) error {
	if cfg.GISAnalysis != "full" {
		return nil
	}
	cacheName := cacheKey
	if len(cacheName) > 32 {
		cacheName = cacheName[:32]
	}
	cacheDir := filepath.Join(cfg.GISCacheRoot, cacheName)
	if err := ensureContainedGISDir(cfg.GISCacheRoot, cacheDir); err != nil {
		return err
	}
	if err := pruneGISCache(cfg.GISCacheRoot, int64(cfg.GISCacheMaxGiB)<<30, cacheDir); err != nil {
		return err
	}
	b := heightmap.Bounds()
	values := make([]float32, b.Dx()*b.Dy())
	for i := range values {
		values[i] = whiteboxNoData
	}
	for id, runs := range fillRuns {
		province := provinces[id]
		if province == nil || province.WaterKind == "sea" || province.WaterKind == "coastal_sea" || province.WaterKind == "impassable_sea" || province.WaterKind == "lake" {
			continue
		}
		for _, run := range runs {
			y := int(run.Y) - b.Min.Y
			if y < 0 || y >= b.Dy() {
				continue
			}
			for x := int(run.X0); x <= int(run.X1); x++ {
				localX := x - b.Min.X
				if localX >= 0 && localX < b.Dx() {
					values[y*b.Dx()+localX] = float32(heightSample(heightmap, x, int(run.Y)))
				}
			}
		}
	}
	input := filepath.Join(cacheDir, "land_dem.dep")
	if _, err := os.Stat(input); os.IsNotExist(err) {
		if err := writeWhiteboxRaster(input, b.Dx(), b.Dy(), values, whiteboxNoData); err != nil {
			return err
		}
	}
	breached := filepath.Join(cacheDir, "breached.dep")
	pointer := filepath.Join(cacheDir, "d8_pointer.dep")
	flow := filepath.Join(cacheDir, "flow_cells.dep")
	if _, err := os.Stat(breached); os.IsNotExist(err) {
		if _, err := runWhiteboxTool(ctx, cfg, cacheDir, "BreachDepressions", []string{"--dem=" + filepath.Base(input), "--output=" + filepath.Base(breached)}); err != nil {
			return fmt.Errorf("BreachDepressions: %w", err)
		}
	}
	if _, err := os.Stat(pointer); os.IsNotExist(err) {
		if _, err := runWhiteboxTool(ctx, cfg, cacheDir, "D8Pointer", []string{"--dem=" + filepath.Base(breached), "--output=" + filepath.Base(pointer)}); err != nil {
			return fmt.Errorf("D8Pointer: %w", err)
		}
	}
	if _, err := os.Stat(flow); os.IsNotExist(err) {
		if _, err := runWhiteboxTool(ctx, cfg, cacheDir, "D8FlowAccumulation", []string{"--input=" + filepath.Base(pointer), "--output=" + filepath.Base(flow), "--out_type=cells"}); err != nil {
			return fmt.Errorf("D8FlowAccumulation: %w", err)
		}
	}
	raster, err := readWhiteboxRaster(flow)
	if err != nil {
		return err
	}
	if raster.Cols != b.Dx() || raster.Rows != b.Dy() {
		return fmt.Errorf("WhiteboxTools flow raster dimensions changed")
	}
	maxima := map[int]float64{}
	for id, runs := range fillRuns {
		province := provinces[id]
		if province == nil || province.BlockKind == "water" && province.WaterKind != "river" {
			continue
		}
		maximum := 0.0
		for _, run := range runs {
			y := int(run.Y) - b.Min.Y
			for x := int(run.X0); x <= int(run.X1); x++ {
				index := y*raster.Cols + x - b.Min.X
				if index < 0 || index >= len(raster.Values) {
					continue
				}
				value := raster.Values[index]
				if value != raster.NoData && !math.IsNaN(value) && !math.IsInf(value, 0) {
					maximum = math.Max(maximum, value)
				}
			}
		}
		if maximum > 0 {
			maxima[id] = maximum
		}
	}
	ordered := make([]float64, 0, len(maxima))
	for _, value := range maxima {
		ordered = append(ordered, value)
	}
	sortFloat64s(ordered)
	for id, value := range maxima {
		stat := stats[id]
		if stat == nil {
			continue
		}
		statCatchment := value
		stat.Confidence = math.Max(stat.Confidence, 0.93)
		percentile := percentileRank(ordered, value)
		stat.CatchmentPixels = &statCatchment
		stat.FlowPercentile = &percentile
		if stat.RiverPixels > 0 || stat.MajorRiver {
			// This is a bounded province-scale order proxy, not a claim of measured
			// discharge or a surveyed Strahler order. It remains deterministic and
			// monotonic with the verified D8 catchment percentile.
			order := 1 + int(math.Floor(math.Min(0.999999, math.Max(0, percentile))*5))
			stat.RiverOrder = &order
		}
	}
	return nil
}

func ensureContainedGISDir(root, candidate string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("GIS cache path escapes configured root")
	}
	return ensureSafeGISCacheDirectory(rootAbs, candidateAbs)
}

type gisCacheEntry struct {
	Path    string
	Size    int64
	ModTime int64
}

func pruneGISCache(root string, maxBytes int64, preserve string) error {
	if maxBytes <= 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var caches []gisCacheEntry
	total := int64(0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isHexCacheName(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		size := int64(0)
		_ = filepath.WalkDir(path, func(_ string, item os.DirEntry, walkErr error) error {
			if walkErr != nil || item.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if !item.IsDir() {
				if itemInfo, err := item.Info(); err == nil {
					size += itemInfo.Size()
				}
			}
			return nil
		})
		caches = append(caches, gisCacheEntry{Path: path, Size: size, ModTime: info.ModTime().UnixNano()})
		total += size
	}
	sort.Slice(caches, func(i, j int) bool { return caches[i].ModTime < caches[j].ModTime })
	preserveAbs, _ := filepath.Abs(preserve)
	for _, entry := range caches {
		if total <= maxBytes {
			break
		}
		entryAbs, _ := filepath.Abs(entry.Path)
		if filepath.Clean(entryAbs) == filepath.Clean(preserveAbs) {
			continue
		}
		if err := ensureGISRemovalContained(root, entryAbs); err != nil {
			return err
		}
		if err := os.RemoveAll(entryAbs); err != nil {
			return err
		}
		total -= entry.Size
	}
	return nil
}

func ensureGISRemovalContained(root, candidate string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing unsafe GIS cache cleanup")
	}
	return nil
}

func isHexCacheName(name string) bool {
	if len(name) < 16 || len(name) > 64 {
		return false
	}
	for _, r := range strings.ToLower(name) {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func writeWhiteboxRaster(depPath string, cols, rows int, values []float32, noData float32) error {
	if cols <= 0 || rows <= 0 || len(values) != cols*rows {
		return fmt.Errorf("invalid Whitebox raster dimensions")
	}
	min, max := math.Inf(1), math.Inf(-1)
	for _, raw := range values {
		value := float64(raw)
		if raw == noData || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		min, max = math.Min(min, value), math.Max(max, value)
	}
	if math.IsInf(min, 1) {
		return fmt.Errorf("Whitebox raster contains no data cells")
	}
	header := fmt.Sprintf("Min:\t%.9g\nMax:\t%.9g\nNorth:\t%d\nSouth:\t0\nEast:\t%d\nWest:\t0\nCols:\t%d\nRows:\t%d\nData Type:\tFLOAT\nData Scale:\tcontinuous\nDisplay Min:\t%.9g\nDisplay Max:\t%.9g\nPreferred Palette:\tspectrum.plt\nNoData:\t%.9g\nByte Order:\tLITTLE_ENDIAN\n", min, max, rows, cols, cols, rows, min, max, noData)
	if err := os.WriteFile(depPath, []byte(header), 0644); err != nil {
		return err
	}
	tasPath := strings.TrimSuffix(depPath, filepath.Ext(depPath)) + ".tas"
	f, err := os.Create(tasPath)
	if err != nil {
		return err
	}
	err = binary.Write(f, binary.LittleEndian, values)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func readWhiteboxRaster(depPath string) (whiteboxRaster, error) {
	f, err := os.Open(depPath)
	if err != nil {
		return whiteboxRaster{}, err
	}
	fields := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), ":")
		if ok {
			fields[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	closeErr := f.Close()
	if scanner.Err() != nil {
		return whiteboxRaster{}, scanner.Err()
	}
	if closeErr != nil {
		return whiteboxRaster{}, closeErr
	}
	cols, err1 := strconv.Atoi(fields["cols"])
	rows, err2 := strconv.Atoi(fields["rows"])
	noData, err3 := strconv.ParseFloat(fields["nodata"], 64)
	if err1 != nil || err2 != nil || err3 != nil || cols <= 0 || rows <= 0 || int64(cols)*int64(rows) > 40_000_000 {
		return whiteboxRaster{}, fmt.Errorf("invalid Whitebox raster header")
	}
	if !strings.Contains(strings.ToLower(fields["byte order"]), "little") {
		return whiteboxRaster{}, fmt.Errorf("unsupported Whitebox raster byte order")
	}
	typeName := strings.ToLower(fields["data type"])
	bytesPerCell := 0
	switch {
	case strings.Contains(typeName, "double") || strings.Contains(typeName, "float64") || strings.Contains(typeName, "f64"):
		bytesPerCell = 8
	case strings.Contains(typeName, "float") || strings.Contains(typeName, "f32"):
		bytesPerCell = 4
	default:
		return whiteboxRaster{}, fmt.Errorf("unsupported Whitebox raster data type %q", fields["data type"])
	}
	tasPath := strings.TrimSuffix(depPath, filepath.Ext(depPath)) + ".tas"
	info, err := os.Lstat(tasPath)
	if err != nil {
		return whiteboxRaster{}, err
	}
	if !info.Mode().IsRegular() {
		return whiteboxRaster{}, fmt.Errorf("Whitebox raster data is not a regular file")
	}
	count := cols * rows
	expectedBytes := int64(count) * int64(bytesPerCell)
	if info.Size() != expectedBytes {
		return whiteboxRaster{}, fmt.Errorf("Whitebox raster size mismatch: got %d bytes, want %d", info.Size(), expectedBytes)
	}
	data, err := os.Open(tasPath)
	if err != nil {
		return whiteboxRaster{}, err
	}
	values := make([]float64, count)
	const chunkCells = 16 * 1024
	buffer := make([]byte, chunkCells*bytesPerCell)
	for offset := 0; offset < count; {
		cells := min(chunkCells, count-offset)
		chunk := buffer[:cells*bytesPerCell]
		if _, err := io.ReadFull(data, chunk); err != nil {
			_ = data.Close()
			return whiteboxRaster{}, fmt.Errorf("read Whitebox raster data: %w", err)
		}
		for i := 0; i < cells; i++ {
			if bytesPerCell == 8 {
				values[offset+i] = math.Float64frombits(binary.LittleEndian.Uint64(chunk[i*8:]))
			} else {
				values[offset+i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(chunk[i*4:])))
			}
		}
		offset += cells
	}
	var trailing [1]byte
	if n, err := data.Read(trailing[:]); n != 0 || err != io.EOF {
		_ = data.Close()
		return whiteboxRaster{}, fmt.Errorf("Whitebox raster data changed while it was being read")
	}
	if err := data.Close(); err != nil {
		return whiteboxRaster{}, err
	}
	return whiteboxRaster{Rows: rows, Cols: cols, NoData: noData, Values: values}, nil
}

func percentileRank(sorted []float64, value float64) float64 {
	if len(sorted) <= 1 {
		return 1
	}
	index := 0
	for index < len(sorted) && sorted[index] <= value {
		index++
	}
	return float64(index-1) / float64(len(sorted)-1)
}

func sortFloat64s(values []float64) {
	for i := 1; i < len(values); i++ {
		value := values[i]
		j := i - 1
		for j >= 0 && values[j] > value {
			values[j+1] = values[j]
			j--
		}
		values[j+1] = value
	}
}
