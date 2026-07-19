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

需要 Go 1.24 或更高版本，以及你本机合法安装的 CK3 与 Mod 文件。

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

[[source]]
name = "game"
path = "../Crusader Kings III/game"
rank = 2
```

第一次建立索引：

```powershell
.\ck3-index.exe scan --clean
.\ck3-index.exe health
```

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

标准模式提供日常使用的核心工具；只有兼容旧客户端时才设置 `CK3_INDEX_MCP_PROFILE=expert` 启用旧名称。详细参数见 [MCP 工具参考](docs/MCP_TOOL_REFERENCE.md)。

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

## 许可证与发布

项目采用 GPL-3.0-or-later。发布包会额外校验可复现构建、第三方声明与 MCP 冒烟测试；完整流程见 [发布说明](docs/RELEASE.md)。
