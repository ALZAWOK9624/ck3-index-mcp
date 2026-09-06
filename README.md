# ck3-index MCP

面向《十字军之王 III》模组开发的语义索引、诊断与只读 MCP 服务。它把 Mod、游戏本体和可选参考来源按优先级合并索引，让编辑器或 AI 能先查到实际定义、引用、覆盖关系和诊断证据，再提出修改建议。

本仓库不包含游戏文件、Mod 内容、索引缓存或个人配置。

## 适合做什么

- 查找事件、决议、头衔、文化、信仰、本地化、资源与脚本键的实际定义和引用。
- 在修改前检查语法、作用域、缺失引用、本地化、资源和覆盖风险。
- 分析事件调用链、对象依赖、同名覆盖与增量修改影响。
- 审计和渲染 CK3 地图，辅助省份映射、路线、地图迁移与地形上下文分析。
- 预览已索引的 GUI 结构、纹理、本地化和受控交互状态。
- 在严格检查通过后，生成可手动安装的 Mod 成品包。

## 快速开始

需要 Go 1.26 或更高版本（与 `go.mod` 一致），以及你本机合法安装的 CK3 与 Mod 文件。

```powershell
git clone https://github.com/ALZAWOK9624/ck3-index-mcp.git
cd ck3-index-mcp
go build -o ck3-index.exe .
```

在仓库根目录创建 `ck3-index.toml`，按自己的文件位置填写来源。路径可以是绝对路径，也可以相对配置文件。

```toml
database = "cache/ck3_index.sqlite"

[[source]]
name = "project"
path = "../my-ck3-mod"
rank = 1
role = "project"
private = true

[[source]]
name = "game"
path = "../Crusader Kings III/game"
rank = 2
role = "game"
private = false

[[source]]
name = "engine-assets"
path = "../Crusader Kings III/clausewitz"
rank = 3
role = "reference"
private = false
resource_only = true
```

### 搜索返回的 token 成本

`ck3_search` 只在 `structuredContent` 中返回一份结果，`content` 为空数组。`evidence`、`suggestions`、`batch` 统一使用 `columns` / `rows` 表格；来源和路径直接保留为字符串，`null` 表示字段缺省。空结果、单条与批量结果使用相同契约；没有格式开关、旧结果副本、共享字段展开或路径字典。调用方直接把 `structuredContent` 序列化给模型即可。

此接口更新需要同步升级只读取正文或旧对象数组的客户端；已删除旧 `format` 参数。具体定义与实测数据见 [搜索结果结构](docs/SEARCH_RESPONSE_COMPACTION.md)。不需要重建索引。

### MCP 并发、排队与超时

重型、栅格与其他高成本调用由每个 MCP 服务实例的共享限额约束；栅格任务同时占用重型共享限额。超出活动限额的请求进入有界队列，被重型限额阻塞的请求不会堵住可运行的普通查询。客户端取消会传递到正在执行的任务和 SQLite 查询；排队超时返回 `OPERATION_QUEUE_TIMEOUT`，执行超时返回 `OPERATION_TIMEOUT`，两者的 `details.phase` 不同。

```toml
sqlite_read_connections = 8
sqlite_cache_mb_per_connection = 64
max_open_database_pools = 2
max_sqlite_cache_budget_mb = 1024
mcp_max_tasks = 12
mcp_max_heavy_tasks = 2       # 共享高成本任务限额，必须小于总任务数和 SQLite 连接数
mcp_max_raster_tasks = 1      # 同时受 mcp_max_heavy_tasks 约束
mcp_max_queued_tasks = 32
mcp_queue_timeout_seconds = 15
mcp_execution_timeout_seconds = 900
```

`ck3_health mode=quick` 和 `ck3_database` 的 `list/status` 属于普通读任务；`ck3_health mode=deep` 和数据库 `switch` 占用共享高成本任务限额，因此它们使用同一套并发、排队和执行超时诊断。`ck3_health` 会报告活动/排队任务、各类上限、两段超时以及为普通查询保留的 SQLite 连接数。调用方仍应串行提交重型任务；服务端队列是故障边界，不是批处理接口。

旧配置若省略新的 `max_sqlite_cache_budget_mb`，默认仍为 1024 MB，但会自动提高到足以容纳其既有单个 SQLite 连接池，避免仅因升级而无法启动；要同时保留或切换到额外连接池，仍需显式配置足够的进程总预算。

### MCP 运行时数据库热切换

一个 MCP 进程可以由管理员预先登记多个具名 SQLite 索引。AI 先调用 `ck3_database` 的 `list` 操作读取名称与说明，再用 `switch` 选择所需证据库；工具不接受任意文件路径。`config` 适合来源模型不同的另一套工作区配置，`database` 只适合同一工作区配置的另一份索引快照。

```toml
database = "cache/project.sqlite"
mcp_database_name = "project"
mcp_database_description = "当前 Mod、上游和游戏本体的合并索引"

[[mcp_database]]
name = "vanilla"
description = "只含 CK3 原版与引擎资料"
config = "base-vanilla.toml"

[[mcp_database]]
name = "previous"
description = "当前工作区的上一份索引快照"
database = "cache/project-previous.sqlite"
```

切换无需重启 MCP，只影响切换完成后开始的调用。已经运行的查询继续持有原数据库租约，最后一个旧租约释放后旧连接池才会关闭；如果期间切回旧库，会安全复用仍存活的连接池。候选库在打开前同时受 `max_open_database_pools` 和 `max_sqlite_cache_budget_mb` 的进程总预算约束，旧 lease 持有的 retired 池仍计入预算，超限时保持当前活动库不变并返回可重试诊断。所有工具结果都带有 `database.name` 与 `database.epoch`；`ck3_health` 还报告 `loaded_database_count`、`retired_database_count`、`aggregate_sqlite_cache_budget_mb` 及两项上限，避免把两个索引的证据或内存预算混在一起。

`role` 表示来源身份，`rank` 只表示覆盖优先级；配置必须恰好有一个 `project` 来源。`private = true` 的来源不会进入公开可见性结果。`resource_only = true` 只遍历 `gfx/`、`map_data/` 和 `sound/`，适合把 CK3 安装目录中的 `game`、`clausewitz`、`jomini` 资源补入解析，而不重复索引其脚本定义；它不能用于 `project` 来源。

### 多工程共用上游索引

上游层占索引的绝大部分，多个工程各自重扫一遍纯属浪费。可选的 `base_database` 指向一份只含上游层的预建索引，全量重建会先复制它，再只解析工程层。

```toml
database = "cache/my-mod.sqlite"
base_database = "cache/base-vanilla.sqlite"
```

base 必须用一份**工程来源指向空目录**、其余来源与本配置逐项一致的配置扫出来：

```powershell
./ck3-index.exe --config base-vanilla.toml scan --clean
```

空目录这条不是形式要求。播种的正确性依赖"上游文件一开始都是活跃的"，这样被工程隐藏时永远是 活跃 → 被覆盖 的单向转换；反向转换救不回来，因为被覆盖的文件从未被解析过，光凭元数据无法复原派生行。base 里带了别的工程的行会被直接拒绝。

发布代次、索引规则版本、引擎日志指纹和上游来源指纹（含每层的 `rank`、`role`、`private`、`resource_only`）四项中任何一项不匹配都会拒绝复用。**拒绝不是错误**：刷新照常回退成完整解析，产出同一份索引，只是慢一些，原因写在 `base_seed.reason` 里。看到重建没有变快，先查 `stats.base_seed`。`base_database` 不能与 `database` 指向同一个文件，否则一次刷新就会用单个工程的快照覆盖共用的上游索引。

上游本身变了才需要重建 base。

## 证据索引与刷新边界

`ck3-index` 的职责是把 CK3 文本、索引快照和有限运行时日志组织成可追溯证据，而不是替调用方生成或修改 Mod 内容。来源层由 `role`、`private` 和覆盖 `rank` 三项独立描述；扫描将这份策略写进可重建 SQLite 缓存，MCP 再以相同策略过滤公开结果。

读取工具只消费已发布的索引代次。`ck3_refresh status` 可在未建索引时安全调用，`files` 只对明确列出的工程相对路径做事务性增量更新；任何会改变全局解析语义、暴露低优先级文件或依赖完整地图重建的情况都会返回可恢复的完整扫描要求。`full` 会先在旁路 SQLite 缓存完成新代次，再用一次事务发布到当前缓存；扫描异常或取消只丢弃旁路结果，旧的 ready generation 会继续可读，绝不静默退化。

`ck3_search` 只会把机械等价的大小写、分隔符或类型前缀修正提升为高置信度证据；仅共享最长 token 前缀的近似项放在 `suggestions`，并带 `recovered_query`、`recovery_confidence=low` 与独立的 `suggestion_pagination`。调用方不得把这些候选当作证据或自动执行后续动作。

第一次建立索引：

```powershell
.\ck3-index.exe scan --clean
.\ck3-index.exe health
```

部署或自动更新脚本应使用 `.\ck3-index.exe health --require-ready`。该模式仍输出完整 health JSON，但当索引 generation 未 ready、数据库与配置不一致、地图/FTS/性能索引不完整，或索引规则版本不匹配时返回非零退出码；普通 `health` 保持仅报告状态的兼容行为。

之后编辑少量文件时，优先做增量更新：

```powershell
.\ck3-index.exe scan --files common/decisions/example.txt
.\ck3-index.exe diag_stats
```

启动 MCP 服务：

```powershell
.\ck3-index.exe mcp
```

## 推荐工作流

1. 首次使用执行 `scan --clean`，配置变化后也重新完整扫描。
2. 先用 `ck3_search` 找对象，再用 `ck3_inspect` 查看定义、覆盖和引用证据。
3. 写入前使用 `ck3_prepare_edit`、`ck3_preflight` 与 `ck3_impact` 检查方案。
4. 小改动后使用 `scan --files`；大改动或发布前执行 `scan`、`validate` 和 `diag_stats`。

## 特色功能

- **语义索引而非纯文本搜索**：理解 CK3 常见对象、脚本键、引用、覆盖和来源优先级。
- **可审计诊断**：每项问题都尽量保留来源与行号；不确定的推断会明确标记，而不是伪装成结论。
- **事件与对象关系图**：可追踪调用者、被调用者、循环、根节点和未解析引用。
- **地图与 GIS 工具**：覆盖省份完整性、地形与水文、地图路线、地图渲染、省份映射和保守迁移。
- **GUI 检查器**：解析跨文件模板与覆盖关系，输出受限的 PNG 或独立 HTML 预览，不执行游戏脚本。
- **安全成品打包**：拒绝危险路径和冲突描述文件，使用确定性 ZIP，并在写入前完成检查。

## 成品打包

`ck3-index package <spec.json>` 接收完整元数据与文件列表；`ck3-index package-dir <mod-dir> --meta <metadata.json>` 打包已有工程目录。打包前会检查语法、作用域、引用、本地化、资源和覆盖风险；输出使用稳定顺序，且不会记录本机路径或用户名。

完整参数与示例见 [成品打包说明](docs/CK3_PACKAGING.md)。

## MCP 工具

MCP 只公开一套规范工具；精细能力通过各工具的受限 `operation` 参数提供。详细参数见 [MCP 工具参考](docs/MCP_TOOL_REFERENCE.md)。

<!-- BEGIN GENERATED MCP TOOLS -->
## MCP 工具（38 个规范工具）

ck3-index 仅公开一套规范 MCP 工具；细分能力通过受限 operation 提供，不再保留旧版专用工具别名。

### 核心工具

| 工具 | 用途 |
|---|---|
| `ck3_search` | 在不知道准确 CK3 标识符时进行搜索。返回按相关度排序的对象、本地化、资源、引用、诊断、数据类型与脚本证据。只读取 structuredContent；evidence、suggestions、batch 均为 columns/rows 表格，null 表示字段缺省，无重复正文。 |
| `ck3_inspect` | 发现目标后，检查一个准确的 CK3 标识符、键或资源路径。定义视图包含覆盖来源、事件字段以及人物静态档案和日期时间线；引用视图保留关系、阶段、置信度和未解析原因；compare 可对准确类型化标识符做受限、只读的来源与上游对象级比较。 |
| `ck3_review` | 审查完整的拟议 CK3 文件；未提供文件时审查当前工程中的脏文件。执行只读的语法、作用域、引用、本地化与资源检查。 |
| `ck3_workspace` | 在选择具体对象前检查已索引的工作区结构。返回架构概览、对象类型分布，或 engine、CK3 1.19 快照与原版相邻注释之间只读的 on_action 证据审计。 |
| `ck3_dependencies` | 追踪一个 CK3 标识符周围的语义依赖。neighborhood 返回通用邻域；event_chain 返回调用者、被调用者、根、叶、循环、最短链和未解析调用，并可附带自包含、无外部请求的交互 HTML 检查器。 |
| `ck3_prepare_edit` | 在生成 CK3 脚本前加载编辑证据。默认返回组合上下文，也可只请求示例、结构规则或经验模式。 |
| `ck3_preflight` | 对已索引目标、拟议完整文件或当前脏文件执行只读门禁。使用 operation 选择目标、补丁或脏文件模式。 |
| `ck3_impact` | 编辑前分析拟议的新增或更新、删除与重命名操作。返回只读依赖风险与未解析引用风险。 |
| `ck3_diagnostics` | 无需重新扫描即可检查已缓存的工程诊断。默认返回摘要；explain 可按诊断代码和可选来源字段筛选。 |
| `ck3_diagnostic_baseline` | 记录当前已经存在的诊断，使后续 ck3_diagnostics 只报告此后新出现的问题。save 把索引当前持有的诊断记在一个名字下，覆盖同名的旧快照；list 列出已记录的名字；clear 忘掉一个。在没有任何诊断的工程上记录的基线同样是基线，而且是最有用的那种：此后出现的每一条都是新的。基线不会被 ck3_refresh operation=full 清除，因为重建会原样复现它当初对照的那些诊断。 |
| `ck3_save` | 读取单个 CK3 存档文件。card 与 compatibility 只读元数据：存档身份——版本、游戏内日期、玩家、主头衔、家族、政体、玩家人数——以及存档声明的 mod、DLC 与游戏规则，供调用方与自己的配置比对。audit 与 character 流式扫描 gamestate：存档携带但已索引来源不再定义的 ID，以及单个角色的属性、特质、家族与头衔。timeline 提取存档记录的带日期事件——头衔继承史与人物记忆。document 走到 gamestate 的任意路径并报告那里有什么，因此没有专门投影的区块同样可读。工具只陈述存档记录的事实，不判断存档能否载入。 |
| `ck3_refresh` | 在 Mod 源文件变动后刷新已配置工程层的索引。status 只报告就绪状态；files 只增量更新显式给出的相对路径；full 通过旁路扫描和事务发布完整重建，不会悄悄降级。 |
| `ck3_script_reference` | 查询一项本地引擎或脚本规则事实。通过 kind 选择作用域、数据类型、值形状、define、on_action、迭代器、示例或修正值。 |
| `ck3_health` | 检查数据库、结构、索引与 MCP 注册是否可信，并报告 SQLite 读取连接、已加载/退役连接池、进程总缓存预算及限额、当前重型/栅格任务和估算任务内存。配置与来源根可辨识，数据库绝对路径保持隐藏。 |
| `ck3_database` | 列出管理员配置的 SQLite 索引、报告当前数据库，或在不重启 MCP 的情况下按名称热切换后续调用。运行中的调用继续持有原数据库租约及连接池预算；候选库超出进程总预算时保持当前数据库不变。调用方不能提交文件路径。 |
| `ck3_package` | 严格验证模型生成的 CK3 文本与二进制文件，统一生成双描述文件，并在受限临时区创建可直接手动安装的 ZIP；不会安装或修改真实 Mod 目录。 |
| `ck3_gui` | 通过现有索引检查生效中的 CK3 GUI 文件，解析跨文件继承、模板和区块覆盖，并输出有界 PNG 或自包含 HTML。检查器支持控件树、裁剪滚动视口、网格布局、英中本地化切换、已索引动态纹理样例与受控行为模拟；model_samples 可从唯一 item 模板实例化有界调用方列表行，绝不执行任意 Jomini 代码。 |
| `ck3_coat_of_arms` | 读取一个生效的纹章并把它画出来。inspect 将纹章的底纹、三种颜色与每个纹饰对照生效的 named_colors 和已索引贴图逐一解析，并报告哪些引用没有任何来源提供；声明了 parent 的定义会先逐层合并父定义，因此返回的是真正会画出来的那份设计；render 合成 CK3 在加框之前构建的纹章本体并返回 PNG；assets 列出纹章定义可以引用的底纹与纹饰贴图名。颜色与贴图均按已配置的载入顺序解析，因此结果是游戏实际会载入的那一份，而不是某一个来源单独声明的内容；visibility=public 下只按公开层的载入顺序解析，响应会明确报告这一点。 |

### 地图工具

| 工具 | 用途 |
|---|---|
| `map_migration_snapshot` | 在上游地图更新前，保存已配置旧上游、当前 Mod 文件清单、文本基线与有效地图的内容寻址快照；不接受任意路径，也不修改来源工程。 |
| `map_province_migration` | 以新上游为底执行保守三方合并和省份语义改写，严格验证通过后才生成独立本地测试 Fork；冲突时只输出报告和 resolution 模板。 |
| `map_asset_audit` | 审计当前生效的 CK3 地图栅格，检查省份定义覆盖、PNG 编码、河流调色板索引语义和正交河道拓扑；吸收 AzgaarToCK3 的特色校验，但不重复 ck3-index 已有解析与几何。 |
| `map_province_mapping` | 通过控制点 Delaunay 分片仿射变换比较两个已配置的 CK3 省份地图，返回像素交叠、重编号、拆分、合并、复杂及未映射分组；只提供迁移证据，不写地图或历史文件。 |
| `map_split_province` | 按调用方给定的种子像素，以地形加权生长切分一个已索引省份，并发布绑定当前扫描代次、provinces.png 与 definition.csv 哈希的不可变方案产物。规划 definition.csv、history 与领地头衔改动，无法证明安全时给出阻塞项；Mod 本身不会被修改。 |
| `map_apply_split` | 加载此前审查过的精确不可变切分几何，以原子方式发布重新着色的 provinces.png，并附上必须同时应用的 definition.csv、history 与领地头衔改动。支持可选 request_key 恢复同一次提交；方案哈希、扫描代次或地图源文件发生漂移时拒绝写入，Mod 本身不会被修改。 |
| `map_terrain_edit` | 按顺序合成点、折线或多边形地貌图层，下切大河河谷，或只替换指定窗口内的小河并保留窗口外内容。confirm=false 仅返回内存预览，confirm=true 才创建带哈希、父级校验和可选幂等键的不可变产物。只输出原始高度图与必要的 rivers.png，绝不修改 Mod，并明确要求经 CK3 地图编辑器重新打包。 |
| `map_artifact` | 列出、查询状态或完整检查已提交的地貌、省份切割方案与切割栅格产物。验证不可变 manifest 和输出哈希，使调用方在超时后能找回产物 ID，而不会重复生成文件。 |
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
<!-- END GENERATED MCP TOOLS -->

## 许可证与发布

项目采用 GPL-3.0-or-later。发布包会额外校验可复现构建、第三方声明与 MCP 冒烟测试；完整流程见 [发布说明](docs/RELEASE.md)。
