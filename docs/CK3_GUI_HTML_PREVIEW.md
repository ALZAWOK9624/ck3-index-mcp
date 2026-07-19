# CK3 GUI HTML 预览与检查器

`ck3-index` 复用同一套 GUI 索引、覆盖解析和布局场景，把 CK3/Jomini GUI 转成三种互补表示：

- PNG：快速检查总体布局。
- `static` HTML：无脚本、确定性、便于归档和模型读取。
- `inspector` HTML：可搜索控件树、缩放/平移画布、属性检查器和受控行为模拟器。

文件覆盖、模板 `using`、自定义类型继承、实例展开、`blockoverride`、锚点、`hbox/vbox`、边距、资源存在性和运行时表达式都来自现有索引与解析流水线，不建立第二套 GUI 数据库。

## MCP

```json
{
  "operation": "preview",
  "symbol": "gh_magic_resource_widget",
  "path_prefix": "gui/GH_types_hud_types.gui",
  "format": "both",
  "html_mode": "inspector",
  "language": "bilingual",
  "sample_values": [
    {
      "property": "text",
      "expression": "event_window_widget_migration",
      "value": "当前移民贡献：87.5 / Current migration contribution: 87.5"
    }
  ],
  "width": 1280,
  "height": 720,
  "limit": 100,
  "visibility": "private"
}
```

`format` 支持 `png`、`html` 和 `both`。`html_mode` 仅在 `format=html|both` 时有效：

- `static`：默认兼容模式；完全无 JavaScript。
- `inspector`：包含一段由生成器固定、SHA-256 CSP 白名单约束的脚本；不包含 GUI 输入生成的代码。

`language` 支持 `raw`、`english`、`simp_chinese` 和 `bilingual`，默认 `raw` 以保持旧调用语义。英中值只来自当前生效的 localization 索引；检查器会把已绑定的变体一起嵌入页面，因此语言切换不会重新查询数据库或访问网络。部分 Mod 把多种语言放在同一自定义目录下，解析器会结合数据库语言字段、`_l_english.yml` / `_l_simp_chinese.yml` 文件名和来源优先级判定实际语言。

HTML 元数据示例：

```json
{
  "schema_version": "ck3-gui-html/v1",
  "mode": "inspector",
  "bytes": 28421,
  "sha256": "...",
  "node_count": 15,
  "scripts": true,
  "script_policy": "fixed-generator-script",
  "script_sha256": "sha256-...",
  "external_requests": false,
  "model_readable": true,
  "behaviors": {
    "visible_expressions": 1,
    "enabled_expressions": 0,
    "dynamic_texts": 2,
    "click_actions": 0,
    "states": 0,
    "scroll_viewports": 1,
    "model_rows": 6
  }
}
```

每个 `preview.nodes[]` 保留 `kind`、`type_chain`、`name`、父节点、深度、来源相对路径、行号、声明的 `declared_position` / `declared_size`、最终原生布局边界、纹理解析结果、`texture_frames`、`texture_slice`、规范化的 `texture_blend_mode`、`mirror` 和近似标记。`type_chain` 使解析成基础 primitive 的自定义类型仍能保留 `scrollbox` 等运行语义。`mirror=horizontal|vertical|vertical|horizontal` 会作为纹理层的水平、垂直或双轴翻转重放，而不会翻转节点坐标或文字。字面量 `framesize` 会建立图集网格：静态 `frame` 按零基索引选初始帧，按钮 `upframe/overframe/downframe/disableframe` 按一基索引在普通、悬停、按下和禁用状态间切换。`Corneredstretched` / `Corneredtiled` 结合 `spriteborder` 与 `texture_density` 通过九宫格边框拉伸或平铺；若纹理同时是多帧图集，嵌入器会先按帧裁出确定性 PNG，再让每个交互状态切换对应九宫格。无尺寸的 `modify_texture` 按父控件填充，已知 `add` / `screen` / `multiply` / `alphamultiply` / `overlay` / `colordodge` 使用固定 CSS 类近似合成，并以最近的父纹理 alpha 轮廓遮罩，避免把颜色图集画成不透明方块；未知模式不进入样式。`layout` 还保存受支持的流向、间距、边距、扩展策略、填充背景和 `ignoreinvisible`；`flowcontainer` 未声明 `direction` 时按 CK3 的横向默认值处理。`tooltipwidget` 子树标记为 `overlay`，不再进入常驻 HUD 的边界计算或 PNG 渲染。常用运行时字段放在 `semantics` 中：`visible`、`enabled`、`value`、`data_context`、`data_model`、`on_click`、`tooltip`、`raw_text` 和 `state`。

当 `text` 或 `tooltip` 命中真实本地化 key 时，节点还会返回 `text_localization` 或 `tooltip_localization`，包含英中原值、从同一生效索引有界展开后的 `resolved_value`、去除 CK3 颜色/图标标记后的 `display_text`、来源和所选语言。静态 `[aspect_blood]`、`[concept|E]` 与 `$nested_key$` 最多递归四层、总计 256 个 key；循环、缺失或动态表达式保持原样。预览阶段会把剩余受支持的 `[GetPlayer...]`、数值格式和 `$VALUE$` 编译成受限文本计划，并只从显式 `runtime_facts` 插值。`SelectLocalization`、`Select_CString`、`AddTextIf` 和 `AddLocalizationIf` 支持最多四层嵌套的惰性分支：只要求当前选中分支的事实，静态本地化分支从同一活动索引分别解析英中值，动态分支保持显式字符串事实；全部递归 token 最多 128 个。缺失的选中分支事实保持 `<unknown>`，未选分支不会把结果误标为不完整。`preview.localization` 汇总绑定、双语、部分解析、缺失目标语言和截断数量。

`sample_values` 最多 32 项，支持 `property=text|texture|visible|enabled`。每项只会精确匹配索引中保留的表达式或已绑定本地化 key：文本作为展示样例，纹理值必须是索引中存在的 `gfx/` 源根相对 PNG/DDS/TGA 路径，布尔属性只接受 `true`/`false`。纹理样例不会接受 URL、客户端文件路径或路径穿越；命中后仍由同一资源索引和嵌入预算验证。结果在 `preview.scenario` 和节点 `scenario` 中固定标记 `source=provided`，并返回每项 `matched_nodes`；零命中项进入 `unused` 和警告。工具不会把这些值描述成游戏截图或真实存档状态。

`model_samples` 用于真实 `fixedgridbox` / `dynamicgridbox` 列表。每个集合必须用 `target`、`datamodel` 或两者精确选中一个网格；目标网格必须只有一个 `item` 模板。工具会深复制该模板并实例化调用者给出的行，而不是猜测存档里的数据模型。契约限制为最多 8 个集合、每个集合 16 行、全部集合合计 32 行、每行 16 个精确 `text|texture|visible|enabled` 样例。行 `id` 在集合内唯一，并写入每个克隆节点的 `model_row`：

```json
{
  "model_samples": [
    {
      "datamodel": "[TraditionGrouping.GetPossibleTraditions]",
      "rows": [
        {
          "id": "stalwart_defenders",
          "samples": [
            {
              "property": "text",
              "expression": "[CultureTradition.GetNameNoTooltip]",
              "value": "坚毅守军 / Stalwart Defenders"
            },
            {
              "property": "texture",
              "expression": "[CultureTradition.GetLayeredIcon.GetTexture( '(int32)4' )]",
              "value": "gfx/interface/icons/culture_tradition/4-items/shield.dds"
            },
            {
              "property": "visible",
              "expression": "[ObjectsEqual( CultureTradition.Self, DivergenceWindow.GetSelectedTradition )]",
              "value": "true"
            }
          ]
        }
      ]
    }
  ]
}
```

结果通过 `preview.model_samples` 返回集合、行、每项命中数与 `unused_samples`；所有内容固定标记为 `source=provided`。检查器的树、属性面板和点击日志显示行 ID。手动修改同一表达式时，只联动所选行内部的节点，不会把第 1 行的 `CultureTradition.Self` 状态错误传播到其余克隆行。共享的 `runtime_facts` 仍是全局原子事实；若确实需要逐行不同值，应使用行内样例。

## 检查器能力

浏览器检查器提供：

1. 按真实父子深度缩进的控件树。
2. 按名称、种类和来源搜索，同时在画布中弱化无关节点。
3. 25%～200% 缩放、自动适配和空白区域拖拽平移。
4. 节点来源、行号、边界、纹理、数据上下文和表达式检查。
5. 对共享表达式的 `visible`、`enabled`、数值 `min` / `max` / `value`、动态文本和 `state` 进行一致的视觉状态模拟；数值事实可直接驱动 `progresspie` 圆形遮罩与 `progressbar` 横向裁剪，进度条按自身范围归一化而不是假定所有值都在 `0..1`。
6. 记录并重放白名单内的 `onclick` 视觉后果。单一 `OpenGameView`、`CloseGameView`、`ToggleGameView`，静态 `SetMapMode` 以及字面量 `GetVariableSystem.Toggle/Clear/Set` 可以触发全页重算；`HasValue` 会读取类型化变量值。Visual 模式默认允许直接点击画布按钮重放，关闭 `Replay clicks` 后只选择节点；其他点击仍只记录，不执行 CK3 效果。
7. 单节点或全局重置，保证审查过程可重复。
8. 在原始 key、英文、简体中文和双语之间即时切换；控件文字和属性面板同步更新。
9. 当 `hbox`、`vbox` 或 `flowcontainer` 使用 `ignoreinvisible=yes` 时，运行事实变化会按源码顺序重新计算可见子项；隐藏项退出画布布局，后续控件收拢，扩展项重新分配剩余空间。
10. `tooltipwidget` 作为独立覆盖层保留在控件树中；悬停拥有者时才在画布边缘安全范围内展开。没有 `tooltipwidget` 但存在已解析 tooltip 文本或运行时文本计划时，检查器使用固定的纯文本悬浮面板，并随语言和事实重算；两条路径互斥，移开后都会关闭。
11. 默认开启 `Visual`：去掉诊断网格、容器底色和普通边框，优先显示已嵌入纹理与真实文本；父节点隐藏会传播到整个子树。嵌入纹理会按节点 `mirror` 重放翻转，按帧字段响应悬停、按下和禁用状态，对 `spriteborder` 使用单帧或逐帧九宫格，并对已知 `modify_texture` 混合模式施加父纹理 alpha 遮罩。关闭 `Visual` 后恢复彩色类型框、近似边界和隐藏节点淡化，便于工程排错。

HTML 模式还会尝试把已由索引确认的 PNG 与 DDS 纹理解码成页面内 `data:` PNG。当前支持普通 PNG、DDS BC1/DXT1、BC2/DXT3、BC3/DXT5、对应的 DX10 BC1/BC2/BC3，以及带明确通道掩膜的 32 位无压缩 DDS；未压缩 DDS 会按头标志区分逐行 `pitch` 与整张图 `linear size`。同一资源先汇总全部节点的最大实际显示框，只在源图更大时用确定性 Catmull-Rom 降采样，不会放大；`texture_ref` 会同时报告嵌入 `width/height`、`source_width/source_height`、`resized` 和解码格式。读取仅使用索引数据库已经确认的资源路径，路径本身不会进入 HTML 或 MCP 输出。

`state` 块被标记为 `behavior_only`，不会再作为普通方框污染画布。检查器会保留状态名、`alpha` 与 `duration`，允许从控件树手动应用；标准 `_mouse_enter`/`_mouse_leave` 状态也会在浏览器指针进入或离开所属控件时重放。这里只处理可证明的透明度视觉结果，复杂动画模板和脚本动作仍保持未执行状态。

这里的“模拟”是对表达式结果的人工赋值和视觉后果重放。例如把 `[InDebugMode]` 设为 `false` 可以隐藏所有引用同一表达式的节点；工具不会解释 `[InDebugMode]` 为什么为真，也不会运行 `[SelectCharacter]`。

## 滚动视口与长文本

解析后的 `scrollbox` 现在按纵向流式视口处理，即使它最终继承解析成 `scrollarea` primitive，也会通过 `type_chain` 保留视口语义。`block`、`blockoverride` 和模板壳不会再占据一个假的流项目；真正的 `scrollwidget` / `scrollbox_content` 子项会依次排列，滚动条、背景和渐隐装饰不参与内容流。视口外内容不再扩大整张预览的边界；`allow_outside=yes` 子树也不会虚增滚动宽高。PNG 与无脚本 HTML 会显示初始裁剪结果，交互检查器则提供鼠标滚轮和受限纵向滑条。嵌套滚动视口会叠加裁剪，背景填充项保持固定，只有内容流随偏移移动。

`preview.nodes[].layout` 会报告 `scroll_viewport`、`scroll_direction`、`scroll_content_width`、`scroll_content_height` 和 `scroll_step`；HTML 行为摘要通过 `scroll_viewports` 报告数量。带 `autoresize=yes` 的多行文本同时保留宽高约束，浏览器在语言、运行时事实或手工样例文本变化后重新测量文本高度，再重新计算滚动范围。测量高度有固定上限，不会执行 GUI 表达式。

真实结构样例为 `examples/ck3-gui-scrollbox-runtime-example.html`，对应诊断图为 `examples/ck3-gui-scrollbox-runtime-example.png`。它来自 `character_window_bio`，用于验证 `scrollbox_content` 的结构壳展开、275 像素文本约束、运行时长文本重排和滚动视口元数据。数据模型产生的行数仍不会凭空实例化；需要审查特定列表内容时，必须通过有界 `model_samples` 明确提供行数据。

```powershell
ck3-index --config ck3-index.toml gui preview character_window_bio gui/GH_types_character_bio.gui --format both --html-mode inspector --language bilingual --width 600 --height 360 --limit 200 --scenario examples/ck3-gui-scrollbox-runtime-example.json --out examples/ck3-gui-scrollbox-runtime-example.png --html-out examples/ck3-gui-scrollbox-runtime-example.html
```

真实列表样例为 `examples/ck3-gui-datamodel-rows-example.html`，对应输入与诊断图为同名 `.json` / `.png`。它来自 CK3 的 `vbox_diverge_traditions_list`，使用真实的 `[TraditionGrouping.GetPossibleTraditions]`、`item` 子树、两列步长、名称/花费/选择光晕/阻止警告表达式和原始点击链。`path_prefix` 只限定待选符号，而 130 个活动 GUI 文件仍参与依赖解析；深层 `blockoverride` 插入的 `widget_tradition_selection_buttons` 会继续递归展开。样例实例化 4 行调用者提供的审查数据，并为五层动态传统图标提供真实已索引 DDS。当前案例共解析并嵌入 39/39 个纹理引用，38 个节点按最大显示框复用降采样资源，5 个控件使用多帧图集，7 个控件使用九宫格，其中滚动条滑块实际组合了 2 帧图集与逐帧九宫格；生成的 207 节点检查器约 699 KiB：

该样例同时是坐标回归案例：四个 `220×184` 原生卡片必须落入稳定的两列网格；同一卡片的 `background`、左右 pattern、`support` 与 `items` 必须共享原点和完整图标边界，90% `stroke` 必须按父框居中。只声明 `parentanchor` 时，预览器会把它同时作为隐式 `widgetanchor` 使用；显式 `widgetanchor` 仍优先。普通控件的字面量零宽或零高是硬布局值，不再被默认尺寸覆盖；只有文本、`autoresize=yes` 或对应轴为 `expanding` 的元素把零尺寸视为自动测量请求。

```powershell
ck3-index --config ck3-index.toml gui preview vbox_diverge_traditions_list gui/window_diverge_culture.gui --format both --html-mode inspector --language bilingual --width 1400 --height 900 --limit 500 --scenario examples/ck3-gui-datamodel-rows-example.json --out examples/ck3-gui-datamodel-rows-example.png --html-out examples/ck3-gui-datamodel-rows-example.html
```

真实数值进度样例为 `examples/ck3-gui-progress-runtime-example.html`，对应输入与诊断图为同名 `.json` / `.png`。它使用 `gui/shared/cooltip.gui` 中的 `tooltip_progress`、第二帧 `progress_circle.dds` 和显式事实 `PdxGuiWidget.GetTooltipLockProgress=0.62`；生成器返回强类型 `number` 计划并把它重放为 `223.2°` 圆形遮罩。检查器中修改该事实会即时更新进度，而不会执行原始 Jomini 表达式：

```powershell
ck3-index --config ck3-index.toml gui preview tooltip_progress gui/shared/cooltip.gui --format both --html-mode inspector --language bilingual --width 400 --height 300 --limit 20 --scenario examples/ck3-gui-progress-runtime-example.json --out examples/ck3-gui-progress-runtime-example.png --html-out examples/ck3-gui-progress-runtime-example.html
```

真实横向进度样例为 `examples/ck3-gui-progressbar-range-example.html` 和同名 `.png`。它来自 `progressbar_standard`，解析 `min=0`、`max=100`、`value=35`，所以填充层裁剪为 35%；`progress_standard.dds`、`progress_red.dds` 与 `progress_overlay.dds` 三个真实资源分别作为填充、未填充底槽与覆盖层嵌入：

```powershell
ck3-index --config ck3-index.toml gui preview progressbar_standard gui/shared/progressbars.gui --format both --html-mode inspector --language bilingual --width 600 --height 220 --limit 20 --out examples/ck3-gui-progressbar-range-example.png --html-out examples/ck3-gui-progressbar-range-example.html
```

## CLI

导出交互检查器：

```powershell
ck3-index --config ck3-index.toml gui preview event_window_widget_migration gui/event_window_widgets/ --format html --html-mode inspector --language bilingual --width 1600 --height 900 --limit 300 --scenario examples/ck3-gui-scenario-example.json --html-out preview.html
```

同时导出 PNG 与检查器：

```powershell
ck3-index --config ck3-index.toml gui preview gh_magic_resource_widget gui/GH_types_hud_types.gui --format both --html-mode inspector --language simp_chinese --width 1600 --height 900 --limit 300 --out preview.png --html-out preview.html
```

若不指定输出文件，CLI 在 `preview.html.document` 返回完整文档。写入文件后，终端 JSON 省略正文，但保留模式、字节数、SHA-256、脚本策略和行为计数。

## 安全边界

- 静态模式只允许内联 CSS 和 `data:` 图像，不包含脚本、表单、链接或外部请求。
- 检查器只允许与 `script_sha256` 完全一致的生成器脚本；CSP 不使用 `unsafe-inline` 脚本。
- GUI 字符串只进入经过 HTML 转义的文本或 `data-ck3-*` 属性，不进入 JavaScript 源码。
- 样例场景最多 32 项，每个表达式和值最多 512 字符；只做精确匹配，不执行、拼接或解释调用方表达式。
- 数据模型样例最多 8 个集合、单集合 16 行、总计 32 行、单行 16 项；目标和 `datamodel` 只做字面匹配，歧义、重复行 ID、多 `item` 模板及嵌套目标会在生成前失败。
- 固定脚本不使用 `eval`、`fetch`、XHR、WebSocket、动态脚本或浏览器存储。
- 纹理源文件最多 16 MiB、最大边长 4096、最多约 420 万像素；单纹理和整份 HTML 还有独立内嵌预算，超限时保留占位诊断而不强行载入。
- 不暴露索引文件的本机绝对路径；节点数继续受 `limit` 和查询预算限制。
- 文档最大 1 MiB；相同输入生成相同字节与 SHA-256。完整展开的重复 tooltip 子树仍会逐行占用文档预算，因此大型数据模型案例应减少行数或缩小目标，不能靠提高安全上限掩盖膨胀。

## 模型工作流

1. 用 `summary` 确认 GUI 索引状态。
2. 用 `file`、`type` 或 `template` 缩小到目标定义。
3. 修改后调用 `preview + format=both + html_mode=inspector`。
4. 先看 PNG 总体布局，再在检查器的默认 `Visual` 模式检查接近游戏的纹理/文字层；关闭 `Visual` 后搜索控件、核对来源、父子传播和近似节点。
5. 优先用 `runtime_facts` 提供共享原子状态；在 “Expression facts” 面板切换值，检查所有引用节点的隐藏和禁用联动。缺失事实应保持 `unknown`。
6. 需要固定整条表达式、文本或动态纹理时传 `sample_values`；确认 `source=provided`、`unused=0`。纹理只提供已索引的 `gfx/` 相对资源；它仅对命中属性优先，不要把样例值写成游戏观察事实。
7. 需要浏览数据模型列表时传 `model_samples`；确认网格唯一、`resolved_grid` 正确、`unused_samples=0`，并在检查器中用行 ID 复核逐行文本、纹理、显隐、禁用和点击日志。不要把调用者给出的行描述成存档扫描结果。
8. 用语言下拉框核对英中本地化，并编辑运行事实检查动态文本、tooltip、数值进度与 `ignoreinvisible` 流式/网格布局是否同步重算；任何 `<unknown>`、`unsupported`、复杂数值表达式和复杂本地化仍必须继续在游戏中验证。
9. 在 CK3 中验证真正的数据上下文、动画、状态机、脚本值、动态纹理与点击效果。

## 已知限制

- 检查器不是完整 Jomini 解释器；它仅从显式 `runtime_facts` 组合 `And` / `Or` / `Not` 和类型化比较。未知引擎调用是原子事实，缺失值不会被猜测。
- BC4、BC5、BC6、BC7、TGA 和未提供样例的引擎动态纹理尚未内嵌；不支持或超限时保留解析状态与占位视觉。
- 多帧图集与九宫格可以组合重放，`progresspie` / `progressbar` 的范围归一化进度遮罩、填充/未填充纹理和普通 overlay 也可重放；但 `tintcolor`、着色器、混合模式、非线性或分段进度效果以及 CK3 对某些边缘平铺的精确采样仍由游戏运行时决定。
- 静态本地化 key 已支持英中绑定和切换，简单作用域值、宏和数值格式可由显式运行事实驱动；数据上下文切换、脚本列表、嵌套本地化选择、复杂动画/状态机、效果和动态纹理仍由游戏运行时决定。当前状态重放只覆盖明确的 `alpha` 与 `duration`。
- 动态重排只覆盖已解析的直接流式/网格子项、边距、间距、网格步长和横纵扩展策略。常见 `parentanchor` / `widgetanchor` 配对及隐式同锚点已确定性重放；`flipdirection` 目前只保留元数据并维持源码顺序，未提供的虚拟化列表行、复合锚点组合和引擎内部布局模板仍可能近似，必须结合 `approximate` 与游戏内结果复核。
- Tooltip 覆盖层会保留解析后的内容与纹理，并用拥有者边界选择左侧或右侧锚点；引擎模板定义的精确尖角、延迟、动画和多屏避让尚未模拟。
- `Visual` 只能隐藏诊断色并优先组合已解析素材；缺失纹理仍显示斜纹警告，无法解析的引擎模板不会被伪造成原版控件。
- 缺少引擎内置模板或外部自定义类型时会保留诊断与近似节点，不能据此声称像素级一致。

因此现在它已经具备“工程浏览、双语核对和共享事实驱动的行为试验”，但还不是“网页里的 CK3”。完整规则与安全边界见 `docs/CK3_GUI_RUNTIME_FACTS.md`。

## 复杂整屏样例与性能

真实 HUD 检查器样例位于 `examples/ck3-gui-hud-inspector-example.html`，对应诊断 PNG 为 `examples/ck3-gui-hud-inspector-example.png`。样例使用 1600×900 画布、300 个节点和双语本地化生成：

带受限表达式求值的新样例为 `examples/ck3-gui-runtime-facts-example.html`，其场景输入为 `examples/ck3-gui-runtime-facts-example.json`，对应诊断图为 `examples/ck3-gui-runtime-facts-example.png`。

```powershell
ck3-index --config ck3-index.toml gui preview ingame_topbar --format both --html-mode inspector --language bilingual --width 1600 --height 900 --limit 300 --out examples/ck3-gui-hud-inspector-example.png --html-out examples/ck3-gui-hud-inspector-example.html
```

`--width` 范围为 64～3840，`--height` 范围为 64～2160，`--limit` 范围为 1～500。超过 1 MiB 的自包含 HTML 会明确失败，不会静默截短；需要更大整屏时应先缩小节点范围或减少纹理预算。

相同纹理在页面资产区只嵌入一次，所有节点通过稳定 CSS 类复用，避免复杂整屏因重复 Base64 膨胀。画布默认只显示真实界面文本；控件名、种类和纹理名等诊断标签默认隐藏，可用 `Node labels` 开关显示。元素树始终保留全部名称与来源，因此隐藏画布标签不会损失工程浏览信息。

长驻 MCP 会按权威 `files.sha256`、路径前缀和隐私边界缓存完整 GUI 解析结果；返回的 `cache_hit` 表明是否命中。任何活动 GUI 文件哈希变化都会选择新缓存键，不会回退到旧数据库或旧文件集合。

仓库内全部 HTML 案例可用 `node tools/check_gui_html_examples.mjs` 一次验证：脚本只解析、不执行每份生成脚本，并核对精确 CSP SHA-256 与 1 MiB 文档上限。

2026-07-17 Windows x64、Ryzen 7 9700X 基准（3 次）：

| 场景 | 中位耗时 | 分配 |
| --- | ---: | ---: |
| 200 节点普通检查器 HTML | 565 µs/op | 1.01 MiB/op |
| 200 节点滚动检查器 HTML | 1.05 ms/op | 1.06 MiB/op |
| 300 个 `progresspie` 节点的布尔可见性与 `min` / `max` / `value` 计划 | 147 µs/op | 224 KiB/op |
| 最大合法 `model_samples`（2 个网格、32 行、约 320 节点，含模板复制、PNG 布局与检查器 HTML） | 15.0 ms/op | 7.10 MiB/op |
| 单个 288×140 未压缩 DDS 解码并内嵌 | 2.16 ms/op | 1.13 MiB/op |
| 同一 DDS 降采样为 144×70 后内嵌 | 2.41 ms/op | 1.82 MiB/op |
| 288×140 三帧图集降采样、逐帧 PNG 与九宫格状态准备 | 1.93 ms/op | 3.45 MiB/op |

数值进度计划在 300 个节点上约为 0.15 ms，因此不会成为模型预览的主要延迟。降采样增加约 0.25 ms 的单资源 CPU 成本；三帧九宫格相对单图仍会增加逐帧裁剪和 PNG 编码成本，但同一资源和帧网格每次查询只解码一次，并显著降低最终 HTML 传输和浏览器解析体积。逐行模型样例仍不会成为聊天回复速度瓶颈，真正的大头仍是索引读取、大量独立纹理与浏览器截图。
