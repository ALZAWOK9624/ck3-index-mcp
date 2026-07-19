# ck3-index

通用 CK3 Mod 索引器、依赖查询工具、验证器、成品打包器与 MCP 服务。除显式的成品打包工具只写配置的临时 artifact 区外，索引查询和模型审查路径保持只读；工作区由用户在配置中声明，可包含任意 CK3 Mod、游戏本体或其他合法参考源。

## 快速开始

```powershell
# 完整重建（首次使用或来源配置发生变化后）
ck3-index scan --clean

# 编辑 Mod 文件后的增量更新
ck3-index scan

# 不做全量扫描，只刷新当前工程中的一个或少量文件
ck3-index scan --files common/decisions/example.txt localization/english/example_l_english.yml

# 按诊断代码汇总
ck3-index diag_stats

# 准确性基准回归用例
ck3-index accuracy

# 热路径健康与性能检查
ck3-index health
ck3-index bench

# 完整验证与编译器检查
ck3-index validate

# 启动供模型调用的 MCP 服务
ck3-index mcp
```

## QQ 成品打包

`ck3-index package <spec.json>` 接收模型生成的元数据与文件列表；`ck3-index package-dir <mod-dir> --meta <metadata.json>` 打包已有工程目录。两个入口使用同一个严格打包核心，只有语法、作用域、引用、本地化、资源及覆盖风险闸门通过后才会发布 ZIP。

默认成品可以直接解压到 CK3 的 `mod` 目录：

```text
<slug>.mod
<slug>/descriptor.mod
<slug>/<Mod 内容>
INSTALL.txt
ck3-package-manifest.json
```

打包器会统一重建双描述文件，将 launcher 路径固定为 `path="mod/<slug>"`，为本地化补 UTF-8 BOM，并拒绝绝对路径、路径穿越、大小写碰撞、符号链接与冲突描述文件。ZIP 使用固定顺序与时间戳；清单不记录时间、本机路径或用户名，因此相同输入产生相同 SHA-256。完整输入契约、限制和示例见[成品打包器说明](docs/CK3_PACKAGING.md)。模型完成代码后必须调用 `ck3_package`，不得自行拼接描述文件或 ZIP。

## 工具正式版候选

当前工具正式版本为 `0.4.0`，采用 GPL-3.0-or-later。Windows x64 与 Linux x64 发布脚本会构建两次并比较二进制、校验固定 WhiteboxTools 压缩包与可执行文件哈希、启动真实 MCP 冒烟测试、收集实际链接 Go 模块的许可证，并生成不含本机路径的 `RELEASE_MANIFEST.json`、`SHA256SUMS` 与确定性 ZIP。

项目采用 GPL-3.0-or-later；完整命令、平台包结构、第三方 notices、可复现性边界和发布闸门见[发布流程](docs/RELEASE.md)。

## Public 0.4.0

The project is licensed under GPL-3.0-or-later; see [LICENSE](LICENSE). The public source tree excludes local indexes, caches, game data, and external research checkouts. Release artifacts are produced only after reproducibility and MCP smoke-test gates pass.

<!-- BEGIN GENERATED MCP TOOLS -->
## MCP 工具（标准模式：29；专家模式：57）

标准模式只公开下列规范工具。专家模式还会公开已弃用的兼容名称；在兼容期内，所有旧名称仍然可以调用。

### 核心工具

| 工具 | 用途 |
|---|---|
| `ck3_search` | 在不知道准确 CK3 标识符时进行搜索。返回按相关度排序的对象、本地化、资源、引用、诊断、数据类型与脚本键证据。 |
| `ck3_inspect` | 发现目标后，检查一个准确的 CK3 标识符、键或资源路径。定义视图包含覆盖来源、事件字段以及人物静态档案和日期时间线；引用视图保留关系、阶段、置信度和未解析原因；compare 可对准确类型化标识符做受限、只读的来源与上游对象级比较。 |
| `ck3_review` | 审查完整的拟议 CK3 文件；未提供文件时审查当前工程中的脏文件。执行只读的语法、作用域、引用、本地化与资源检查。 |
| `ck3_workspace` | 在选择具体对象前检查已索引的工作区结构。返回架构概览、对象类型分布，或 engine、Tiger 与原版相邻注释之间只读的 on_action 证据审计。 |
| `ck3_dependencies` | 追踪一个 CK3 标识符周围的语义依赖。neighborhood 返回通用邻域；event_chain 返回调用者、被调用者、根、叶、循环、最短链和未解析调用，并可附带自包含、无外部请求的交互 HTML 检查器。 |
| `ck3_prepare_edit` | 在生成 CK3 脚本前加载编辑证据。默认返回组合上下文，也可只请求示例、结构规则或经验模式。 |
| `ck3_preflight` | 对已索引目标、拟议完整文件或当前脏文件执行只读门禁。使用 operation 选择目标、补丁或脏文件模式。 |
| `ck3_impact` | 编辑前分析拟议的新增或更新、删除与重命名操作。返回只读依赖风险与未解析引用风险。 |
| `ck3_diagnostics` | 无需重新扫描即可检查已缓存的工程诊断。默认返回摘要；explain 可按诊断代码和可选来源字段筛选。 |
| `ck3_script_reference` | 查询一项本地引擎或脚本规则事实。通过 kind 选择作用域、数据类型、值形状、define、on_action、迭代器、示例或修正值。 |
| `ck3_health` | 检查数据库、结构、索引与 MCP 注册是否可信。返回隐藏路径后的简短健康报告。 |
| `ck3_package` | 严格验证模型生成的 CK3 文本与二进制文件，统一生成双描述文件，并在受限临时区创建可直接手动安装的 ZIP；不会安装或修改真实 Mod 目录。 |
| `ck3_gui` | 通过现有索引检查生效中的 CK3 GUI 文件，解析跨文件继承、模板和区块覆盖，并输出有界 PNG 或自包含 HTML。检查器支持控件树、裁剪滚动视口、网格布局、英中本地化切换、已索引动态纹理样例与受控行为模拟；model_samples 可从唯一 item 模板实例化有界调用方列表行，绝不执行任意 Jomini 代码。 |

### 地图工具

| 工具 | 用途 |
|---|---|
| `map_migration_snapshot` | 在上游地图更新前，保存已配置旧上游、当前 Mod 文件清单、文本基线与有效地图的内容寻址快照；不接受任意路径，也不修改来源工程。 |
| `map_province_migration` | 以新上游为底执行保守三方合并和省份语义改写，严格验证通过后才生成独立本地测试 Fork；冲突时只输出报告和 resolution 模板。 |
| `map_asset_audit` | 审计当前生效的 CK3 地图栅格，检查省份定义覆盖、PNG 编码、河流调色板索引语义和正交河道拓扑；吸收 AzgaarToCK3 的特色校验，但不重复 ck3-index 已有解析与几何。 |
| `map_province_mapping` | 通过控制点 Delaunay 分片仿射变换比较两个已配置的 CK3 省份地图，返回像素交叠、重编号、拆分、合并、复杂及未映射分组；只提供迁移证据，不写地图或历史文件。 |
| `map_province_info` | 检查一个省份的精确几何、头衔、脚本地形、实际地表材质混合、纹理资源与直接边界。返回只读的精确上下文和分类后的邻省。 |
| `map_physical_context` | 只读检查归一化高程、地形、gfx/map/terrain 地表材质混合及纹理资源、复合河流、水体、相对海床深浅与物理障碍；明确区分 CK3 原生观察事实、GIS 派生值和综合推断。 |
| `map_neighbors` | 检查某省份或领地头衔周围的受限图邻域。返回按半径分组的方向、距离与边界分类。 |
| `map_spatial_relation` | 比较两个省份的精确空间关系。返回质心偏移、方位角、距离、直接边界与附近障碍。 |
| `map_strategic_passages` | 把显式邻接与像素边界邻省分开检查。返回海峡、渡口、地下连接与离图通道。 |
| `map_title_context` | 检查一个领地头衔的省份覆盖、持有者、文化、信仰与相邻头衔。返回只读的历史和视觉上下文。 |
| `map_assignment_plan` | 生成仅供审查的宗教或占位角色分配建议。private 模式返回补丁预览，visibility=public 时会移除预览。 |
| `map_building_candidates` | 为省份或领地头衔排列可审计的特殊建筑候选地。返回地形、地产、水域、文化与边界证据，不写入文件。 |
| `map_recipe_catalog` | 列出支持的地图配方、层级、变换、图层、调色板与使用建议。构建自定义指标或渲染规格前应先调用此工具。 |
| `map_build_metric` | 在渲染前构建可审计的索引指标或带来源说明的地图指标。返回数值、分位数、异常值、来源与警告。 |
| `map_route` | 解析中英文地名、领地头衔或省份 ID，并在已索引省份拓扑上计算确定性的合法陆路、海路或混合路线；返回紧凑路径、分段、沿途上下文和像素距离警告。 |
| `map_render` | 渲染只读的自适应 CK3 地图；省略尺寸时自动选择分辨率。返回结构化元数据和内存中的 PNG，不接受客户端文件路径。 |

### 兼容模式

只有仍需发现旧版专用工具名的客户端才应设置 `CK3_INDEX_MCP_PROFILE=expert`。新提示词与 `next_queries` 一律使用规范工具名。
<!-- END GENERATED MCP TOOLS -->

## 架构

```text
ck3-index scan --clean
  - 按用户配置的优先级遍历 Mod、游戏本体与可选参考源
  - 按 rel_path 检测文件覆盖关系
  - 使用并发工作池完成解析、哈希与静态检查
  - 使用预编译语句和 WAL 模式串行写入数据库
  - 构建索引、执行验证并解析引用
  - SQLite 数据库：对象、引用、本地化、资源、诊断、引擎数据、粗粒度地图上下文与 FTS5

ck3-index mcp
  - 通过标准输入输出传输 JSON-RPC，支持 Content-Length 分帧
  - 协商 MCP 2025-11-25 与 2025-06-18；分别处理畸形 JSON 和无效 JSON-RPC 请求
  - 使用只读 SQLite 连接；数据库结构和索引维护只在 scan/health 路径执行
  - 提供只读查询、本地规则查询、内存补丁叠加检查和精简健康检查
  - `ck3_package`、`map_migration_snapshot` 与 `map_province_migration` 是受限写入工具：前者只发布 ZIP，后两者只写持久迁移快照或临时 artifact；三者都不修改真实 Mod 目录
  - 提供类似代码库记忆的架构概览缓存与依赖图工具
```

当前实现仍把核心代码保留在 `internal/indexer` 下，作为兼容门面。进一步拆包前，新的性能工作会优先围绕查询与索引健康、补丁叠加和模型热路径结果整形展开。

## 模型快速使用路径

- 使用 `ck3_search` 开始宽泛发现，用 `ck3_inspect` 调查单个标识符，用 `ck3_review` 审查代码；所有专用工具仍然可用。
- 查询 CK3 对象、引用、本地化、资源、诊断和地图上下文时，先使用 ck3-index，再进行原始文本搜索。`rg` 只用于检查索引返回的准确证据文件。
- 机器人会话开始或结果疑似过期时先调用 `ck3_health`；它会给出简短可信度信号，不会泄露私有路径。
- 生成代码前使用 `ck3_prepare_edit <type>`；它会返回示例、结构字段、经验模式与明确的 `guidance`。可用 `operation=examples|rules|patterns` 只选择一类证据。
- 宽泛调查开始时使用 `ck3_workspace`；它会概括已索引来源、对象类型分布、引用类型压力与诊断热点，不倾倒原始文件。扫描会写入缓存摘要；没有缓存时回退到实时聚合。
- 重命名、删除或编辑的影响面很重要时使用 `ck3_dependencies <id>`；默认 `operation=neighborhood` 返回传入、传出边以及传统参数解锁兵士等 CK3 语义边。追查事件或 on_action 链时改用 `operation=event_chain`，并用 `direction=callers|callees|both` 直接取得根、叶、循环、最短链与未解析调用。
- 编辑 `history/characters` 前先检查 `character:<id>`：定义结果会同时给出静态人物字段、按日期分组的生卒/婚姻/特质/effect 时间线，以及父母、配偶、雇主、王朝、家族和死亡原因引用；日期块不会作为伪字段进入经验模式。
- 生成前后使用 `ck3_preflight` 与 `operation=subject`，以较低成本发现定义冲突、未解析的对象/资源/声音引用、缺失本地化候选项和相关诊断。
- 起草改动时使用 `ck3_preflight` 与 `operation=patch`：提交完整的拟议文件内容，它会通过内存叠加检查解析器、编译器诊断以及对象/本地化/资源引用。由于没有刷新 SQLite，结果会设置 `needs_scan=true`。补丁操作可为 `upsert`、`delete` 或 `rename`；删除和重命名结果还包含影响证据。
- 只需要拟议删除、重命名或新增更新的风险面，而不需要完整验证证据时，使用 `ck3_impact`。
- 使用 `ck3_preflight` 与 `operation=dirty` 快速检查当前工程中磁盘上已修改的文件；该操作仍不会写入 SQLite。
- 写入少量当前工程文件后使用 `scan --files <relpath...>`，随后运行 `diag_stats`。增量扫描会同步刷新头衔 ID、省份归属和层级完整性诊断；来源配置变化、删除文件或覆盖链不确定时应执行完整 `scan`。
- 查询 `event:add_character_modifier` 或 `decision:trigger_event` 等嵌套语法时，对 `ck3_prepare_edit` 使用 `operation=examples` 和 `type:term` 标识符；条件允许时，片段会以匹配词为中心。
- 对不熟悉的作用域、数据类型、值形状、迭代器、示例和修正值使用 `ck3_script_reference`。实时引擎日志的优先级高于编译进程序的 Tiger 提示。
- 地图工具同时提供拓扑与精确几何：省份质心和边界框、图半径、八方向、方位角、直线像素距离、共享边界像素、地图画幅比例、水域边界与不可通行山脉边界。准确比较两个省份时使用 `map_spatial_relation`；其中的像素距离是质心距离，不是旅行距离。
- 路线任务先用一次 `map_route` 解析中英文地名、头衔或省份 ID，并计算合法陆路、海路或混合路线。再把完整 `route` 结果交给 `map_render`，启用 `auto_context=true` 生成沿途县/公国背景与精确 `route_points_output`；旅游卡片、箭头和说明继续由 HTML/SVG 叠加。不要逐省读取邻接表，也不要用端点直线距离冒充航程。
- 专题地图工具使用缓存的扫描线 RLE 几何和头衔层级邻接。先调用 `map_recipe_catalog`，再用 `map_build_metric` 检查公式，最后把受约束的填色、边界、标记、流线和标签图层交给 `map_render`。模型提供的数值必须包含 `source_note`，并会被标记为模型来源。
- 查询某个准确地理区域的沿岸浅深时，直接调用 `map_physical_context`，使用 `target_type=region`、`region:<id>` 与 `include_adjacent_water=true`。该路径只读取扫描缓存，一次返回沿岸邻海聚合；湖泊和大河不会混入海床结论，也不需要逐省调用邻接工具或构建完整指标表。
- 地理区域定义会作为 `geographical_region:<id>` 一等对象进入通用语义索引。先用 `ck3_search` 找准确 ID，再用 `ck3_inspect` 或 `ck3_dependencies` 查看定义、父子地区和脚本消费者；需要成员、地形或邻海聚合时再交给地图查询工具，不要全盘文本搜索地区名。
- 完整架构、SQLite 结构、算法、MCP/CLI 契约、性能数据、风险与审批门槛见[地图生成技术设计](docs/MAP_GENERATION_TECHNICAL_DESIGN.md)。
- 渲染器会自动区分裁剪范围内的海洋、湖泊、河流省份、不可通行海域和不可通行山脉。带独立指纹的物理缓存保存五张 PNG 栅格：多尺度山体阴影、曲率细节、归一化高度、CK3 河流像素，以及从 `gfx/map/map_object_data` 提取的山峰/悬崖锚点；刷新历史和领地头衔时不会重建这些栅格。
- 独立的 `map_object_instances` 缓存会把当前生效的树木变换采样为阔叶林、针叶林、丛林、棕榈、芦苇、灌木和枯木符号，并按省份索引 `building_locators.txt`。历史地图册配方会先绘制植被，再绘制政治边界，并在标签之前绘制带日期的地产类型符号；可通过显式图层省略任一标记来源。
- `adjacencies.csv` 中的显式连接会与自然像素边界邻接分开保存。较长的地下或离图连接会绘制为端点传送门，避免误导性的直线；相连的湖泊省份会缓存为水体，并记录岸边省份和岸线长度。
- CK3 地形材质顺序、`detail_index.tga` 与 `detail_intensity.tga` 会缓存为主导材质栅格、强度栅格和各省材质占比。这样渲染器就能用世界空间表面材质为政治纸张底图着色，而不必每次请求都重新扫描 8K 蒙版。
- 政治填色使用各领地头衔定义的 RGB `color`；只有头衔没有明确颜色时才回退到考虑邻接关系的颜色。在目标头衔层级使用 `field: "entity_id"`、`kind: "category"` 和 `palette: "political"`。
- 边界图层可使用 `source: "title_color"`，按边界两侧头衔各自的原生 RGB 颜色绘制。把顶层 `terrain_overlay` 设为 `false` 可获得干净的政治地图，同时保留水域和不可通行地形。如果 Mod 把本地化放在自定义目录下，标签语言检测也会识别 `_l_simp_chinese` 与 `_l_english` 文件名。
- 使用 `palette: "political_muted"` 可保留头衔原生色相，同时约束饱和度和明度以提高可读性。`political_coordinated` 会在 OKLCH 中锚定每个头衔，再以最多 8 度色相偏移和 0.12 明度偏移分离相邻头衔。
- 使用 `texture: "political_material"` 可获得由低频色彩变化、中尺度纸纤维和细颗粒构成的精细非重复表面。水域使用更安静的同类材质；不可通行山脉使用更强的岩石材质和稀疏程序化山脊。
- 只有用户明确要求写入磁盘时，才使用 `ck3-index map render <spec.json> --out <png> --meta <json>` 复现渲染并保存坐标 sidecar；MCP 渲染始终只读并在内存中完成。
- 只读比较地图版本时可运行 `ck3-index map province-mapping examples/map-province-mapping.json`。真正更新上游前必须先调用 `map_migration_snapshot`；更新后再调用 `map_province_migration`，由三方改写层生成独立本地测试 Fork，禁止模型自行全局替换省份数字。完整流程、自动化边界与 resolution 契约见[地图自动迁移说明](docs/CK3_MAP_MIGRATION.md)。
- 深色海洋历史地图册预设使用 `duchy_political_atlas` 配方，其中包含 `historical_atlas`、`full_atlas`、轻微地形阴影、双语标签、协调头衔颜色与 2 倍超采样。完整布局包含双层边框、本地化标题、年份、政治和地形图例、指北符号、来源徽章与相对距离比例尺。
- 新工作优先使用自适应配方：`political_atlas` 接受 `level: barony|county|duchy|kingdom|empire`；`thematic_atlas` 可在任一支持层级使用 `theme: culture|faith|development|terrain`。`boundary_levels` 可覆盖自动组合的高阶边界；带来源说明的自定义指标仍可使用显式图层。
- 同时省略 `width` 和 `height` 时，渲染器会根据省份覆盖范围、标签粒度、详细标记/流线图层、地形阴影和地图册布局，自动选择约 2K、4K 或 8K 的长边。结果会报告 `resolution_mode` 和 `resolution_reason`；明确尺寸仍会精确覆盖画布。核心与 MCP 共用常量把输出限制在原生省份地图级别 `8192x4096`，工作像素预算会拒绝不安全的 8K 加 2 倍超采样组合。世界级 `strategic_waterways_atlas` 始终使用帝国政治块和帝国标签，即使旧规格请求较低层级也是如此。
- 边界、标签、地图册装饰、地产、植被、可选标记、传送门与通道线都按最终输出像素定尺寸。从 1600px 到 8K 会保持相同视觉尺寸，只在更小画布上缩小，因此高分辨率会增加有效信息密度，而不会把界面元素一起放大。内置战略地图用亮蓝色水面区分湖泊，不再绘制湖泊图标。
- 地图册图例根据实际填色和边界图层生成。适用时包含男爵领、伯爵领、公国、王国、帝国/目标轮廓、本地化分类色块、地形、河流和无数据颜色；不会把脚本标识符直接当成可见图例标签。
- 所有新地图请求统一使用 `year`。`history_year` 只作为 `map_render` 的废弃兼容别名；若与 `year` 同时提供且不同，调用会被拒绝。游戏内发展度来自伯爵领头衔历史中累计的 `change_development_level`，不是推导的发展网络模型。
- Windows/Linux 插件启动器会从常见系统字体目录自动选择可用的中日韩字体；`CK3_INDEX_MAP_FONT` 可显式覆盖该选择。CLI 渲染规格也可使用 `font_path`；MCP 请求会拒绝客户端字体路径。缺少可用字体或本地化时会隐藏标签并报告不含本机路径的警告，而不会暴露脚本标识符。
- MCP `ck3_diagnostics` 的 `operation=summary` 有意采用扫描刷新、只读和当前工程范围，避免上游/参考来源淹没可编辑子 Mod。`scan` 与 `scan --files` 都会刷新头衔完整性；`validate` 是额外的完整复核入口，不再是发现重复头衔的唯一入口。每个 MCP 结果都报告索引代次，扫描期间代次变化会自动重试一次。
- `duplicate_title_id`、`duplicate_barony_province` 与 `invalid_title_hierarchy` 是带文件和行号的强完整性 warning。地图仍会生成，但结果标记 `integrity_status=warning`，冲突省份使用洋红斜线覆盖；GUI、`on_action` 等合并型命名空间不再产生笼统的 `duplicate_object` 噪声。
- 性能敏感改动后运行 `ck3-index bench`。热查询计划不应报告 `SCAN refs` 或 `SCAN objects` 风险。
- 修改提取、引用、资源、本地化、作用域规则或诊断后运行 `ck3-index accuracy`。它会执行针对已知误报与漏报的固定回归用例。
- 扫描仅限各配置来源中相对于来源根目录的 CK3 加载目录。即使根目录备份、工具、文档、缓存或临时目录内部含有嵌套的 `common/`、`events/` 或 `history/`，也会被剪枝。
- 文化定义现在会暴露支柱、传统、命名列表与父文化的类型化依赖；文化传统、支柱、革新与命名列表拥有不同对象类型。

## 数据来源

部分规则种子来自 ck3-tiger 源码与 CK3 引擎转储，并直接编译进二进制。它们只是本地规则提示，不应被当作 Tiger 权威结论：

| 来源 | 条目 | Go 文件 |
|---|---|---|
| `triggers.rs` | 1,315 个触发器 | `scope_data.gen.go` + `shape_data.gen.go` |
| `effects.rs` | 848 个效果 | 同上 |
| `iterators.rs` | 1,324 个迭代器 | `scope_data.gen.go` |
| `targets.rs` | 278 个作用域转换与类型化作用域前缀 | `scope_transitions.gen.go` |
| `defines.rs` | 1,903 个 define | `defines_data.gen.go` |
| `on_action.rs` | 199 个 on_action | `on_action_data.gen.go` |
| CK3/Tiger 声音转储 | `event:/` 声音事件 | `tiger_extra.gen.go` |

规则种子更新后重新生成：

```powershell
python tools/extract_all_scopes.py   > internal/indexer/scope_data.gen.go
python tools/extract_shapes.py       > internal/indexer/shape_data.gen.go
python tools/extract_defines.py      > internal/indexer/defines_data.gen.go
python tools/extract_on_actions.py   > internal/indexer/on_action_data.gen.go
python tools/extract_targets.py      > internal/indexer/scope_transitions.gen.go
```

本地 Wiki 整理出的工作流笔记汇总在 `docs/CK3_EXPERIENCE_NOTES.md`。它们是生成指导，不是引擎权威结论。
