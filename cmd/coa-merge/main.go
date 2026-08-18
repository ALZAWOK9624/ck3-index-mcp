// coa-merge finds emblems that will visually merge: two layers whose drawn
// pixels actually touch and whose colours are too close to tell apart.
//
// A bounding-box test is not enough -- a thin cross and a charge in the corner
// share a full-canvas box but never touch. This walks the same paint order the
// renderer uses, keeps the topmost layer per pixel, and only reports pairs that
// are genuinely adjacent on screen.
package main

import (
	"flag"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/coatofarms"
	"ck3-index/internal/script"
)

type assets struct {
	roots []string
	cache map[string]*image.NRGBA
}

func (a *assets) Texture(name string) (*image.NRGBA, error) {
	if img, ok := a.cache[name]; ok {
		return img, nil
	}
	for _, root := range a.roots {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		img, err := coatofarms.DecodeDDS(data)
		if err != nil {
			break
		}
		a.cache[name] = img
		return img, nil
	}
	a.cache[name] = nil
	return nil, nil
}

func lin(c float64) float64 {
	c /= 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func lab(r, g, b uint8) [3]float64 {
	rr, gg, bb := lin(float64(r)), lin(float64(g)), lin(float64(b))
	x := rr*.4124 + gg*.3576 + bb*.1805
	y := rr*.2126 + gg*.7152 + bb*.0722
	z := rr*.0193 + gg*.1192 + bb*.9505
	f := func(t float64) float64 {
		if t > .008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116
	}
	fx, fy, fz := f(x/.95047), f(y), f(z/1.08883)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

func deltaE(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

// 整幅不透明、形状在绿通道里的贴图
var fullField = map[string]bool{
	"ce_waves_02.dds": true, "ce_fretty.dds": true, "ce_ermine.dds": true,
	"ce_chief.dds": true, "ce_checkers_diagonal_02.dds": true, "ce_checkers_08.dds": true,
	"ce_checkers_06.dds": true, "ce_bendy_06.dds": true, "ce_barry_undy.dds": true,
}

func main() {
	defs := flag.String("defs", "", "coat of arms definition file")
	colors := flag.String("colors", "", "named_colors directory")
	base := flag.String("assets", "", "gfx/coat_of_arms directory")
	size := flag.Int("size", 200, "render edge")
	limit := flag.Float64("threshold", 45, "delta-E below which two touching layers merge")
	flag.Parse()

	palette := coatofarms.Palette{}
	if entries, err := os.ReadDir(*colors); err == nil {
		var names []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".txt") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			data, err := os.ReadFile(filepath.Join(*colors, n))
			if err == nil {
				palette.ParseNamedColors(script.Parse(string(data)).Nodes)
			}
		}
	}
	data, err := os.ReadFile(*defs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tex := &assets{roots: []string{
		filepath.Join(*base, "patterns"),
		filepath.Join(*base, "colored_emblems"),
		filepath.Join(*base, "textured_emblems"),
	}, cache: map[string]*image.NRGBA{}}

	total := 0
	for _, def := range coatofarms.Parse(script.Parse(string(data)).Nodes, palette) {
		type layer struct {
			name  string
			rgba  [3]float64
			depth float64
			order int
			art   *image.NRGBA
			inst  coatofarms.Instance
		}
		var layers []layer
		for _, em := range def.Emblems {
			art, _ := tex.Texture(em.Texture)
			if art == nil {
				continue
			}
			cols := def.EffectiveColors(em)
			// 整幅不透明的分割贴图画出来的是 color2，color1 是被「关掉」的那半
			c := cols[0].RGBA
			if fullField[em.Texture] {
				c = cols[1].RGBA
			}
			insts := em.Instance
			if len(insts) == 0 {
				insts = []coatofarms.Instance{{Position: [2]float64{0.5, 0.5}, Scale: [2]float64{1, 1}}}
			}
			for _, in := range insts {
				layers = append(layers, layer{
					name: em.Texture, rgba: lab(c.R, c.G, c.B),
					depth: in.Depth, order: len(layers), art: art, inst: in,
				})
			}
		}
		sort.SliceStable(layers, func(i, j int) bool {
			if layers[i].depth != layers[j].depth {
				return layers[i].depth > layers[j].depth
			}
			return layers[i].order < layers[j].order
		})

		n := *size
		top := make([]int, n*n)
		for i := range top {
			top[i] = -1
		}
		for li, l := range layers {
			halfW := math.Abs(l.inst.Scale[0]) * float64(n) / 2
			halfH := math.Abs(l.inst.Scale[1]) * float64(n) / 2
			cx, cy := l.inst.Position[0]*float64(n), l.inst.Position[1]*float64(n)
			b := l.art.Bounds()
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					u := (float64(x)+0.5-cx)/(2*halfW) + 0.5
					v := (float64(y)+0.5-cy)/(2*halfH) + 0.5
					if u < 0 || u >= 1 || v < 0 || v >= 1 {
						continue
					}
					sx := b.Min.X + int(u*float64(b.Dx()))
					sy := b.Min.Y + int(v*float64(b.Dy()))
					if l.art.NRGBAAt(sx, sy).A > 128 {
						top[y*n+x] = li
					}
				}
			}
		}

		touch := map[[2]int]int{}
		for y := 0; y < n-1; y++ {
			for x := 0; x < n-1; x++ {
				a := top[y*n+x]
				for _, b := range []int{top[y*n+x+1], top[(y+1)*n+x]} {
					if a < 0 || b < 0 || a == b {
						continue
					}
					k := [2]int{a, b}
					if a > b {
						k = [2]int{b, a}
					}
					touch[k]++
				}
			}
		}
		for k, count := range touch {
			if float64(count) < float64(n)*0.15 {
				continue // 只是几个像素擦边，不算糊
			}
			d := deltaE(layers[k[0]].rgba, layers[k[1]].rgba)
			if d >= *limit {
				continue
			}
			fmt.Printf("%-24s %-32s %-32s ΔE=%5.1f 接触=%d px\n",
				def.ID, layers[k[0]].name, layers[k[1]].name, d, count)
			total++
		}
	}
	fmt.Printf("\n可见相邻且同色 %d 处\n", total)
}
