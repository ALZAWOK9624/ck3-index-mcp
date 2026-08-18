// coa-preview renders coat of arms definitions to PNG so a design can be
// judged without launching the game.
//
//	coa-preview -defs <coa.txt> -colors <named_colors dir> -assets <gfx/coat_of_arms> -out <dir> [-size 160]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/coatofarms"
	"ck3-index/internal/script"
)

// assetDir loads and caches textures from the three asset folders. A name is
// looked up in each, because the definition only ever names the file.
type assetDir struct {
	roots []string
	cache map[string]*image.NRGBA
}

func newAssetDir(base string) *assetDir {
	return &assetDir{
		roots: []string{
			filepath.Join(base, "patterns"),
			filepath.Join(base, "colored_emblems"),
			filepath.Join(base, "textured_emblems"),
		},
		cache: map[string]*image.NRGBA{},
	}
}

func (a *assetDir) Texture(name string) (*image.NRGBA, error) {
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
			a.cache[name] = nil
			return nil, nil
		}
		a.cache[name] = img
		return img, nil
	}
	a.cache[name] = nil
	return nil, nil
}

func main() {
	defs := flag.String("defs", "", "coat of arms definition file")
	colors := flag.String("colors", "", "directory holding named_colors files")
	assets := flag.String("assets", "", "gfx/coat_of_arms directory")
	out := flag.String("out", "coa-out", "output directory")
	size := flag.Int("size", 160, "square edge in pixels")
	flag.Parse()
	if *defs == "" || *assets == "" {
		fmt.Fprintln(os.Stderr, "-defs and -assets are required")
		os.Exit(2)
	}

	palette := coatofarms.Palette{}
	if *colors != "" {
		entries, _ := os.ReadDir(*colors)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(*colors, name))
			if err != nil {
				continue
			}
			palette.ParseNamedColors(script.Parse(string(data)).Nodes)
		}
	}

	data, err := os.ReadFile(*defs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	parsed := coatofarms.Parse(script.Parse(string(data)).Nodes, palette)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	textures := newAssetDir(*assets)
	type report struct {
		ID       string   `json:"id"`
		File     string   `json:"file"`
		Emblems  int      `json:"emblems_drawn"`
		Missing  []string `json:"missing_textures,omitempty"`
		Warnings []string `json:"warnings,omitempty"`
	}
	var rows []report

	for _, def := range parsed {
		result, err := coatofarms.Render(def, textures, coatofarms.RenderOptions{Size: *size})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", def.ID, err)
			continue
		}
		file := def.ID + ".png"
		f, err := os.Create(filepath.Join(*out, file))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if err := png.Encode(f, result.Image); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		f.Close()
		rows = append(rows, report{
			ID: def.ID, File: file, Emblems: result.EmblemsDrawn,
			Missing: result.MissingTextures, Warnings: def.Warnings,
		})
	}

	index, _ := json.MarshalIndent(rows, "", "  ")
	os.WriteFile(filepath.Join(*out, "index.json"), index, 0o644)
	missing := 0
	for _, r := range rows {
		missing += len(r.Missing)
	}
	fmt.Printf("rendered %d definitions at %dpx -> %s (%d missing texture refs)\n",
		len(rows), *size, *out, missing)
}
