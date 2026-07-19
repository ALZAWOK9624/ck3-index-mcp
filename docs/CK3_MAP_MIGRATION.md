# CK3 地图自动迁移

地图迁移分为两个有意隔离的阶段：更新上游前保存持久快照，更新后以新上游为底构建完整本地 Fork。两个阶段都只接受 `ck3-index.toml` 中已经配置的来源名；MCP 不能提交任意文件路径，也不会修改项目或上游目录。

持久快照使用配置项 `migration_snapshot_root`（默认 `cache/migration-snapshots`）；迁移报告和成功 Fork 使用现有 `artifact_root` 与保留期限。两个存储目录以及 CLI 的 `--out` 都不得与任何配置来源目录重叠。

## 1. 更新前保存快照

```json
{
  "project": "project",
  "base": "godherja"
}
```

```text
ck3-index map migration-snapshot examples/map-migration-snapshot.json
```

快照按内容寻址并长期保存：它记录旧上游和当前项目的排序文件清单、SHA-256、双方重叠文本的旧版本，以及当时实际生效的 `provinces.png`、`definition.csv` 和 `default.map`。重复输入复用相同 `snapshot_id`。版本库、缓存、日志、备份、工具输出和旧压缩包不会进入快照。

## 2. 更新后迁移

```json
{
  "snapshot_id": "map-snapshot-0123456789abcdef",
  "target": "godherja",
  "output_name": "my_submod_migrated",
  "control_points": [],
  "resolutions": [],
  "delete_paths": []
}
```

```text
ck3-index map migrate examples/map-migration.json
ck3-index map migrate examples/map-migration.json --out ../my_submod_migrated
```

`--out` 仅供本地 CLI 使用，而且目录必须不存在。MCP 始终只写配置的 artifact 区。完整 Fork 先复制新上游，再按旧上游/快照项目/新上游进行三方合并；新上游的省份图、高度图、河流、拓扑及派生地图资源始终是几何权威。

只改写能从文件来源和语法上下文证明是旧省份引用的内容，包括省份历史、男爵领 `province`、省份地形、`default.map` 集合、`adjacencies.csv`、区域/岛屿/气候集合和 `province:<id>`。注释、字符串、日期、权重、角色 ID 与普通数字保持原样。

## 自动应用边界

- 标量必须是孤立一对一映射，覆盖率至少 `0.95`、置信度至少 `0.85`，并保持水陆类型。
- 拆分只能在集合、地形和省份历史等允许展开的结构中复制；男爵领和普通标量会阻止迁移。
- 合并只会在集合去重，或重复的历史/地形内容完全相同时自动消解。
- `complex`、`unmapped`、低置信度、水陆冲突、不同历史合并和双方同时修改的普通二进制文件都需要人工 resolution。
- 不创建占位男爵领，不推断新伯爵领归属，也不把新上游原生 ID 再按旧映射改写。

## 冲突与复跑

阻塞结果只包含 `migration-report.json`、`migration-manifest.json` 和 `resolution-template.json`，不包含可运行的 Mod 目录。复制模板中的稳定 `conflict_id`，选择 `select_target`、`expand`、`prefer_project`、`prefer_target` 或 `drop` 后复跑。跨水陆选择必须同时提交 `allow_type_change: true`。

全部改写文本会重新解析并经过 preflight；随后检查目标 ID、男爵领唯一性、历史和地形重复、区域/default.map/adjacency 引用及地图资产。只有没有 blocker 时才原子发布 Fork。它仍是本地测试产物，应重新扫描并在隔离 playset 中启动验证；不会自动打包或公开。
