package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"ck3-index/internal/indexer"
	"ck3-index/internal/migrator"
)

const demoRoot = "examples/map-migration-demo"

func main() {
	if _, err := os.Stat(demoRoot); err == nil {
		fatalf("%s already exists; move it aside before regenerating", demoRoot)
	} else if !os.IsNotExist(err) {
		fatalf("inspect demo output: %v", err)
	}

	root, err := filepath.Abs(demoRoot)
	check(err)
	project := filepath.Join(root, "project")
	oldBase := filepath.Join(root, "old_base")
	newUpstream := filepath.Join(root, "new_upstream")
	artifacts := filepath.Join(root, "artifacts")
	snapshots := filepath.Join(root, "snapshots")

	writeOldBase(oldBase)
	writeProject(project)
	writeNewUpstream(newUpstream)
	writeText(filepath.Join(root, "ck3-index.toml"), demoConfig)
	writeJSON(filepath.Join(root, "snapshot-spec.json"), migrator.SnapshotSpec{Project: "project", Base: "old_base"})

	cfg := indexer.Config{
		ConfigPath:             filepath.Join(root, "ck3-index.toml"),
		Database:               filepath.Join(root, "cache", "demo.sqlite"),
		ArtifactRoot:           artifacts,
		MigrationSnapshotRoot:  snapshots,
		ArtifactRetentionHours: 24 * 7,
		Sources: []indexer.Source{
			{Name: "project", Path: project, Rank: 1},
			{Name: "old_base", Path: oldBase, Rank: 2},
			{Name: "new_upstream", Path: newUpstream, Rank: 3},
		},
	}

	snapshot, err := migrator.CreateSnapshot(context.Background(), cfg, migrator.SnapshotSpec{Project: "project", Base: "old_base"})
	check(err)
	writeJSON(filepath.Join(root, "snapshot-result.json"), snapshot)

	spec := migrator.MigrationSpec{
		SnapshotID: snapshot.SnapshotID,
		Target:     "new_upstream",
		OutputName: "demo_migrated",
	}
	writeJSON(filepath.Join(root, "migration-spec.json"), spec)

	result, err := migrator.BuildMigration(context.Background(), cfg, spec, migrator.BuildOptions{
		ArtifactRoot: artifacts,
		Retention:    7 * 24 * time.Hour,
	})
	check(err)
	writeJSON(filepath.Join(root, "migration-result.json"), result)
	if result.Status != "ready" {
		fatalf("demo migration was blocked; inspect %s", filepath.Join(root, "migration-result.json"))
	}

	readme := fmt.Sprintf(demoReadme, snapshot.SnapshotID, result.ArtifactID, result.ArtifactID, result.ReplacementCount, result.ChangedFiles)
	writeText(filepath.Join(root, "README.md"), readme)
	fmt.Printf("generated %s\nsnapshot: %s\nartifact: %s\n", demoRoot, snapshot.SnapshotID, result.ArtifactID)
}

func writeOldBase(root string) {
	writeMap(root, []int{1, 1, 2, 2}, map[int]color.RGBA{
		1: {R: 210, G: 70, B: 70, A: 255},
		2: {R: 70, G: 160, B: 90, A: 255},
	})
	writeText(filepath.Join(root, "descriptor.mod"), "name=\"Demo Upstream (Old)\"\nsupported_version=\"1.16.*\"\n")
	writeText(filepath.Join(root, "history", "provinces", "demo.txt"), `1 = {
  culture = old_culture
  religion = old_faith
}
2 = {
  culture = old_culture
  religion = old_faith
}
`)
	writeText(filepath.Join(root, "common", "landed_titles", "demo.txt"), `e_demo = {
  k_demo = {
    d_demo = {
      c_demo = {
        b_demo_one = { province = 1 }
        b_demo_two = { province = 2 }
      }
    }
  }
}
`)
	writeText(filepath.Join(root, "common", "province_terrain", "demo.txt"), "1 = plains\n2 = forest\n")
}

func writeProject(root string) {
	writeText(filepath.Join(root, "descriptor.mod"), "name=\"Demo Project\"\nsupported_version=\"1.16.*\"\n")
	writeText(filepath.Join(root, "history", "provinces", "demo.txt"), `1 = {
  culture = project_custom_culture
  religion = old_faith
}
2 = {
  culture = old_culture
  religion = old_faith
}
`)
	writeText(filepath.Join(root, "common", "province_terrain", "demo.txt"), "1 = hills\n2 = forest\n")
	writeText(filepath.Join(root, "common", "scripted_modifiers", "project_added.txt"), `demo_project_modifier = {
  monthly_prestige = 0.1
}
`)
}

func writeNewUpstream(root string) {
	writeMap(root, []int{10, 10, 20, 20}, map[int]color.RGBA{
		10: {R: 65, G: 115, B: 220, A: 255},
		20: {R: 225, G: 170, B: 65, A: 255},
	})
	writeText(filepath.Join(root, "descriptor.mod"), "name=\"Demo Upstream (New)\"\nsupported_version=\"1.17.*\"\n")
	writeText(filepath.Join(root, "history", "provinces", "demo.txt"), `10 = {
  culture = old_culture
  religion = old_faith
}
20 = {
  culture = old_culture
  religion = old_faith
}
`)
	writeText(filepath.Join(root, "common", "landed_titles", "demo.txt"), `e_demo = {
  k_demo = {
    d_demo = {
      c_demo = {
        b_demo_one = { province = 10 }
        b_demo_two = { province = 20 }
      }
    }
  }
}
`)
	writeText(filepath.Join(root, "common", "province_terrain", "demo.txt"), "10 = plains\n20 = forest\n")
	writeText(filepath.Join(root, "common", "scripted_modifiers", "new_upstream_added.txt"), `demo_new_upstream_modifier = {
  monthly_piety = 0.1
}
`)
}

func writeMap(root string, pixels []int, colors map[int]color.RGBA) {
	if len(pixels) != 4 {
		fatalf("demo map requires exactly four pixels")
	}
	mapDir := filepath.Join(root, "map_data")
	check(os.MkdirAll(mapDir, 0o755))
	provinceImage := image.NewRGBA(image.Rect(0, 0, 2, 2))
	definition := "0;0;0;0;x;x\n"
	seen := map[int]bool{}
	for index, id := range pixels {
		provinceImage.SetRGBA(index%2, index/2, colors[id])
		if seen[id] {
			continue
		}
		seen[id] = true
		provinceColor := colors[id]
		definition += strconv.Itoa(id) + ";" + strconv.Itoa(int(provinceColor.R)) + ";" + strconv.Itoa(int(provinceColor.G)) + ";" + strconv.Itoa(int(provinceColor.B)) + ";demo;x\n"
	}
	file, err := os.Create(filepath.Join(mapDir, "provinces.png"))
	check(err)
	check(png.Encode(file, provinceImage))
	check(file.Close())
	writeText(filepath.Join(mapDir, "definition.csv"), definition)
	writeText(filepath.Join(mapDir, "default.map"), "definitions = \"definition.csv\"\nprovinces = \"provinces.png\"\nsea_zones = LIST { }\nlakes = LIST { }\n")
}

func writeText(path, content string) {
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	check(os.WriteFile(path, []byte(content), 0o644))
}

func writeJSON(path string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	check(err)
	data = append(data, '\n')
	writeText(path, string(data))
}

func check(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "generate map migration demo: "+format+"\n", args...)
	os.Exit(1)
}

const demoConfig = `database = "cache/demo.sqlite"
artifact_root = "artifacts"
migration_snapshot_root = "snapshots"
artifact_retention_hours = 168

[[source]]
name = "project"
path = "project"
rank = 1

[[source]]
name = "old_base"
path = "old_base"
rank = 2

[[source]]
name = "new_upstream"
path = "new_upstream"
rank = 3
`

const demoReadme = `# CK3 地图迁移可运行案例

这个目录由真实迁移核心生成，不是手工拼出的展示 JSON。

## 场景

- 旧上游使用省份 1 和 2。
- 当前项目修改了省份 1 的文化、地形和根描述文件，并新增一个 modifier 文件。
- 新上游把相同几何重编号为省份 10 和 20，同时新增自己的 modifier 文件。
- 迁移器以新上游为底，只重放项目拥有的改动。

快照：%s

产物：%s

## 查看结果

打开 artifacts/%s/demo_migrated，并与 project、new_upstream 对比：

- history/provinces/demo.txt 保留 project_custom_culture，但记录键已变成省份 10。
- common/province_terrain/demo.txt 保留 hills，但数字键已变成省份 10。
- common/landed_titles/demo.txt 保持新上游原生的 10/20，不会被二次改写。
- project_added.txt 和 new_upstream_added.txt 都存在。
- map_data 与目标上游地图资产逐字节相同。
- descriptor.mod 采用项目版本。

本次实际结果记录了 %d 次语义 ID 替换和 %d 个项目改动文件。产物目录还包含 migration-report.json、migration-manifest.json 与 resolution-template.json。

## 从仓库根目录重新生成

当前快照和产物是一轮真实成功运行的证据。若要重新生成，先把本目录移到别处，再执行：

    go run ./tools/generate_map_migration_demo.go

这个微型案例验证迁移、来源追踪和地图审计，但不会假装自己是完整可玩的 CK3 世界。真实工程迁移后仍应重新扫描，并放进隔离 playset 启动测试，再考虑打包。
`
