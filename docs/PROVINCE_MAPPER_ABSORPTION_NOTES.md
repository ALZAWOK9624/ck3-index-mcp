# ProvinceMapper 吸收记录

## 上游快照

- 仓库：`ParadoxGameConverters/provinceMapper`
- 审阅提交：`5d2f09cd121fa482f98f1d083cf6494b89cc3dfe`
- 提交日期：2026-07-03
- 许可证：MIT（Copyright 2024 The Paradox Game Converters Group）
- 重点文件：`Automapper.cpp`、`Triangle.cpp`、`ImageFrame.cpp`、`README.md`

上游仓库的 `.NO_AI/README.md` 明确拒绝 AI 生成的 Pull Request、Issue 和文档贡献。ck3-index 不会向该仓库提交任何内容；本次仅在 MIT 许可范围内研究公开算法思想，并在自己的 Go 地图模型上独立实现只读能力。

## 值得吸收的算法边界

ProvinceMapper 的核心不是 GUI 连线，而是三段数据流：

1. 用户在两张地图上提供对应控制点，并对源控制点建立 Delaunay 三角网。
2. 每个三角形内部使用重心坐标/仿射变换，把源地图像素投影到目标地图。
3. 按源省份与目标省份累计像素交叠，同时把陆地和水域分开匹配。

这套模型能处理单纯重编号，也能识别一个旧省份覆盖多个新省份、多个旧省份汇入一个新省份，以及地图整体缩放或局部形变。

## ck3-index 的独立实现

- 新增 `map_province_mapping` 与 `ck3-index map province-mapping <spec.json>`。
- 输入只接受 `ck3-index.toml` 中已配置的来源名或 `active`，不接受 MCP 客户端提供任意文件路径。
- 没有控制点时，使用两张地图四角建立规范化映射；地图形变时可提交至少三个控制点对。
- 使用自行实现的 Bowyer-Watson Delaunay 三角化、瓦片化三角形查找和重心坐标变换。
- 交叠边同时返回 `source_share` 与 `target_share`，并用连通分量分类 `one_to_one`、`renumbered`、`split`、`merge` 和 `complex`。
- 默认拒绝水陆交叉映射；被排除像素、控制点凸包外像素和目标图外像素分别计数。
- MCP 输出按 `limit` 截断证据行，但保留完整总数与摘要；`visibility=public` 会移除 rank 1 当前工程来源。

## 有意不吸收的部分

- 不引入 wxWidgets、ImageMagick、C++ 子模块或上游 GUI。
- 不复制上游多轮贪心匹配、链接“偷取”与自动写出逻辑；这些步骤会把不确定对应伪装成确定结果。
- 不自动改写 `definition.csv`、领地头衔、文化、信仰、省份历史或角色历史。
- 不把省份 ID 相同视为地理位置必然相同；所有建议必须有像素交叠证据。
- 不向上游创建 Pull Request、Issue、评论或其他通知。

## 验证边界

- 同几何但不同 ID 的地图返回 `renumbered`。
- 一对多与多对一分别返回 `split` 和 `merge`。
- 四控制点旋转映射可正确恢复省份对应。
- 默认水陆隔离会留下明确的未映射省份与 `type_mismatch_pixels`。
- 重复控制点、共线控制点、非法阈值和缺失来源会被拒绝。

## 当前工作区实测

使用实际配置中的 `godherja → project` 完整地图进行只读冷启动 CLI 对比：

- 两张地图均为 `8192×4096`，来源省份 8,652 个，目标省份 9,886 个。
- 比较 33,547,353 个有效像素；7,079 个像素因水陆类型不一致被隔离。
- 得到 8,569 个连通映射组：8,492 个同 ID 一对一、3 个重编号、58 个拆分、16 个复杂组；没有合并组。
- 来源省份全部获得映射证据，目标地图仍有 4 个省份未映射。
- 包含 `go run` 编译/启动开销的端到端冷运行约 26.77 秒；同尺寸地图使用直接坐标通道，不执行逐像素三角查找。

## 后续独立改写层

`map_province_mapping` 仍保持只读证据工具；自动迁移没有照搬 ProvinceMapper 的写出逻辑，而是在 `internal/migrator` 中独立实现。新增的 `map_migration_snapshot` 与 `map_province_migration` 把旧上游文本基线、当前项目哈希、目标几何权威、保留格式的语义改写、保守三方合并、稳定冲突 resolution 和严格验证串成一条流水线。失败只发布审查报告，成功才发布完整本地 Fork，原项目始终不变。详细契约见 `docs/CK3_MAP_MIGRATION.md`。
