# ck3-index Change Log (2026-06-27 Session)

## Overview

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
