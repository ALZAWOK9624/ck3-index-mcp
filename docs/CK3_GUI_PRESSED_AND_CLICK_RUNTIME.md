# CK3 GUI 按压态与多动作运行时

这一层让 HTML inspector 更接近 CK3 按钮的真实行为，但仍保持封闭、可审计的安全边界。

## 解析语义

- `down` 与 `selected` 会和 `visible`、`enabled` 一样编译为受限布尔计划。
- 结果为 `true` 时，HTML 分别应用按下态和选中态，并同步 `aria-pressed`。
- 缺少事实时保持 `unknown`，不会默认当作 `false`。
- 同一节点重复声明的 `onclick` 会按源码顺序保存在 `semantics.on_clicks[]`；兼容字段 `semantics.on_click` 仍保留旧行为，指向最后一条。
- `blockoverride` 中直接声明的 `texture`、`onclick`、`down` 等属性会注入其所属按钮；嵌套元素继续替换结构块内容。

## 可重放动作

当前只编译以下封闭动作：

- `OpenGameView('id')`
- `CloseGameView('id')`
- `ToggleGameView('id')`
- `ToggleGameViewData('id', data_expression)`
- `SetMapMode('id')`
- `GetVariableSystem.Toggle('id')`
- `GetVariableSystem.Clear('id')`
- `GetVariableSystem.Set('id', literal)`

前三种直接更新对应的 `IsGameViewOpen('id')` 事实。`ToggleGameViewData` 也只更新这个布尔事实；第二参数以 `data_expression` 元数据保留，绝不求值，也不会假装切换了真实游戏数据上下文。`SetMapMode` 把目标 `IsMapMode('id')` 设为真，并把当前预览内其他已知地图模式设为假。静态 `GetVariableSystem.Toggle/Clear/Set` 更新受限变量状态：字面量 `Set` 同时把 `GetVariableSystem.Exists('id')` 设为真，并把 `GetVariableSystem.Get('id')` 写成对应的布尔、数字或字符串；`Clear` 把 Exists 设为假并将 Get 恢复为 unknown。`GetVariableSystem.HasValue('id', literal)` 会被降为 Exists 与类型化相等比较，动态键和值仍然拒绝编译。

一次点击中有多条 `onclick` 时，受支持动作按源码顺序重放。未支持动作仍在 Click effects 和动作日志中显示，但不执行。例如真实 HUD 的旅行按钮会重放 `ToggleGameViewData('travel_planner', TravelPlan.GetID)`，同时把 `Character.ZoomCameraTo` 明确标为仅保留。inspector 的 Visual 模式默认启用 `Replay clicks`，直接点击画布中的按钮或其子图标即可重放受限动作；关闭该选项后恢复为只选择节点，也始终可以用 `Simulate click` 明确重放。

## 声明式复杂动作后置事实

无法安全通用解释的 `GetScriptedGui(...).Execute(...)` 等动作，可以由审查场景提供 `action_effects`：

```json
{
  "expression": "[GetScriptedGui('biodiversity_map').Execute(GuiScope.End)]",
  "updates": [
    {
      "expression": "GetScriptedGui('biodiversity_map').IsShown(GuiScope.End)",
      "operation": "set",
      "value": true
    }
  ]
}
```

生成器只把它绑定到预览内规范化后完全相同、且没有内置语义的 `onclick`。点击时先验证全部更新，再原子应用并重新计算界面；动作表达式本身永不执行。每个场景最多 32 个动作，每个动作最多 8 个 `set` 或 `toggle`。值只能是 JSON 布尔、数字或字符串；`toggle` 只能作用于已有且当前值已知的布尔事实。未命中动作进入 `unused_action_effects`，内置白名单动作不能被覆盖。

## 真实 HUD 样例

- `examples/ck3-gui-travel-runtime-example.html`：验证重复 `onclick`、`ToggleGameViewData` 和未执行的镜头动作。
- `examples/ck3-gui-find-title-runtime-example.html`：验证 `down`，以及同一次点击按源码顺序切换 GameView、清除展开菜单变量。
- `examples/ck3-gui-main-tab-runtime-example.html`：验证 `blockoverride` 将军事标签的纹理、tooltip、`down` 和 `ToggleGameViewData` 注入真实基础按钮。
- `examples/ck3-gui-map-mode-runtime-example.html`：验证真实调试地图模式按钮之间的互斥选择、横向默认 `flowcontainer`、`ignoreinvisible` 动态收拢，以及 `biodiversity_map` 的显式 ScriptedGui 后置事实。
- `examples/ck3-gui-lore-document-runtime-example.html`：验证一次点击按顺序执行变量 Toggle、Clear 和字面值 Set。

对应的 `.json` 文件是显式事实输入，`.png` 是同一解析场景的诊断图。

## 安全边界

- 不执行任意 Jomini、JavaScript 字符串、数据表达式或游戏效果。
- GameView 名称必须是单行、有限长度的静态字符串。
- VariableSystem 名称也必须是单行、有限长度的静态字符串。
- `SetMapMode` 只接受静态地图模式 ID；`MapMode.GetKey` 等动态参数保持日志态。
- `GetVariableSystem.Set` 的第二参数必须是静态字符串、布尔值或数字字面量。
- `ToggleGameViewData` 的数据表达式只允许单行且有长度上限。
- 固定脚本不使用 `eval`、网络、浏览器存储或动态脚本。
- 这证明的是“确定性翻译与受限状态重放”，不是完整 CK3 引擎等价性。

## 2026-07-17 性能基准

Windows x64、Ryzen 7 9700X：

| 场景 | 耗时 | 分配 |
| --- | ---: | ---: |
| 300 节点布尔运行时 | 135 µs/op | 130 KiB/op |
| 300 节点动态文本 | 251 µs/op | 166 KiB/op |
| 300 节点 `down` / `selected` / 多动作 | 223 µs/op | 141 KiB/op |
| 300 节点声明式复杂动作后置事实 | 322 µs/op | 195 KiB/op |
| 完整 inspector HTML | 843 µs/op | 597 KiB/op |

真实 HUD 的旅行、查找头衔与军事主标签样例从 CLI 冷启动到写出 HTML/PNG 均约 1–1.2 秒；查询后的纯编译与 HTML 生成远低于一毫秒到一毫秒量级。
