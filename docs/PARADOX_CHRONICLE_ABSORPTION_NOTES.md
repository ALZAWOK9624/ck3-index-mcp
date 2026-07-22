# Paradox Chronicle 吸收记录

## 上游快照

- 仓库：`DragonKnightOfBreeze/Paradox-Language-Support`
- 产品名：Paradox Chronicle（原 Paradox Language Support）
- 审阅提交：`fb92d0cbe1e894b17397793ec49b274ca5c3a220`
- 审阅日期：2026-07-15
- 许可证：MIT
- 重点文件：
  - `ParadoxEventTreeDiagramProvider.kt`
  - `ParadoxEventManager.kt`
  - `ParadoxEventService.kt`
  - `ParadoxCallerHierarchyTreeStructure.kt`
  - `ParadoxCalleeHierarchyTreeStructure.kt`

## 值得吸收的独有能力

Paradox Chronicle 会把事件调用整理成事件树，并分别提供调用者与被调用者层级。这比普通“查找所有引用”更适合回答事件链从哪里进入、会走到哪里、是否递归等问题。

本轮把这个思想接入现有规范工具 `ck3_dependencies`：

- `operation=neighborhood` 保留原有通用一至两跳依赖图。
- `operation=event_chain` 提供 `callers`、`callees`、`both` 三种方向。
- 输出事件和 on_action 的类型化节点及调用边，并保留现有 `relation`、`phase`、`confidence`、`resolution` 与来源证据。
- 在服务端直接计算根、叶、入度、出度、强连通循环和从中心出发的最短遍历链。
- 未解析调用只作为带原因的边返回，不虚构目标节点。
- 默认三跳、最多六跳，并同时受节点和边预算限制；结果明确标记截断。

## 复用而不是移植

ck3-index 已经拥有 CK3 脚本解析、对象索引、覆盖语义、类型化引用解析以及事件/on_action 的关系与阶段证据。因此本轮只增加拓扑解释层，不移植 Chronicle 的 IntelliJ PSI、CWT 配置解析、引用搜索器或图形界面。

这使吸收后的能力比上游裸调用边更适合模型：模型无需重新拼图，也不会因为另建事件数据库而得到与普通引用查询相互矛盾的答案。

## 明确排除

- 不引入 IntelliJ Platform、Kotlin 或 IDE 插件运行时依赖。
- 不把 CWT 配置当作 ck3-index 的第二套事实来源。
- 不复制 Chronicle 的编辑器导航、补全、颜色预览或 Diagram UI。
- 不增加新的规范 MCP 工具名；扩展现有 `ck3_dependencies`，避免增加 QQ 会话的工具目录负担。
- 不吸收归档兼容数据，也不把 Chronicle 尚未处理完整的 inline script、继承或动态表达式推断成确定调用。

## 测试边界

- 事件到事件的调用者/被调用者遍历。
- 事件与 on_action 混合拓扑及过滤。
- 双节点循环与自循环。
- 未解析事件调用保留证据但不生成假节点。
- 最短遍历链、深度边界、确定性排序和公开模式脱敏。
- MCP 合约与真实调用冒烟测试。
