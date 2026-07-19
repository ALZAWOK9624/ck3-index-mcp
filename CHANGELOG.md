# ck3-index 更新日志（2026-06-27 会话）

## Unreleased

## 0.4.0 - Public release (2026-07-19)

- Adopt GPL-3.0-or-later for ck3-index and prepare the first public release. External research checkouts, caches, game data, and local configuration are excluded from distribution.

- 暂无。

## 0.4.0-rc.1 - 正式版候选（2026-07-18）

- 发布链新增 Windows/Linux x64 便携包闸门：单一版本同步、双构建逐字节比较、固定 WhiteboxTools manifest、真实 MCP launcher 冒烟、实际链接 Go 模块许可证收集、私有路径/缓存/旧二进制拒绝、无时间字段内容清单、`SHA256SUMS` 与确定性 ZIP。项目自身许可证尚未选择时，只允许显式构建标记为 `PROJECT_LICENSE_MISSING` 的本地非公开 RC。
- 扩展 `ck3_gui` 预览：保留确定性、无脚本的 `static` HTML，并新增 CSP 哈希固定脚本的 `inspector` 模式，提供控件树、搜索、缩放、属性检查以及 `visible`、`enabled`、数值 `value`、动态文本、状态与点击的受控视觉模拟；模拟器不会执行 Jomini 表达式或游戏效果。
- HTML GUI 预览会在严格文件、像素和文档预算内读取已索引资源，把 PNG、DDS BC1/BC2/BC3 与 32 位无压缩 DDS 确定性转换为内嵌 PNG；输出只保留相对资源 ID、解码格式和尺寸，不泄漏索引文件路径。
- GUI `state` 块现在作为行为定义保留，不再被误绘制成可见控件；检查器可手动或通过 `_mouse_enter`/`_mouse_leave` 重放明确的 `alpha` 与 `duration`，复杂动画和游戏脚本仍不会被猜测执行。
- GUI 预览现在把已索引的英文和简体中文 `text`/`tooltip` 本地化绑定到节点，支持 `raw|english|simp_chinese|bilingual` 初始语言和检查器即时切换；颜色、图标和链接标记只做展示级清理，运行时宏保留为显式 `<runtime>` 与 `partial`，不伪造游戏状态。
- `ck3_gui` 新增最多 32 项的 `sample_values` 场景输入，对 `text`、`texture`、`visible`、`enabled` 表达式做精确匹配并生成初始视觉状态；纹理值只能引用已索引的 `gfx/` 相对资源，所有值标记为 `source=provided`，未命中项显式报告，不执行表达式或冒充存档事实。
- GUI 检查器新增真实 `scrollbox` 视口语义：透明展开 `block`/`blockoverride` 内容壳，按纵向流排列，静态输出裁剪越界内容，交互模式提供滚轮、滑条、嵌套裁剪与动态滚动范围；`autoresize` 多行文本会在本地化或运行时文本变化后按源码宽高限制重新测量，数据模型行仍不会被凭空实例化。
- `ck3_gui` 新增有界 `model_samples`：最多 8 个精确网格集合、单集合 16 行、合计 32 行，按唯一 `fixedgridbox`/`dynamicgridbox` 的 `item` 模板实例化调用者提供的数据。节点、命中报告、属性面板和点击日志保留 `source=provided` 与稳定行 ID；同表达式的手动视觉修改限定在当前行，不再串改其他克隆行。
- GUI 预览新增 `datamodel_wrap`、`addcolumn`、`addrow` 和 `flipdirection` 网格元数据及确定性单元格布局；检查器可在 `ignoreinvisible=yes` 时重新压紧已知隐藏行。真实 CK3 `vbox_diverge_traditions_list` 四行案例现在会跨文件解析依赖、递归展开深层 `blockoverride` 注入的传统卡片，加载五层动态 DDS，并验证名称、花费、选择光晕、阻止警告和原始点击链。
- GUI 查询的 `path_prefix` 现在只限定符号选择，类型、模板和覆盖依赖始终对全部活动 GUI 文件解析；结果用 `files` 与 `resolution_files` 分别报告选择范围和依赖闭包。覆盖替换会沿祖先链失效已缓存的解析状态，避免深层新控件被错误保留为不透明占位符。
- GUI HTML 现在保留并重放纹理 `mirror=horizontal|vertical|vertical|horizontal`，只翻转相应纹理层而不翻转控件坐标或文字；传统图标的右侧花纹因而与 CK3 的真实分层组合方向一致。
- GUI 纹理嵌入会先按同一资源在预览中的最大实际显示框确定性降采样，不放大源图，并报告源尺寸、嵌入尺寸与 `resized`；真实传统列表由 27/39 提升为 39/39 纹理全部嵌入，同时把 HTML 从约 983 KiB 降至约 682 KiB。
- 未压缩 DDS 解码器现在依据头标志区分逐行 `pitch` 与整图 `linear size`，兼容 CK3 的 `progress_circle.dds`、`scrollbar_fade.dds` 等合法资源，不再把整图字节数误当作单行跨度。
- GUI HTML 新增真实图集帧语义：`framesize + frame` 选择静态帧，`upframe/overframe/downframe/disableframe` 在普通、悬停、按下和禁用状态间切换；图集只编码一次并通过确定性背景位置复用。
- `Corneredstretched` / `Corneredtiled` 纹理现在按 `spriteborder` 与 `texture_density` 使用 CSS 九宫格拉伸或平铺；多帧图集会按帧裁成确定性 PNG，并在普通、悬停、按下和禁用状态切换对应九宫格，修复长 tooltip 与多状态滚动条仍显示为整张图集或居中正方形边框的问题。
- GUI 运行时计划新增强类型 `number` 结果：直接数值事实和字面量可驱动 `progresspie` 的圆形遮罩与 `progressbar` 的横向裁剪；`min`、`max`、`value` 分别求值后按控件范围归一化，真实 `progress_textures` block 中的填充/未填充 DDS 与普通 overlay 会分层嵌入。原始表达式、未钳制结果和 `source=provided` 来源继续保留，未知、无效范围或复杂数值不被猜测。

## 0.3.0 - 专题地图渲染器

- 吸收 MIT 许可的 `ParadoxGameConverters/provinceMapper@5d2f09c` 省份对应思想：新增只读 MCP `map_province_mapping` 与 CLI `map province-mapping`，以控制点 Delaunay 三角网和分片仿射变换比较两个已配置地图来源，输出双向像素占比、重编号、拆分、合并、复杂及未映射分组。实现保留证据而不采用上游多轮贪心“抢链接”，不写映射文件，也不向上游提交 AI 生成内容。
- 吸收 MIT 许可的 `jj248/CK3-Character-History-Generator@54898ef` 数据模型：`character` 现可索引静态身份字段、按日期分组的生卒/婚姻/特质/effect 时间线，以及父母、配偶、雇主、王朝和家族引用；日期不再污染经验字段名，`ck3_inspect character:<id>` 可直接返回人物档案与带来源证据的历史阶段。未吸收随机死亡、婚配、生育、世界观专用继承算法或正则解析器。
- 修复增量扫描可在未重建全局规则产物时直接推进 `index_rule_version` 的一致性漏洞：`scan --files` 现在遇到规则版本变化会明确要求先执行完整扫描，不能再把旧对象字段/引用伪装成新版本索引。
- 吸收 Paradox Chronicle 的事件树与调用层级思想：`ck3_dependencies operation=event_chain` 直接基于现有类型化引用返回 callers/callees、根、叶、循环、最短链及未解析调用；没有引入 IntelliJ PSI、CWT 运行时或第二套解析器。
- 新增严格的 CK3 Mod 成品打包器：MCP `ck3_package`、CLI `package`/`package-dir` 共用 Go 核心，执行双描述文件重建、安全路径与限额检查、现有 preflight 闸门、确定性 ZIP/清单、原子发布和七天 artifact 生命周期管理。
- 模型交付契约改为先通过 preflight，再调用 `ck3_package`；不再允许模型自行拼接 ZIP、launcher 描述文件、内部描述文件或安装说明。
- 审阅并吸收 MIT 许可的 `MnTronslien/AzgaarToCK3@5c41484` 地图校验特色：新增 MCP `map_asset_audit` 与 CLI `map audit`，校验省份定义/像素覆盖、PNG 编码、河流调色板索引和河道拓扑；复用现有 ck3-index 解析与地图图模型，并排除文化/信仰生成启发式及会误报 Godherja 保留河流索引的过严规则。

- 完成 MCP 复兴 Phase 0–1：冻结 41 工具线协议契约，把服务端迁入 `internal/mcpserver`，拆分协议、传输、注册表与结果职责，并在不改变公开工具和行为的前提下，补齐成功调用、畸形参数与 `tools/list` golden 覆盖。
- 完成 MCP 复兴 Phase 2–4：加入类型化闭合结构（含受限的嵌套地图输入）、结构化 MCP 错误与结果、MCP 2025-11-25/2025-06-18 协商、规范的标准/专家模式、旧调用兼容、公开输出脱敏、注册表生成文档、单一来源 `0.3.0` 版本，以及带真实 MCP 冒烟检查的 staging/安装发布链。
- 面向用户的 README、MCP 工具参考、CK3 经验笔记与当前更新记录已中文化；生成器会保持中文工具说明，同时让面向模型的技能目录继续使用英文。
- 世界级 `strategic_waterways_atlas` 改用帝国政治块和帝国标签，不再使用低阶头衔拼块。
- 以亮蓝色湖泊水面替代默认湖心图标，使其与深色海洋保持明显区别。
- 新增带指纹的 `map_object_instances` 缓存，用于当前生效的生成式植被变换；同时按省份索引 `building_locators.txt`，保留来源与准确省份分配。
- 新增确定性的阔叶林、针叶林、丛林、棕榈、芦苇、灌木和枯木分类及采样。
- 新增城堡、城市、大都会、教堂、部落、游牧营地、废墟、墓园和通用聚落的历史地产符号；空地产与荒地仍保持隐藏。
- 历史地图册配方现在会把植被绘制在政治边界下方，把带日期的地产符号绘制在标签下方，并分别报告标记来源、数量与图例条目。
- 完整索引与地图缓存收集新增 CK3 描述符 `replace_path` 处理，防止已被替换的原版文化、省份历史、头衔历史、角色和领地头衔泄漏进全转换结果。
- 重复地图历史按来源优先级摄取，确保高优先级 Mod 数据获胜，不受跨来源文件名排序影响。
- 新增 `duchy_political_atlas` 历史地图册配方，包含连续深色海洋纸张底图、完整地图册装饰、双语分层标签与 2 倍超采样。
- 新增带独立指纹的 PNG 栅格缓存，用于多方向 16 位高度图山体阴影和蓝/青色河流提取。
- 新增 OKLCH 原生颜色锚定和确定性邻接协调，限制色相/明度移动，并支持王国/帝国分层边界。
- 用缓存地形起伏、岩石不可通行地形、河流叠加、岸边压暗和海岸高光替代历史地图册的山脉排线。
- 新增自适应 MCP 配方 `political_atlas` 和 `thematic_atlas`，支持从男爵领到帝国的政治层级，以及文化、信仰、已索引发展度、地形和显式自定义图层。
- 新增男爵领头衔邻接缓存、高阶边界自动组合、由图层生成的地图册图例、本地化分类标签、唯一分类颜色与明确的无数据色块。
- 分离地图册显示年份与 CK3 历史年份，并索引伯爵领头衔累计的 `change_development_level` 效果，以生成事实性的游戏内发展度地图。
- 使用带几何指纹和增量失效的扫描线 RLE，缓存准确的省份填充与边界几何。
- 新增省份颜色/周长，以及从省份到帝国头衔的邻接聚合，记录普通边界、阻塞边界和水域边界长度。
- 新增六种可审计专题配方、带来源的自定义指标、图变换、五种受限图层、调色板、图例、来源、裁剪/比例处理与确定性 PNG 渲染。
- 新增 TTF/OTF/TTC 标签渲染，按简体中文、英文、标识符顺序回退；MCP 字体由服务端配置，不接受请求方指定。
- 新增考虑地形的物理底图、山脉排线、独立目标轮廓、仅内部政治边界、真实 Alpha 混合、描边标签、分位数分类与低饱和发展度调色板。
- 索引领地头衔 RGB 颜色，新增原生色政治填充；头衔定义不完整时回退到考虑邻接关系的颜色。
- 新增原生头衔色边界、可选可玩地形叠加，以及面向自定义本地化目录的文件名中文/英文标签检测。
- 新增保留色相的低饱和政治颜色、确定性低透明度头衔纹理，以及分层阴影/原生色边界样式。
- 新增确定性的多尺度政治、水域与岩石材质，不产生明显重复的排线方向。
- 新增 MCP 工具 `map_recipe_catalog`、`map_build_metric` 和 `map_render`，以及 `ck3-index map recipes|metric|render` CLI 命令。
- MCP 渲染保持只读；PNG 字节在内存中生成，模型提供的数值必须带 `source_note`。

## 0.2.2 - 精确地图上下文

- 新增 `map_spatial_relation`，提供两个省份的准确质心偏移、八方向关系、方位角、像素距离、地图对角线占比和直接边界分类。
- 扩展 `map_province_info` 与 `map_neighbors`，新增邻省中心、像素偏移/距离、方位角、共享边界像素、水域边界类型与不可通行山脉边界。
- 新增按水域类型和方向分组的紧凑邻接摘要，以及不可通行山脉方向计数。
- 保留现有 SQLite 地图缓存格式；本版本无需在服务端保存原始地图图像。

## 概览

将 ck3-index 从一个基础索引器升级为完整的 CK3 mod 编程助手。所有改动按类别汇总。

---

## 一、Bug 修复（来自另一 AI 的 REVIEW_REPORT）

| 文件 | 修复 | 严重度 |
|---|---|---|
| `scan.go` | `stats.Diagnostics += countDiagnostics` → `=` 修复双计数 | Medium |
| `scan.go` | `loadAllLocKeys` / `loadAllResources` 提前到 `refreshRefsResolvedGo` 之前，修复增量扫描 stale loc/resource | Medium |
| `query.go` | `RefHit` 加 `Source` 字段 + SQL 选 `f.source_name` + `in.Err()` / `out.Err()` 检查 | Medium |
| `llm.go` | `refEvidence` 传播 `Source` 字段，修复 public/group 过滤 | Medium |
| `mcp.go` | 删除 `serveMCP` 中不可达的 `return nil` | Low |

## 二、Bug 修复（Code Review 新发现）

| 文件 | 修复 | 严重度 |
|---|---|---|
| `scan.go` | overridden 文件 `os.Stat` 失败时返回 `overridden: true`，防止 nil deref | Critical |
| `scan.go` | 删除不可达的第二个 `if err != nil` | Medium |
| `scan.go` | `LastInsertId()` 错误忽略 → 改为检查 | Medium |
| `mcp.go` | 多次修复 `mcpTools()` 的额外 `}` 导致语法错误 | — |
| `health.go` | `hasUTF8BOM` 打开失败返回 `false`（原来 `true` 吞噬错误）+ Read 错误检查 | Medium |
| `health.go` | `checkSavedScopeCrossFile` / `checkVariableCrossFile` LEFT JOIN 改为 NOT EXISTS | Medium |
| `query.go` | `Overrides = Definitions` slice 共享 → 改为 deep copy | Medium |
| `lexer.go` | 裸 `?` 被当标识符 → 改为报错 | Medium |
| `lexer.go` | `\r\n` 列号偏移 → 处理 `\r` | Low |
| `lexer.go` | `operator()` 和 `ident()` 加 `peek()` ok 检查 | Low |
| `parser.go` | 操作符后意外 token 不 advance → 加 `p.advance()` | Medium |
| `scope_data.gen.go` | 3 个 scope 常量值为 0 → 修复 Python 生成器 bit overflow | High |

## 三、Parser 增强（CK3 GUI + 参数语法）

| 文件 | 新增 | 效果 |
|---|---|---|
| `parser.go` | `type = A = B` 语法（CK3 GUI type 声明） | `GH_types_main_types.gui` 3 条 parse_error 清零 |
| `parser.go` | `OPERATOR = <=` 语法（CK3 参数化触发器） | `vizierate_events.txt` 20 条 parse_error 清零 |
| `lexer.go` | 跳过 UTF-8 BOM（`\uFEFF`） | `magocratic_government` 等 BOM 文件正确索引 |

## 四、Object 提取增强（嵌套结构）

| 文件 | 新增 | 效果 |
|---|---|---|
| `scan.go` | `extractObjects` 递归 faith 嵌套（`faiths = { X = {...} }`） | 宗教文件里所有信仰正确索引 |
| `scan.go` | `extractObjects` 递归 title 嵌套（`k_kingdom → d_duchy → c_county`） | 深层嵌套的 county/duchy/barony 正确索引 |
| `scan.go` | `extractObjects` GUI 黑名单（25 个内置 GUI 原语） | `container`、`icon` 等不再产生 duplicate |
| `scan.go` | `objectTypeForPath` 扩展到 20+ 目录 + `religion` 类型修正（faith → religion） | |

## 五、Reference 提取增强（新 ref 种类）

| 新增 kind | 数量 | 触发条件 | 用途 |
|---|---|---|---|
| `iterator` | 45,056 | block key 在 `iteratorScopeIn` 中 | 可查 `find_refs any_tributary` |
| `scope_transition` | 39,930 | block key 在 `scopeTransitionsIn` 中 | 可查 `find_refs liege` |
| `define` | 15,938 | value 以 `@` 开头 | 可查 `find_refs @MAINTENANCE_COST` |

**修复的 ref 误匹配：**
- `keyRefTypes` 跳过 scope 表达式（`prev`、`.dot`、`scope:`）
- `keyRefTypes` 跳过 `.t` / `.desc` / `.tt` loc key
- `prefixTypes` 跳过 scope 链和双前缀（`culture:scope:xxx`、`religion:religion:xxx`）

## 六、Lint 检查（新 diagnostic 代码）

| 文件 | 诊断码 | 检查内容 | 来源 |
|---|---|---|---|
| `lint.go` | `missing_trigger_else` | 2+ `trigger_if` 链缺 `trigger_else` 收尾 | Wiki M19 |
| `lint.go` | `on_action_direct_override` | 原版 on_action 里直接写 effect/trigger 覆盖 | Wiki M9 |
| `lint.go` | `gui_crash_risk` | GUI 崩模式（hbox/vbox + %size、flowcontainer+hbox） | Wiki M21 |
| `lint.go` | `gui_layout_misuse` | hbox/vbox 内用 parentanchor（应换 expand） | Wiki M22 |
| `lint.go` | `nested_iterator` | 嵌套迭代器（深度≥2） | Wiki M6 |
| `lint.go` | `event_no_option` | 事件缺 option 块 | Wiki M17 |
| `lint.go` | `scope_never_saved` | `scope:name` 块未配 `save_scope_as`（只查 project） | Wiki M3 |
| `scope_tracker.go` | `scope_mismatch` | 迭代器感知 scope 栈追踪——iterator block 内 scope push/pop | Wiki M1 |

**scope_never_saved 防误报优化：** 90+ 内置 scope 白名单、跳过 scope chain（含 `.`）、跳过单字母、降级 info

## 七、Health 检查（Post-scan 跨文件校验）

| 文件 | 诊断码 | 检查内容 |
|---|---|---|
| `health.go` | `missing_event_loc` | 事件/决议缺 loc 引用 |
| `health.go` | `lios_partial_override` | LIOS 部分覆盖检测 |
| `health.go` | `scope_never_saved`（跨文件） | scope 引用跨所有生效文件校验 |
| `health.go` | `variable_never_set`（跨文件） | global_var 引用跨所有生效文件校验 |
| `health.go` | `duplicate_character` | 同名历史角色跨文件重复 |

## 八、ck3-tiger 数据全量提取

| 数据表 | 条目 | 生成文件 | MCP 工具 |
|---|---|---|---|
| `triggers.rs` + `effects.rs` (scope) | 1,315 + 848 | `scope_data.gen.go` (4,901 行) | `lookup_scope` |
| `triggers.rs` + `effects.rs` (shape) | 2,150 | `shape_data.gen.go` (2,190 行) | `lookup_shape` |
| `iterators.rs` | 1,324 | 同上 | scope tracker 内部 |
| `targets.rs` | 222 | `scope_transitions.gen.go` | scope transition 内部 |
| `defines.rs` | 1,903 | `defines_data.gen.go` | `lookup_define` |
| `on_action.rs` | 200 | `on_action_data.gen.go` | `lookup_on_action` |
| `modifs.rs` | 634 | `tiger_modifs.gen.go` | `lookup_modifier`（升级） |
| `sounds.rs` | 923 | `tiger_extra.gen.go` | `IsSound` |
| `localization.rs` | 426 | 同上 | `IsLocMacro` |

**提取工具：** `tools/extract_all_scopes.py`、`tools/extract_shapes.py`、`tools/extract_defines.py`、`tools/extract_on_actions.py`、`tools/extract_targets.py`、`tools/extract_tiger_modifs.py`、`tools/extract_tiger_extra.py`

## 九、Game Log 数据提取

| 数据 | 条目 | MCP 工具 |
|---|---|---|
| `effects.log` descriptions | 1,886 | `lookup_example` |
| `triggers.log` descriptions | 1,768 | `lookup_example` |
| `modifiers.log` tags | 2,210 | `lookup_modifier` |

## 十、不信任数据过滤

全量游戏安装（18,664 文件 / 520MB）、Godherja（7,399 文件）、游戏源码（14,704 文件）三重 grep 验证：

| 数据表 | 确认率 | 不可信 | 处理 |
|---|---|---|---|
| trigger/effect scope+shape | 85% (1,833/2,150) | 317 | `deprecated_data.gen.go`，参考不拦截 |
| modifier kinds | 97% | 14 | 不标记 |
| defines | 99% | 18 | 不标记 |
| on_actions | 99% | 2 | 不标记 |
| loc_macros | 98% | 6 | 不标记 |

## 十一、性能优化

| 优化 | 效果 |
|---|---|
| 并发 worker 池（GOMAXPROCS） | parse + hash 并行 |
| prepared statements（14 个预编译 SQL） | 写库速度 ↑ |
| 延迟建索引（clean scan 最后 CREATE INDEX） | 写库速度 ↑ |
| PRAGMA synchronous=OFF / cache_size=-200000 | DB 写加速 |
| SHA256 + mtime 增量扫描 | 未变时 12s |
| rel_path 覆盖检测（不解析被覆盖文件） | objects -25% / refs -19% |
| 砍 nodes 表全量写入（context check 移 worker） | 省 1270 万行 |

Scan 性能：clean 34s / incremental 12s

## 十二、MCP 工具总数：18 个

查询：`inspect_object` `prepare_edit` `diagnose_key` `query_object` `query_object_types` `find_refs` `query_loc` `query_resource` `query_examples` `query_rules`
校验：`validate_project` `explain_diagnostic`
Tiger 数据：`lookup_scope` `lookup_shape` `lookup_define` `lookup_on_action` `lookup_example` `lookup_modifier`

## 十三、诊断码汇总

| 诊断码 | 最终数量 | 来源 |
|---|---|---|
| `parse_error` | 0 | parser 增强后清零 |
| `missing_object_reference` | 0 | 嵌套提取 + ref 过滤修复 |
| `missing_localization` | 91 | 项目缺 loc |
| `gui_layout_misuse` | 25 | 项目 GUI |
| `missing_resource` | 6,033 | game 源缺 gfx（不影响游戏） |
| `missing_trigger_else` | 1,576 | godherja 上游 |
| `nested_iterator` | 479 | 上游 |
| `scope_never_saved` | 0 | 白名单+只查 project 后清零 |
| `event_no_option` | 0 | 只查 project 后清零 |
| `on_action_direct_override` | 13 | 上游 |
| `scope_mismatch` | 5 | 上游 |

**总诊断：21,190 → 目标区域仅 116 条 actionable（项目 91 loc + 25 GUI）**

## 十四、新增文件

| 文件 | 用途 |
|---|---|
| `scope_data.gen.go` | tiger trigger/effect/iterator scope 数据（4,901 行） |
| `shape_data.gen.go` | tiger trigger/effect 取值形状数据（2,190 行） |
| `deprecated_data.gen.go` | 不信任 key 列表（327 行） |
| `tiger_modifs.gen.go` | modifier scope kinds（642 行） |
| `tiger_extra.gen.go` | sounds + loc macros（1,362 行） |
| `defines_data.gen.go` | game defines |
| `on_action_data.gen.go` | game on_actions |
| `scope_transitions.gen.go` | scope transitions |
| `lint.go` | 结构化 lint 检查（600+ 行） |
| `health.go` | 跨文件健康检查（250+ 行） |
| `scope_check.go` | scope 校验 + MCP lookup 函数（200+ 行） |
| `scope_tracker.go` | 迭代器感知 scope 栈追踪（100+ 行） |
| `tools/extract_all_scopes.py` | 主提取脚本 |
| `tools/extract_shapes.py` | shape 提取脚本 |
| `tools/extract_defines.py` | define 提取脚本 |
| `tools/extract_on_actions.py` | on_action 提取脚本 |
| `tools/extract_targets.py` | scope 转换提取脚本 |
| `tools/extract_tiger_modifs.py` | modifier 提取脚本 |
| `tools/extract_tiger_extra.py` | sounds/loc 提取脚本 |
| `tools/char_rank.py` | CK3 角色排行工具 |
| `SKILL.md` | 更新为完整 LLM skill 文档 |
| `README.md` | 更新为完整说明 |
| `CHANGELOG.md` | 本变更日志 |
