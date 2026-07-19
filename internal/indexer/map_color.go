package indexer

import (
	"image/color"
	"math"
	"sort"
)

type mapOKLab struct{ L, A, B float64 }
type mapOKLCH struct{ L, C, H float64 }

func srgbLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linearSRGB(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

func rgbaToOKLab(c color.RGBA) mapOKLab {
	r, g, b := srgbLinear(float64(c.R)/255), srgbLinear(float64(c.G)/255), srgbLinear(float64(c.B)/255)
	l := 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
	m := 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
	s := 0.0883024619*r + 0.2817188376*g + 0.6299787005*b
	l, m, s = math.Cbrt(l), math.Cbrt(m), math.Cbrt(s)
	return mapOKLab{
		L: 0.2104542553*l + 0.793617785*m - 0.0040720468*s,
		A: 1.9779984951*l - 2.428592205*m + 0.4505937099*s,
		B: 0.0259040371*l + 0.7827717662*m - 0.808675766*s,
	}
}

func okLabToRGBA(lab mapOKLab, alpha uint8) color.RGBA {
	l := lab.L + 0.3963377774*lab.A + 0.2158037573*lab.B
	m := lab.L - 0.1055613458*lab.A - 0.0638541728*lab.B
	s := lab.L - 0.0894841775*lab.A - 1.291485548*lab.B
	l, m, s = l*l*l, m*m*m, s*s*s
	r := +4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	b := -0.0041960863*l - 0.7034186147*m + 1.707614701*s
	toByte := func(v float64) uint8 { return uint8(math.Round(clampFloat(linearSRGB(v), 0, 1) * 255)) }
	return color.RGBA{R: toByte(r), G: toByte(g), B: toByte(b), A: alpha}
}

func okLabToLCH(lab mapOKLab) mapOKLCH {
	h := math.Atan2(lab.B, lab.A) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return mapOKLCH{L: lab.L, C: math.Hypot(lab.A, lab.B), H: h}
}

func okLCHToLab(lch mapOKLCH) mapOKLab {
	h := lch.H * math.Pi / 180
	return mapOKLab{L: lch.L, A: lch.C * math.Cos(h), B: lch.C * math.Sin(h)}
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func hueDelta(a, b float64) float64 {
	d := math.Mod(a-b+540, 360) - 180
	return d
}

func harmonizePoliticalColor(c color.RGBA) color.RGBA {
	anchor := okLabToLCH(rgbaToOKLab(c))
	adjusted := anchor
	adjusted.L = clampFloat(anchor.L, 0.42, 0.61)
	adjusted.L = clampFloat(adjusted.L, anchor.L-0.12, anchor.L+0.12)
	adjusted.C = clampFloat(anchor.C*0.62, 0.045, 0.13)
	return okLabToRGBA(okLCHToLab(adjusted), c.A)
}

func okLabDistance(a, b color.RGBA) float64 {
	x, y := rgbaToOKLab(a), rgbaToOKLab(b)
	return math.Sqrt((x.L-y.L)*(x.L-y.L) + (x.A-y.A)*(x.A-y.A) + (x.B-y.B)*(x.B-y.B))
}

func coordinatePoliticalColors(input map[string]color.RGBA, neighbors map[string]map[string]bool) map[string]color.RGBA {
	ids := make([]string, 0, len(input))
	anchors := map[string]mapOKLCH{}
	values := map[string]mapOKLCH{}
	alpha := map[string]uint8{}
	for id, c := range input {
		ids = append(ids, id)
		anchors[id] = okLabToLCH(rgbaToOKLab(c))
		values[id] = okLabToLCH(rgbaToOKLab(harmonizePoliticalColor(c)))
		alpha[id] = c.A
	}
	sort.Strings(ids)
	for round := 0; round < 28; round++ {
		changed := false
		for _, id := range ids {
			for neighbor := range neighbors[id] {
				if id >= neighbor || input[neighbor].A == 0 {
					continue
				}
				a, b := values[id], values[neighbor]
				if okLabDistance(okLabToRGBA(okLCHToLab(a), 255), okLabToRGBA(okLCHToLab(b), 255)) >= 0.075 {
					continue
				}
				// Deterministic opposing nudges remain inside each title's native anchor envelope.
				direction := 1.0
				if hueDelta(a.H, b.H) < 0 {
					direction = -1
				}
				// Keep a one-degree gamut-conversion margin inside the public +/-8 degree contract.
				a.H = anchors[id].H + clampFloat(hueDelta(a.H+direction*1.25, anchors[id].H), -7, 7)
				b.H = anchors[neighbor].H + clampFloat(hueDelta(b.H-direction*1.25, anchors[neighbor].H), -7, 7)
				if round%2 == 0 {
					a.L = clampFloat(a.L+0.006, math.Max(0.40, anchors[id].L-0.12), math.Min(0.68, anchors[id].L+0.12))
					b.L = clampFloat(b.L-0.006, math.Max(0.40, anchors[neighbor].L-0.12), math.Min(0.68, anchors[neighbor].L+0.12))
				}
				values[id], values[neighbor] = a, b
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	out := make(map[string]color.RGBA, len(input))
	for id, value := range values {
		out[id] = okLabToRGBA(okLCHToLab(value), alpha[id])
	}
	return out
}
