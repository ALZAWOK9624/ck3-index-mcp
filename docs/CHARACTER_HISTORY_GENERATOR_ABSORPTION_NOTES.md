# CK3 Character History Generator 吸收记录

## 上游快照

- 仓库：`jj248/CK3-Character-History-Generator`
- 审阅提交：`54898ef86d790882b6a0f4e78e42ba7e11ad235c`
- 提交说明：`Fixed Graphviz bug and updated UI`
- 提交日期：2026-04-19
- 许可证：MIT（Copyright 2025 Jamie Jones, Jaco Daan）
- 重点文件：`ck3gen/character.py`、`ck3gen/simulation.py`、`ck3gen/title_history.py`

## 值得吸收的能力

上游把 CK3 历史人物拆成两类数据：不会随时间变化的身份字段，以及用 `year.month.day` 标记的生卒、婚姻、特质变化和收养事件。它还显式维护父母、子女、配偶和王朝归属，用于家族树与头衔持有者时间线。

ck3-index 吸收的是这套数据边界，而不是上游生成算法：

- `object_fields` 增加 `date_key`，静态字段使用 `0`，日期块中的真实字段使用规范化整数日期。
- 日期字符串本身不再被误当成 `character` 字段，因此 `query_patterns character` 能返回 `birth`、`death`、`add_spouse`、`father` 等可生成模式。
- `QueryObject` 与 `ck3_inspect` 返回人物静态档案和按日期分组的时间线，并保留来源、行号、值形状和原始字段摘要。
- 父母、配偶、雇主、收养、王朝与家族进入现有 `refs`；人物历史中的 trait、culture、faith/religion 与 death_reason 引用复用原有类型解析，并附带关系名和日期阶段。
- `effect = { set_father/set_mother = ... }` 的直接子项随所在日期进入时间线，支持上游采用的收养表示。

## 复用而不是移植

ck3-index 继续使用自己的 Clausewitz/Jomini AST、文件覆盖语义、来源优先级、引用解析和诊断系统。没有引入第二个人物数据库；已有 `map_characters` 与 `map_character_history` 仍是地图查询的派生缓存，通用人物事实以 `objects`、`object_fields` 和 `refs` 为准。

## 明确排除

- 不吸收上游基于正则表达式解析嵌套 PDXScript 的实现。
- 不吸收随机寿命、死亡原因、婚配、生育、收养概率或模拟循环。
- 不吸收 Middle-earth、Numenorean 等特定世界观的血统与继承规则。
- 不引入 Graphviz、Tk UI、家族树图片生成或头衔历史生成器。
- 不把人物时间线做成新的 MCP 工具；它通过现有 `ck3_inspect`、`ck3_prepare_edit`、`ck3_search` 和 `ck3_dependencies` 暴露，避免扩大 QQ 工具目录。

## 验证边界

- 静态字段和日期字段分离，日期键不会出现在经验字段列表。
- 生卒、婚姻、特质变化及 `effect` 子项按日期稳定排序。
- 父母、配偶、雇主、收养、王朝、家族、特质和死亡原因引用带关系与日期证据。
- 人物档案进入模型结果，公开模式继续移除当前工程证据。
- 全量扫描后，用真实 `character:<id>` 对字段模式、时间线和引用解析进行复核。
