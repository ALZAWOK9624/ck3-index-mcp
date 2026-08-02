package indexer

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// Applying a split to provinces.png is deliberately separated from planning it.
// The plan is small and reviewable; the image is 8192x4096 and rewriting it is
// the one step that cannot be undone by discarding a text diff. Keeping them
// apart means a caller can inspect exactly which provinces would be minted
// before any pixels move.

// MapSplitImageChange summarises what recolouring did, so a caller can verify
// the write matched the plan without diffing two enormous images.
type MapSplitImageChange struct {
	SourcePath     string                  `json:"source_path"`
	OutputPath     string                  `json:"output_path"`
	Width          int                     `json:"width"`
	Height         int                     `json:"height"`
	RecoloredCount int                     `json:"recolored_pixel_count"`
	PerProvince    map[int]int             `json:"recolored_per_province"`
	Mismatches     []string                `json:"mismatches,omitempty"`
	MismatchCount  int                     `json:"mismatch_count,omitempty"`
	Retained       MapSplitPart            `json:"-"`
	NewProvinces   []MapSplitProvinceColor `json:"new_provinces"`
}

// MapSplitProvinceColor is the colour a new province claimed in the raster.
type MapSplitProvinceColor struct {
	ID int `json:"id"`
	R  int `json:"r"`
	G  int `json:"g"`
	B  int `json:"b"`
}

// ApplyProvinceSplitToImage writes a copy of provinces.png with the split parts
// recoloured. It never edits in place: the caller supplies a distinct output
// path and compares before replacing anything.
//
// Pixels are verified against the source colour before being changed. The
// geometry came from an indexed scan, so if the image on disk no longer matches
// it the index is stale — and recolouring anyway would corrupt whatever
// province now occupies those pixels.
func ApplyProvinceSplitToImage(sourcePath, outputPath string, result MapSplitResult, plan MapSplitPlan) (MapSplitImageChange, error) {
	return ApplyProvinceSplitToImageContext(context.Background(), sourcePath, outputPath, result, plan)
}

func ApplyProvinceSplitToImageContext(ctx context.Context, sourcePath, outputPath string, result MapSplitResult, plan MapSplitPlan) (MapSplitImageChange, error) {
	change := MapSplitImageChange{
		SourcePath: sourcePath, OutputPath: outputPath, PerProvince: map[int]int{},
	}
	if sourcePath == "" || outputPath == "" {
		return change, fmt.Errorf("both a source and a distinct output path are required")
	}
	if sameDatabasePath(sourcePath, outputPath) {
		return change, fmt.Errorf("refusing to overwrite %s in place; supply a separate output path", sourcePath)
	}
	if len(plan.Blockers) > 0 {
		return change, fmt.Errorf("refusing to recolour: the plan has %d unresolved blocker(s)", len(plan.Blockers))
	}
	if err := ctx.Err(); err != nil {
		return change, err
	}
	const mismatchSampleLimit = 32
	appendMismatch := func(message string) {
		change.MismatchCount++
		if len(change.Mismatches) < mismatchSampleLimit {
			change.Mismatches = append(change.Mismatches, message)
		}
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return change, err
	}
	decoded, err := png.Decode(file)
	file.Close()
	if err != nil {
		return change, fmt.Errorf("%s: %w", filepath.Base(sourcePath), err)
	}
	bounds := decoded.Bounds()
	change.Width, change.Height = bounds.Dx(), bounds.Dy()

	canvas := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if y&63 == 0 {
			if err := ctx.Err(); err != nil {
				return change, err
			}
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			canvas.Set(x, y, decoded.At(x, y))
		}
	}

	// The retained part keeps the original colour, so only the new provinces
	// need pixels rewritten. The expected colour comes from the plan, which got
	// it from the index — reading it off the canvas instead would make the
	// staleness check tautological, since a wholesale repaint would move the
	// reference along with the pixels it is meant to catch.
	sourceColor := color.RGBA{
		R: uint8(plan.SourceColor >> 16 & 0xFF),
		G: uint8(plan.SourceColor >> 8 & 0xFF),
		B: uint8(plan.SourceColor & 0xFF),
		A: 255,
	}
	sourceKnown := plan.SourceColor != 0
	if !sourceKnown {
		appendMismatch("the plan carries no indexed province colour, so pixels cannot be verified before being overwritten")
	}
	for _, province := range plan.NewProvinces {
		part := partByIndex(result, province.PartIndex)
		if part == nil {
			appendMismatch(fmt.Sprintf("plan references part %d, which the split result does not contain", province.PartIndex))
			continue
		}
		target := color.RGBA{R: uint8(province.R), G: uint8(province.G), B: uint8(province.B), A: 255}
		recolored := 0
		for _, run := range part.Runs {
			if err := ctx.Err(); err != nil {
				return change, err
			}
			y := int(run.Y)
			for x := int(run.X0); x <= int(run.X1); x++ {
				if !(image.Point{x, y}).In(bounds) {
					appendMismatch(fmt.Sprintf("part %d covers pixel (%d,%d) outside the image", province.PartIndex, x, y))
					continue
				}
				if sourceKnown {
					current := canvas.RGBAAt(x, y)
					if current.R != sourceColor.R || current.G != sourceColor.G || current.B != sourceColor.B {
						appendMismatch(fmt.Sprintf("pixel (%d,%d) is %02x%02x%02x, not the indexed province colour %02x%02x%02x; the index is stale",
							x, y, current.R, current.G, current.B, sourceColor.R, sourceColor.G, sourceColor.B))
						continue
					}
				}
				canvas.SetRGBA(x, y, target)
				recolored++
			}
		}
		change.PerProvince[province.ID] = recolored
		change.RecoloredCount += recolored
		change.NewProvinces = append(change.NewProvinces, MapSplitProvinceColor{
			ID: province.ID, R: province.R, G: province.G, B: province.B,
		})
	}

	if change.MismatchCount > 0 {
		return change, fmt.Errorf("refusing to write: %d pixel(s) did not match the indexed geometry, so the index is stale relative to %s",
			change.MismatchCount, filepath.Base(sourcePath))
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return change, err
	}
	out, err := os.CreateTemp(filepath.Dir(outputPath), ".provinces-split-*.png")
	if err != nil {
		return change, err
	}
	tempPath := out.Name()
	defer os.Remove(tempPath)
	// CK3 reads provinces.png by exact RGB, so the encoder must not quantise or
	// dither; the default PNG encoder is lossless, but be explicit about it.
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(contextCheckingWriter{ctx: ctx, writer: out}, canvas); err != nil {
		out.Close()
		return change, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return change, err
	}
	if err := out.Close(); err != nil {
		return change, err
	}
	if err := ctx.Err(); err != nil {
		return change, err
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return change, fmt.Errorf("refusing to replace existing split output %s", filepath.Base(outputPath))
	} else if !os.IsNotExist(err) {
		return change, err
	}
	// Atomic rename is the publication boundary. No cancellation check follows:
	// once this succeeds the caller must receive a committed response.
	if err := os.Rename(tempPath, outputPath); err != nil {
		return change, err
	}
	return change, nil
}

func partByIndex(result MapSplitResult, index int) *MapSplitPart {
	for i := range result.Parts {
		if result.Parts[i].Index == index {
			return &result.Parts[i]
		}
	}
	return nil
}
