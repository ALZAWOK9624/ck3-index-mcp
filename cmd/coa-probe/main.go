// coa-probe prints channel statistics for coat of arms textures, so a design
// can tell an emblem whose shape lives in its alpha from one that is opaque
// everywhere and carries its shape in a colour channel.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ck3-index/internal/coatofarms"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: coa-probe <gfx/coat_of_arms> <texture.dds>...")
		os.Exit(2)
	}
	base := os.Args[1]
	roots := []string{
		filepath.Join(base, "patterns"),
		filepath.Join(base, "colored_emblems"),
		filepath.Join(base, "textured_emblems"),
	}
	fmt.Printf("%-40s %6s %6s %6s %6s %8s\n", "texture", "meanR", "meanG", "meanB", "meanA", "opaque%")
	for _, name := range os.Args[2:] {
		var data []byte
		var err error
		for _, root := range roots {
			data, err = os.ReadFile(filepath.Join(root, name))
			if err == nil {
				break
			}
		}
		if err != nil {
			fmt.Printf("%-40s  not found\n", name)
			continue
		}
		img, err := coatofarms.DecodeDDS(data)
		if err != nil {
			fmt.Printf("%-40s  decode error: %v\n", name, err)
			continue
		}
		b := img.Bounds()
		var sr, sg, sb, sa, opaque float64
		n := float64(b.Dx() * b.Dy())
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := img.NRGBAAt(x, y)
				sr += float64(c.R)
				sg += float64(c.G)
				sb += float64(c.B)
				sa += float64(c.A)
				if c.A > 200 {
					opaque++
				}
			}
		}
		fmt.Printf("%-40s %6.1f %6.1f %6.1f %6.1f %7.1f%%\n",
			name, sr/n, sg/n, sb/n, sa/n, opaque/n*100)
	}
}
