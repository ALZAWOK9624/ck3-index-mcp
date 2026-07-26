package indexer

import (
	"context"
	"fmt"
	"image"
	"image/color"
)

// A basemap's bounding boxes are enough to place a label but not to answer
// "which duchy is the cursor over": real territories interleave, so their boxes
// overlap heavily. The hit map solves that the way interactive raster maps
// always have — a second plate, pixel-aligned with the first, where every entity
// owns one flat colour. A page draws it to an off-screen canvas and reads the
// pixel under the pointer to get an exact answer in constant time.
//
// Two properties are essential and easy to lose:
//
//   - No antialiasing, no blending, no supersample downscale. Every pixel must
//     hold an exact entity colour, so the plate is drawn at final resolution
//     with draw.Src only.
//   - Colours are indices, not aesthetics. Index 0 is reserved for "nothing
//     here", so entity colours start at 1.
func (db *DB) renderEntityHitMap(ctx context.Context, scratch *mapRenderScratch,
	metric MapMetricResult, v renderViewport, width, height int) (*image.RGBA, map[string]string, error) {
	if metric.Level == "" || len(metric.Values) == 0 {
		return nil, nil, nil
	}
	_, groups, err := db.mapMetricEntities(ctx, metric.Target, metric.Level)
	if err != nil {
		return nil, nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	// Index 0 stays as opaque black: "no entity".
	for i := 3; i < len(canvas.Pix); i += 4 {
		canvas.Pix[i] = 255
	}
	byEntity := make(map[string]string, len(metric.Values))
	index := 0
	for _, item := range metric.Values {
		pids := groups[item.ID]
		if len(pids) == 0 {
			continue
		}
		index++
		if index > 0xFFFFFF {
			return nil, nil, fmt.Errorf("hit map supports at most %d entities", 0xFFFFFF)
		}
		key := color.RGBA{uint8(index >> 16), uint8(index >> 8), uint8(index), 255}
		byEntity[item.ID] = rgbaHex(key)
		for _, pid := range pids {
			runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
			if err != nil {
				return nil, nil, err
			}
			// drawRuns copies the colour verbatim, which is exactly what a lookup
			// plate needs.
			drawRuns(canvas, v, runs, key)
		}
	}
	return canvas, byEntity, nil
}
