package main

import (
	"fmt"
	"strings"

	"ck3-index/internal/mcpserver"
)

type chineseToolText struct {
	Title       string
	Description string
}

var chineseToolTexts = map[string]chineseToolText{
	"ck3_search": {
		Title:       "搜索 CK3 索引",
		Description: "在不知道准确 CK3 标识符时进行搜索。返回按相关度排序的对象、本地化、资源、引用、诊断、数据类型与脚本键证据。",
	},
	"ck3_inspect": {
		Title:       "检查 CK3 标识符",
		Description: "发现目标后，检查一个准确的 CK3 标识符、键或资源路径。定义视图包含覆盖来源、事件字段以及人物静态档案和日期时间线；引用视图保留关系、阶段、置信度和未解析原因；compare 可对准确类型化标识符做受限、只读的来源与上游对象级比较。",
	},
	"ck3_review": {
		Title:       "审查 CK3 文件",
		Description: "审查完整的拟议 CK3 文件；未提供文件时审查当前工程中的脏文件。执行只读的语法、作用域、引用、本地化与资源检查。",
	},
	"ck3_workspace": {
		Title:       "检查 CK3 工作区",
		Description: "在选择具体对象前检查已索引的工作区结构。返回架构概览、对象类型分布，或 engine、Tiger 与原版相邻注释之间只读的 on_action 证据审计。",
	},
	"ck3_dependencies": {
		Title:       "追踪 CK3 依赖",
		Description: "追踪一个 CK3 标识符周围的语义依赖。neighborhood 返回通用邻域；event_chain 返回调用者、被调用者、根、叶、循环、最短链和未解析调用，并可附带自包含、无外部请求的交互 HTML 检查器。",
	},
	"ck3_prepare_edit": {
		Title:       "准备 CK3 编辑",
		Description: "在生成 CK3 脚本前加载编辑证据。默认返回组合上下文，也可只请求示例、结构规则或经验模式。",
	},
	"ck3_preflight": {
		Title:       "预检 CK3 改动",
		Description: "对已索引目标、拟议完整文件或当前脏文件执行只读门禁。使用 operation 选择目标、补丁或脏文件模式。",
	},
	"ck3_impact": {
		Title:       "分析 CK3 补丁影响",
		Description: "编辑前分析拟议的新增或更新、删除与重命名操作。返回只读依赖风险与未解析引用风险。",
	},
	"ck3_diagnostics": {
		Title:       "检查 CK3 诊断",
		Description: "无需重新扫描即可检查已缓存的工程诊断。默认返回摘要；explain 可按诊断代码和可选来源字段筛选。",
	},
	"ck3_script_reference": {
		Title:       "查询 CK3 脚本参考",
		Description: "查询一项本地引擎或脚本规则事实。通过 kind 选择作用域、数据类型、值形状、define、on_action、迭代器、示例或修正值。",
	},
	"ck3_health": {
		Title:       "检查 CK3 索引健康状态",
		Description: "检查数据库、结构、索引与 MCP 注册是否可信。返回隐藏路径后的简短健康报告。",
	},
	"ck3_package": {
		Title:       "打包 CK3 Mod",
		Description: "严格验证模型生成的 CK3 文本与二进制文件，统一生成双描述文件，并在受限临时区创建可直接手动安装的 ZIP；不会安装或修改真实 Mod 目录。",
	},
	"ck3_gui": {
		Title:       "检查 CK3 GUI",
		Description: "通过现有索引检查生效中的 CK3 GUI 文件，解析跨文件继承、模板和区块覆盖，并输出有界 PNG 或自包含 HTML。检查器支持控件树、裁剪滚动视口、网格布局、英中本地化切换、已索引动态纹理样例与受控行为模拟；model_samples 可从唯一 item 模板实例化有界调用方列表行，绝不执行任意 Jomini 代码。",
	},
	"map_asset_audit": {
		Title:       "审计 CK3 地图资源",
		Description: "审计当前生效的 CK3 地图栅格，检查省份定义覆盖、PNG 编码、河流调色板索引语义和正交河道拓扑；吸收 AzgaarToCK3 的特色校验，但不重复 ck3-index 已有解析与几何。",
	},
	"map_province_mapping": {
		Title:       "比较省份地图版本",
		Description: "通过控制点 Delaunay 分片仿射变换比较两个已配置的 CK3 省份地图，返回像素交叠、重编号、拆分、合并、复杂及未映射分组；只提供迁移证据，不写地图或历史文件。",
	},
	"map_migration_snapshot": {
		Title:       "保存 CK3 地图迁移快照",
		Description: "在上游地图更新前，保存已配置旧上游、当前 Mod 文件清单、文本基线与有效地图的内容寻址快照；不接受任意路径，也不修改来源工程。",
	},
	"map_province_migration": {
		Title:       "迁移 CK3 省份地图改动",
		Description: "以新上游为底执行保守三方合并和省份语义改写，严格验证通过后才生成独立本地测试 Fork；冲突时只输出报告和 resolution 模板。",
	},
	"map_province_info": {
		Title:       "检查地图省份",
		Description: "检查一个省份的精确几何、头衔、脚本地形、实际地表材质混合、纹理资源与直接边界。返回只读的精确上下文和分类后的邻省。",
	},
	"map_physical_context": {
		Title:       "检查物理地理",
		Description: "只读检查归一化高程、地形、gfx/map/terrain 地表材质混合及纹理资源、复合河流、水体、相对海床深浅与物理障碍；明确区分 CK3 原生观察事实、GIS 派生值和综合推断。",
	},
	"map_neighbors": {
		Title:       "检查地图邻域",
		Description: "检查某省份或领地头衔周围的受限图邻域。返回按半径分组的方向、距离与边界分类。",
	},
	"map_spatial_relation": {
		Title:       "比较地图省份",
		Description: "比较两个省份的精确空间关系。返回质心偏移、方位角、距离、直接边界与附近障碍。",
	},
	"map_strategic_passages": {
		Title:       "检查战略通道",
		Description: "把显式邻接与像素边界邻省分开检查。返回海峡、渡口、地下连接与离图通道。",
	},
	"map_title_context": {
		Title:       "检查地图头衔",
		Description: "检查一个领地头衔的省份覆盖、持有者、文化、信仰与相邻头衔。返回只读的历史和视觉上下文。",
	},
	"map_assignment_plan": {
		Title:       "规划地图分配",
		Description: "生成仅供审查的宗教或占位角色分配建议。private 模式返回补丁预览，visibility=public 时会移除预览。",
	},
	"map_building_candidates": {
		Title:       "评估地图建筑候选地",
		Description: "为省份或领地头衔排列可审计的特殊建筑候选地。返回地形、地产、水域、文化与边界证据，不写入文件。",
	},
	"map_recipe_catalog": {
		Title:       "列出地图配方",
		Description: "列出支持的地图配方、层级、变换、图层、调色板与使用建议。构建自定义指标或渲染规格前应先调用此工具。",
	},
	"map_build_metric": {
		Title:       "构建地图指标",
		Description: "在渲染前构建可审计的索引指标或带来源说明的地图指标。返回数值、分位数、异常值、来源与警告。",
	},
	"map_route": {
		Title:       "计算地图路线",
		Description: "解析中英文地名、领地头衔或省份 ID，并在已索引省份拓扑上计算确定性的合法陆路、海路或混合路线；返回紧凑路径、分段、沿途上下文和像素距离警告。",
	},
	"map_render": {
		Title:       "渲染 CK3 地图",
		Description: "渲染只读的自适应 CK3 地图；省略尺寸时自动选择分辨率。返回结构化元数据和内存中的 PNG，不接受客户端文件路径。",
	},
}

var chineseFieldDescriptions = map[string]string{
	"Target selector family.": "目标选择类型。",
	"One numeric province id, landed-title id, region:<id>, exact region id with target_type=region, or all.":                            "一个数字省份 ID、领地头衔 ID、region:<id>、配合 target_type=region 使用的准确地区 ID，或 all。",
	"Up to 16 province, title, or region:<id> targets.":                                                                                  "最多 16 个省份、头衔或 region:<id> 目标。",
	"Include a bounded coast-to-adjacent-water aggregate. Lakes and major-river provinces remain separate from the ocean-depth verdict.": "包含受限的沿岸至相邻水体聚合；湖泊和大河省份不会进入海洋深度结论。",
	"Physical geography view.": "要查询的物理地理视图。",
	"Physical geography view. surface returns observed material blend weights plus configured mask and DDS resources without requiring WhiteboxTools.": "要查询的物理地理视图；surface 返回观测到的材质混合权重以及配置引用的 mask 与 DDS 资源，不依赖 WhiteboxTools。",
	"Configured current Mod source name.":                   "已配置的当前 Mod 来源名称。",
	"Configured old-upstream source name.":                  "已配置的旧上游来源名称。",
	"Configured new-upstream source name.":                  "已配置的新上游来源名称。",
	"Persistent id returned by map_migration_snapshot.":     "map_migration_snapshot 返回的持久快照标识。",
	"Optional safe directory name for the local test fork.": "本地测试 Fork 的可选安全目录名。",
	"Aggregation level.":                                    "聚合层级。",
	"Asset family to audit.":                                "要审计的地图资源类别。",
	"Assignment family.":                                    "分配类别。",
	"Built-in recipe id from map_recipe_catalog.":           "来自 map_recipe_catalog 的内置配方标识符。",
	"Center object or referenced id.":                       "中心对象或被引用标识符。",
	"Center object or referenced id. event_chain accepts event:<id>, on_action:<id>, or an untyped event id.": "中心对象或被引用标识符；event_chain 接受 event:<id>、on_action:<id> 或无类型事件标识符。",
	"CK3 history year.": "CK3 历史年份。",
	"Map subject: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name.":        "地图地点：数字省份 ID、b_/c_/d_/k_/e_ 头衔 ID，或可唯一解析的准确英文或中文本地化名称。",
	"Source map subject: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name.": "来源地图地点：数字省份 ID、b_/c_/d_/k_/e_ 头衔 ID，或可唯一解析的准确英文或中文本地化名称。",
	"Target map subject in the same forms as from.":                                                                           "目标地图地点，接受与 from 相同的形式。",
	"CK3 id, localized text, resource path, diagnostic code, or semantic prefix.":                                             "CK3 标识符、本地化文本、资源路径、诊断代码或语义前缀。",
	"CK3 script-history year.":                                     "CK3 脚本历史年份。",
	"Complete source-root-relative files analyzed only in memory.": "仅在内存中分析、相对于来源根目录的完整文件。",
	"Configured source-map name, or active.":                       "已配置的来源地图名称，或 active。",
	"Configured target-map name, or active.":                       "已配置的目标地图名称，或 active。",
	"Diagnostic view.":                                             "诊断视图。",
	"Dependency view. neighborhood defaults to at most two hops; event_chain supports up to six.":                                                          "依赖视图；neighborhood 最多两跳，event_chain 最多六跳。",
	"Response representation. html is only available for event_chain and adds an interactive no-network HTML inspector alongside the structured topology.": "响应表示形式；html 仅适用于 event_chain，会在结构化拓扑旁附带可交互、无网络请求的 HTML 检查器。",
	"Displayed atlas year.": "地图册显示年份。",
	"Deprecated alias for year; conflicting values are rejected.": "year 的废弃兼容别名；两者数值冲突时拒绝调用。",
	"Engine or script key.":                "引擎键或脚本键。",
	"Exact CK3 id, key, or resource path.": "准确的 CK3 标识符、键或资源路径。",
	"Inspection view.":                     "检查视图。",
	"Optional configured higher-precedence source for operation=compare. Defaults to the configured project/highest-priority layer in private visibility.": "operation=compare 时可选的已配置高优先级来源；私有可见性下默认使用当前 Mod/最高优先级层。",
	"Optional configured lower-precedence base source for operation=compare. Defaults to the nearest lower-precedence layer.":                              "operation=compare 时可选的已配置低优先级基线来源；默认使用最近的低优先级层。",
	"Landed-title id.":                                    "领地头衔标识符。",
	"Maximum evidence items per section.":                 "每个结果分区最多返回的证据项数。",
	"Maximum markers, labels, or passages for the layer.": "该图层最多绘制的标记、标签或通道数。",
	"Numeric province id.":                                "数字省份标识符。",
	"Numeric source province id.":                         "数字来源省份标识符。",
	"Numeric target province id.":                         "数字目标省份标识符。",
	"Object id, object type, or type:term.":               "对象标识符、对象类型或 type:term。",
	"Optional bounded regular expression for exact namespace filtering, e.g. ^c_c[0-9]+$.": "可选的受限正则表达式，用于准确筛选命名空间，例如 ^c_c[0-9]+$。",
	"Optional confidence filter.": "可选的置信度筛选器。",
	"Optional diagnostic source.": "可选的诊断来源。",
	"Optional entity-id prefix filter, e.g. c_c for a custom county namespace.": "可选的实体标识符前缀筛选器，例如用 c_c 筛选自定义伯爵领命名空间。",
	"Optional evidence category.": "可选的证据类别。",
	"Optional explicit output height. Omit width and height for automatic sizing.":                                     "可选的明确输出高度；同时省略 width 与 height 时自动确定尺寸。",
	"Optional explicit output width. Omit width and height for automatic sizing.":                                      "可选的明确输出宽度；同时省略 width 与 height 时自动确定尺寸。",
	"Optional indexed source name.":                                                                                    "可选的已索引来源名称。",
	"Optional passage family.":                                                                                         "可选的通道类别。",
	"Optional geographic control-point pairs used to build a piecewise-affine warp.":                                   "可选的地理控制点对，用于建立分片仿射变换。",
	"Optional source-root-relative path prefix.":                                                                       "可选的来源根目录相对路径前缀。",
	"Origin: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name.":      "起点：数字省份 ID、b_/c_/d_/k_/e_ 头衔 ID，或可唯一解析的准确英文或中文本地化名称。",
	"Destination: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name.": "终点：数字省份 ID、b_/c_/d_/k_/e_ 头衔 ID，或可唯一解析的准确英文或中文本地化名称。",
	"Traversal mode.":  "通行模式。",
	"Route objective.": "路线目标。",
	"Optional exact map subjects that the route must visit in order.":                                              "路线必须按顺序经过的可选准确地图地点。",
	"Source-map corridor radius used to select nearby context.":                                                    "用于选择邻近上下文的来源地图走廊半径。",
	"Political context level returned with the corridor.":                                                          "随路线走廊返回的政治上下文层级。",
	"Preferred context-label language.":                                                                            "首选的上下文标签语言。",
	"Maximum graph nodes expanded before returning a bounded failure.":                                             "返回受限失败前最多展开的图节点数。",
	"Include bounded graph-load and expansion evidence.":                                                           "包含受限的图加载与节点展开证据。",
	"Ordered route and endpoint province ids to include in the render viewport.":                                   "应纳入渲染视口的有序路线与端点省份 ID。",
	"Expand a route into a bounded county- or duchy-level map corridor instead of rendering isolated route nodes.": "把路线扩展为受限的伯爵领或公国级地图走廊，而非只渲染孤立路线节点。",
	"Source-map route corridor radius.":                                                                            "来源地图中的路线走廊半径。",
	"Political context expansion level.":                                                                           "政治上下文扩展层级。",
	"Include full metric values and recipe targets. Route renders default to compact metadata.":                    "包含完整指标值和配方目标；路线渲染默认返回紧凑元数据。",
	"Outer map padding in final-output pixels.":                                                                    "以最终输出像素计的地图外边距。",
	"Preflight target.": "预检目标。",
	"Minimum source or target overlap share retained as a mapping edge.": "保留为映射边的最小来源或目标交叠比例。",
	"Maximum target candidates returned per source province.":            "每个来源省份最多返回的目标候选数。",
	"Allow land provinces to map to water provinces and vice versa.":     "允许陆地省份与水域省份互相映射。",
	"Preparation view.":     "准备视图。",
	"Primary render level.": "主要渲染层级。",
	"Province id, landed-title id, comma-separated targets, or all.":            "省份标识符、领地头衔标识符、逗号分隔的多个目标或 all。",
	"Province or landed-title id.":                                              "省份或领地头衔标识符。",
	"Province or landed-title target.":                                          "省份或领地头衔目标。",
	"Province/title id, comma-separated ids, or all.":                           "省份或头衔标识符、逗号分隔的多个标识符或 all。",
	"Reference family.":                                                         "参考资料类别。",
	"event_chain traversal direction.":                                          "event_chain 的遍历方向。",
	"Whether event_chain includes on_action nodes and edges. Defaults to true.": "event_chain 是否包含 on_action 节点和边；默认为 true。",
	"Required for operation=explain.":                                           "operation=explain 时必填。",
	"Required for operation=subject.":                                           "operation=subject 时必填。",
	"Required when values are model supplied.":                                  "由模型提供 values 时必填。",
	"Traversal depth.":                                                          "遍历深度。",
	"Traversal depth. event_chain defaults to 3 and caps at 6; neighborhood defaults to 1 and caps at 2.": "遍历深度；event_chain 默认为 3、最多 6，neighborhood 默认为 1、最多 2。",
	"Traversal radius.": "遍历半径。",
	"Use public to remove current-project and patch evidence from the result.": "设为 public 可从结果中移除当前工程与补丁证据。",
	"Workspace view.": "工作区视图。",
	"GUI view.":       "GUI 查询视图。",
	"Source-root-relative gui/*.gui path for operation=file.":                                 "operation=file 使用的、相对于来源根目录的 gui/*.gui 路径。",
	"Optional source-root-relative gui/ prefix for summary/type/template resolution.":         "汇总或解析类型与模板时使用的可选 gui/ 相对路径前缀。",
	"Exact custom type or template name for operation=type/template.":                         "operation=type/template 使用的精确自定义类型或模板名称。",
	"Optional source-root-relative gui/ prefix for summary/type/template/preview resolution.": "汇总、解析类型或模板或生成预览时使用的可选 gui/ 相对路径前缀。",
	"Optional source-root-relative gui/ prefix that scopes symbol selection. Type/template dependencies still resolve against every active GUI file; responses report files and resolution_files separately.": "限定符号选择范围的可选 gui/ 源根相对前缀；类型和模板依赖仍会对全部活动 GUI 文件解析，响应分别报告 files 与 resolution_files。",
	"Exact custom type or template name for operation=type/template/preview.":                                              "operation=type/template/preview 使用的精确自定义类型或模板名称。",
	"Exact custom type, template, or named GUI element for preview; type/template operations keep their narrower meaning.": "preview 可使用精确的自定义类型、模板或具名 GUI 控件；type/template 保持原有窄语义。",
	"Optional preview width in pixels.":  "可选的 GUI 预览宽度（像素）。",
	"Optional preview height in pixels.": "可选的 GUI 预览高度（像素）。",
	"Preview representation. png preserves the legacy response; html returns a standalone document; both returns both.":                                                                                                                  "预览表示形式：png 保持旧响应；html 返回独立文档；both 同时返回二者。",
	"HTML behavior. static is script-free; inspector adds a fixed CSP-hashed tree, zoom, search, clipped scrollbox navigation, property inspector, and visual-state simulator. Only valid with format=html or both.":                     "HTML 行为模式：static 完全无脚本；inspector 使用固定 CSP 哈希脚本提供控件树、缩放、搜索、裁剪滚动视口、属性检查和视觉状态模拟。仅适用于 format=html 或 both。",
	"Initial GUI localization view. raw preserves script keys; English, Simplified Chinese, and bilingual values come only from the active localization index. The inspector can switch among embedded variants without network access.": "初始 GUI 本地化视图：raw 保留脚本 key；英文、简体中文和双语值只来自当前生效的本地化索引。检查器可离线切换已嵌入的语言变体。",
	"Optional caller-provided example results for exact GUI expressions. Values are labeled provided, never observed, and unmatched expressions are reported.":                                                                           "可选的调用方样例表达式结果；数值明确标记为 provided 而非游戏观察事实，未命中的表达式会被报告。",
	"Optional caller-provided example results for exact GUI expressions. Values are labeled provided, never observed, and unmatched expressions are reported. Texture samples must name an indexed source-root-relative gfx asset.":      "可选的调用方精确 GUI 表达式样例结果；数值明确标记为 provided 而非游戏观察事实，未命中表达式会被报告，纹理样例必须指向已索引的 gfx 源根相对资源。",
	"Optional bounded datamodel row samples. Each collection must exactly select one fixedgridbox or dynamicgridbox with one item template. At most 32 rows are accepted across all collections.":                                        "可选的有界数据模型行样例；每个集合必须精确选中一个仅含单一 item 模板的 fixedgridbox 或 dynamicgridbox，全部集合最多接受 32 行。",
	"Optional caller-provided atomic facts for bounded And/Or/Not and comparison evaluation. Facts are labeled provided rather than observed; missing facts remain unknown.":                                                             "可选的调用方原子事实，用于受限的 And/Or/Not 和比较求值；事实标记为 provided 而非游戏观察值，缺失事实保持 unknown。",
	"Optional caller-provided postconditions for exact unsupported onclick expressions. The expression is never executed; only typed fact updates are replayed and labeled provided.":                                                    "可选的调用方点击后置事实；只匹配未受内置支持的精确 onclick，表达式本身绝不执行，类型化事实更新明确标记为 provided。",
	"Exact unsupported onclick expression, including its Jomini call shape.":                                                                                                                                                             "完整的未受内置支持 onclick 表达式，包括 Jomini 调用形状。",
	"Bounded fact updates applied atomically by the HTML simulator.":                                                  "由 HTML 模拟器原子应用的有限事实更新。",
	"Exact atomic fact expression to update after the matching click.":                                                "匹配点击后要更新的精确原子事实表达式。",
	"Declarative update operation. toggle requires a known boolean value; set requires value.":                        "声明式更新操作；toggle 要求已知布尔值，set 要求提供 value。",
	"Required for set and forbidden for toggle.":                                                                      "set 操作必须提供，toggle 操作禁止提供。",
	"GUI property whose expression result is sampled.":                                                                "要提供表达式样例结果的 GUI 属性。",
	"Exact indexed GUI expression or localized text key to match.":                                                    "要精确匹配的已索引 GUI 表达式或本地化文本 key。",
	"Example display text, or true/false for visible and enabled.":                                                    "样例显示文本；visible 和 enabled 使用 true 或 false。",
	"Example display text, indexed source-root-relative gfx path for texture, or true/false for visible and enabled.": "样例显示文本、纹理使用的已索引 gfx 源根相对路径，或 visible 与 enabled 使用的 true/false。",
	"Provided row text, indexed source-root-relative gfx path for texture, or true/false for visible and enabled.":    "调用方提供的行文本、纹理使用的已索引 gfx 源根相对路径，或 visible 与 enabled 使用的 true/false。",
}

var chineseSchemaTypes = map[string]string{
	"array": "数组", "boolean": "布尔值", "integer": "整数", "number": "数值", "object": "对象", "string": "字符串",
}

func validateChineseCatalog(tools []mcpserver.ToolDocumentation) error {
	for _, tool := range tools {
		text, ok := chineseToolTexts[tool.Name]
		if !ok || strings.TrimSpace(text.Title) == "" || strings.TrimSpace(text.Description) == "" {
			return fmt.Errorf("missing Chinese tool text for %s", tool.Name)
		}
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		for name, raw := range properties {
			property, _ := raw.(map[string]any)
			description := schemaText(property, "description")
			if description != "" && chineseFieldDescriptions[description] == "" {
				return fmt.Errorf("missing Chinese field description for %s.%s: %q", tool.Name, name, description)
			}
		}
	}
	return nil
}

func chineseSchemaType(property map[string]any) string {
	typeName := schemaText(property, "type")
	if translated := chineseSchemaTypes[typeName]; translated != "" {
		return translated
	}
	if _, ok := property["anyOf"]; ok {
		return "复合类型"
	}
	return "—"
}

func chineseBool(value bool) string {
	if value {
		return "是"
	}
	return "否"
}
