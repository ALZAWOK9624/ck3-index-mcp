# CK3 GUI 受限运行时事实

`ck3_gui operation=preview` 的 inspector HTML 可以把调用者提供的原子事实组合成可交互的 `visible` / `enabled` 结果，把相同事实插入受支持的动态文本与 tooltip，并把直接数值事实绑定到 `progresspie` / `progressbar` 的 `value`。目标是减少“模型为每条完整表达式逐个填写答案”的编排成本，同时保持明确的安全边界。

## 请求

```json
{
  "operation": "preview",
  "symbol": "ingame_topbar",
  "format": "html",
  "html_mode": "inspector",
  "language": "bilingual",
  "runtime_facts": [
    {"expression": "IsPauseMenuShown", "value": false},
    {"expression": "IsDefaultGUIMode", "value": true},
    {"expression": "GetPlayer.IsValid", "value": true},
    {"expression": "GetPlayer.MakeScope.Var('hide_ui_top_bar').GetValue", "value": 0}
  ],
  "width": 1600,
  "height": 900,
  "limit": 300,
  "visibility": "private"
}
```

`runtime_facts` 最多 64 项。`value` 只能是 JSON 布尔、数字或字符串。事实是调用者提供的审查输入，返回结果固定标记 `source=provided`，不能描述成从存档或游戏进程观测到的状态。

## 求值模型

生成核心只编译以下封闭集合：

- `And(...)`、`Or(...)`、`Not(...)`。
- `EqualTo_*`、`NotEqualTo_*`、`LessThan_*`、`LessThanOrEqualTo_*`、`GreaterThan_*`、`GreaterThanOrEqualTo_*`。
- 布尔、字符串、数字，以及 CK3 常见的 `'(int32)1'` / `'(CFixedPoint)0'` 数字字面量。

引擎属性、作用域链和未知调用会成为原子事实，不会被执行。缺失值使用三值逻辑：`And(false, unknown)` 为 `false`，`Or(true, unknown)` 为 `true`，其余无法证明的结果保持 `unknown`。不平衡括号或超出白名单的组合进入 `unsupported`，不会阻止生成诊断预览。

表达式被编译为生成器控制的 RPN token。HTML 只读取这些 token 和事实控件；它不使用 `eval`、动态脚本、网络请求或浏览器存储。GUI 输入字符串不会进入 JavaScript 源码。

## 数值 value 与进度控件

`progresspie`、名称包含 `progressbar` 的控件，以及其他带 `value` 属性的节点会保留原始表达式。`min`、`max` 和 `value` 分别编译为强类型数值计划。当前数值计划只接受数值字面量或一个精确的原子事实，例如 `[PdxGuiWidget.GetTooltipLockProgress]`；它不会执行算术、作用域切换或任意 Jomini 调用。

当事实是有限 JSON 数字时，检查器保留原始数值作为运行时结果，并按 `(value-min)/(max-min)` 归一化视觉值；未声明范围时才默认使用 `0..1`：

- `progresspie` 使用确定性的圆形遮罩，`0.62` 对应 `223.2°`。
- `progressbar` 使用确定性的横向裁剪；例如 `min=0, max=100, value=35` 显示 35%，不会误钳成满格。
- 横向进度条会从 `progress_textures` block 解析并嵌入 `progresstexture` 与 `noprogresstexture`，分别作为裁剪填充和未填充底槽；普通 overlay 仍按源码叠加。
- 小于 `min` 或大于 `max` 的值只在视觉层钳制；原始值仍留在节点元数据中供审查。
- 缺失、字符串、非数值事实或无效的 `max<=min` 保持 `unknown`，不会伪造为 0。

“Expression facts” 面板修改数值后，所有引用同一事实的进度控件、动态文本和条件会一起重算。事实仍是 `source=provided` 的审查输入，不是从 CK3 进程观测到的游戏状态。

## 动态文本与 tooltip

生成器会把 `[Character.GetName]`、`[MagicValue|1]`、`[MonthlyIncome|+=2]` 和 `$MACRO$` 编译成去重的文本 token 计划。事实值改变后，所有引用同一事实的控件文字和 tooltip 会一起重算。`|0`～`|4` 提供固定小数位，`|+=0`～`|+=4` 额外显示正号；CK3 的颜色、强调和图标标记只影响游戏富文本，在 HTML 中会被安全移除。没有完整 `tooltipwidget` 子树时，重算后的 tooltip 会进入固定纯文本悬浮面板，而不是只作为隐藏元数据。

条件文本支持 `SelectLocalization(condition,true,false)`、`Select_CString(condition,true,false)`、`AddTextIf(condition,text)` 和 `AddLocalizationIf(condition,localization)`。条件沿用同一布尔编译器；分支最多嵌套四层，总 token 不超过 128。求值是惰性的：未选择分支缺少事实不会污染当前结果。静态 localization key 会加入活动索引的英中闭包；动态分支继续作为调用者可编辑的字符串事实，不会被猜测。错误参数数量、非布尔条件和超深结构保持 `unsupported`。

真实条件本地化案例为 `examples/ck3-gui-conditional-localization-example.html`。它来自 `gui/shared/buttons_icons.gui` 的 `button_open_inventory`，并默认提供 `Character.IsLocalPlayer=true`，因此首次打开即可看到真实背包按钮。把 `Character.IsLocalPlayer` 改为 `false` 后，按钮显隐、禁用状态和 `OPEN_INVENTORY_TOOLTIP` / `EMPTY_INVENTORY_TOOLTIP` 英中分支会一起重算。案例还验证了无尺寸 `modify_texture` 填充父按钮、`blend_mode=add` 的固定合成和父图标 alpha 遮罩：

```powershell
ck3-index --config ck3-index.toml gui preview button_open_inventory gui/shared/buttons_icons.gui --format both --html-mode inspector --language bilingual --width 640 --height 360 --limit 50 --scenario examples/ck3-gui-conditional-localization-example.json --out examples/ck3-gui-conditional-localization-example.png --html-out examples/ck3-gui-conditional-localization-example.html
```

英文和简体中文本地化分别保留自己的模板，语言切换会选择对应计划。已在当前索引中定义的静态概念或嵌套宏会先按语言有界展开，例如 `[aspect_blood]`、`[magic|E]` 和 `$blood_name$`；颜色指令只影响格式，不会成为运行时事实。缺失或动态事实显示 `<unknown>` 并进入 `missing_facts`，不使用占位人物名或虚构数值。超过 2048 字符、128 个 token、四层嵌套、256 个嵌套 key、未闭合标记或不支持的格式会保持部分解析或标记为 `unsupported`。

## 与 sample_values 的关系

- `runtime_facts`：共享原子输入，一个变化可联动所有引用节点。
- `sample_values`：精确匹配整条表达式或文本 key，适合固定一个可复现审查结果。
- 两者同时命中同一属性时，`sample_values` 只对该属性优先；其他属性仍可由运行时事实求值。

HTML 的 “Expression facts” 面板允许把事实改成 `true`、`false`、数值、字符串或空白 `unknown`。所有绑定节点会同步重算。`Reset simulation` 会恢复请求中的初始事实。

## 受限点击动作

如果节点的 `onclick` 是单一 `OpenGameView('id')`、`CloseGameView('id')` 或 `ToggleGameView('id')`，生成器会将它编译成对 `IsGameViewOpen('id')` 布尔事实的更新。在 inspector 选中节点并点击 “Simulate click” 后，页面会更新该事实并重算所有绑定；Visual 模式下启用 `Replay clicks` 时，直接点击画布按钮也会执行相同的受限重放。`ToggleGameView` 在初始值为 `unknown` 时会拒绝猜测；`Open` / `Close` 可以给出确定值。

静态变量系统也保留值而不只保留存在性。`GetVariableSystem.Set('tab', 'ritual')` 会原子更新 `GetVariableSystem.Exists('tab')=true` 与 `GetVariableSystem.Get('tab')='ritual'`；`Clear` 会把 Exists 设为 false 并清除 Get。`GetVariableSystem.HasValue('tab', 'ritual')` 编译为 Exists 与类型化相等比较，因此标签页、选中态和显隐层可以随点击一起切换。变量名和比较值必须是受限字面量；动态表达式不会被猜测执行。

动作参数只允许最多 128 个字符的单行引用字符串。复合效果、作用域写入、命令、事件和其他函数不会编译，仍然只显示原始表达式。

## CLI

场景文件可同时包含 `runtime_facts`、`sample_values` 和 `action_effects`。`action_effects` 只为完全匹配的复杂点击声明类型化后置事实，不执行点击表达式；详细约束见 `CK3_GUI_PRESSED_AND_CLICK_RUNTIME.md`：

```powershell
ck3-index --config ck3-index.toml gui preview ingame_topbar --format html --html-mode inspector --language bilingual --width 1600 --height 900 --limit 300 --scenario examples/ck3-gui-runtime-facts-example.json --html-out examples/ck3-gui-runtime-facts-example.html
```

真实 `progresspie` 案例使用 `gui/shared/cooltip.gui` 的 `tooltip_progress`、第二帧 `progress_circle.dds` 和数值事实 `PdxGuiWidget.GetTooltipLockProgress=0.62`：

```powershell
ck3-index --config ck3-index.toml gui preview tooltip_progress gui/shared/cooltip.gui --format both --html-mode inspector --language bilingual --width 400 --height 300 --limit 20 --scenario examples/ck3-gui-progress-runtime-example.json --out examples/ck3-gui-progress-runtime-example.png --html-out examples/ck3-gui-progress-runtime-example.html
```

真实横向范围案例使用 `gui/shared/progressbars.gui` 的 `progressbar_standard`，验证 `0..100` 范围、35% 裁剪、填充纹理、未填充纹理和 overlay：

```powershell
ck3-index --config ck3-index.toml gui preview progressbar_standard gui/shared/progressbars.gui --format both --html-mode inspector --language bilingual --width 600 --height 220 --limit 20 --out examples/ck3-gui-progressbar-range-example.png --html-out examples/ck3-gui-progressbar-range-example.html
```

这仍不是完整 Jomini 虚拟机。数据上下文切换、脚本列表、超出白名单的条件本地化、动画状态机、复杂数值表达式、动态纹理和未编译的点击效果必须在 CK3 中复核。
