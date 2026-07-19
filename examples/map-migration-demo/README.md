# CK3 地图迁移可运行案例

这个目录由真实迁移核心生成，不是手工拼出的展示 JSON。

## 场景

- 旧上游使用省份 1 和 2。
- 当前项目修改了省份 1 的文化、地形和根描述文件，并新增一个 modifier 文件。
- 新上游把相同几何重编号为省份 10 和 20，同时新增自己的 modifier 文件。
- 迁移器以新上游为底，只重放项目拥有的改动。

快照：map-snapshot-66b79eab9ba94c36

产物：ck3-map-migration-73cc276a7141ef7d

## 查看结果

打开 artifacts/ck3-map-migration-73cc276a7141ef7d/demo_migrated，并与 project、new_upstream 对比：

- history/provinces/demo.txt 保留 project_custom_culture，但记录键已变成省份 10。
- common/province_terrain/demo.txt 保留 hills，但数字键已变成省份 10。
- common/landed_titles/demo.txt 保持新上游原生的 10/20，不会被二次改写。
- project_added.txt 和 new_upstream_added.txt 都存在。
- map_data 与目标上游地图资产逐字节相同。
- descriptor.mod 采用项目版本。

本次实际结果记录了 4 次语义 ID 替换和 4 个项目改动文件。产物目录还包含 migration-report.json、migration-manifest.json 与 resolution-template.json。

## 从仓库根目录重新生成

当前快照和产物是一轮真实成功运行的证据。若要重新生成，先把本目录移到别处，再执行：

    go run ./tools/generate_map_migration_demo.go

这个微型案例验证迁移、来源追踪和地图审计，但不会假装自己是完整可玩的 CK3 世界。真实工程迁移后仍应重新扫描，并放进隔离 playset 启动测试，再考虑打包。
