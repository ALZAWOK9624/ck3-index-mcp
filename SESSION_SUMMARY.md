# ck3-index 对话全程改动总结

## 起点

用户在开发一个 Godherja 东方子模组，需要一个 CK3 编码助手工具。ck3-index 已有基础功能——索引 mod 文件、查询对象/引用/本地化、校验缺失引用。但存有大量问题：校验不准确（6,649 条 `missing_object_reference` 误报）、parser 不认识 CK3 合法语法、性能差（clean scan 84s）、ck3-tiger 的 scope/shape 数据未利用、很多 key 类型（迭代器、scope 转换、@define）完全不被追踪。

## 改动总览（按时间线）

### 阶段 1：架构定型与性能优化

| 改动 | 效果 |
|---|---|
| 并发 worker 池解析文件 | 84s → 44s |
| prepared statements 复用 | 避免每行重新 compile SQL |
| 延迟建索引（clean scan 末尾 CREATE INDEX） | 写库速度翻倍 |
| 砍 nodes 表全量写入 | 省 1270万行 INSERT |
| 砍 object_defs 重复表 | 省 27万行 |
| PRAGMA synchronous=OFF / cache_size=-200000 | DB 写加速 |
| SHA256 + mtime 增量扫描 | 未变时 12s |
| rel_path 覆盖检测——被覆盖文件不解析 | objects -25%、refs -19%（检测到 1310 个被覆盖文件） |

→ **Clean scan：84s → 44s → 34s**

### 阶段 2：Bug 修复（AI Review + Code Review）

| 来源 | 修复数 | 代表 |
|---|---|---|
| 另一 AI 的 REVIEW_REPORT | 5 个 | 双计数 diagnostics、增量 stale loc/resource、RefHit 缺 Source 字段、rows.Err 未检查、refEvidence 未传播 Source |
| 本 session Code Review | 11 个 | overridden 文件 nil deref（Critical）、LastInsertId 错误忽略、不可达代码、scope 常量值为 0（High）、LEFT JOIN 死逻辑、hasUTF8BOM 吞噬错误、`?` token 误解析、`\r\n` 列号偏移等 |

### 阶段 3：Parser 增强（CK3 合法语法不再报错）

| 问题 | 修复 |
|---|---|
| GUI `type = A = B` 语法（`GH_types_main_types.gui`）| `parser.go` 处理连续赋值 |
| `OPERATOR = <=` 参数化触发器（`vizierate_events.txt`）| `parser.go` 把 `<=` 当 value |
| UTF-8 BOM 污染 key | `lexer.go` 跳过 `\uFEFF` |

→ **parse_error 从 23 → 0（全源归零）**

### 阶段 4：Object 提取增强（嵌套结构不再漏）

| 问题 | 修复 | 效果 |
|---|---|---|
| 宗教文件里 `faiths = { faith = {...} }` 嵌套提取不到 | `extractObjects` 递归 faiths 块 | `wasteland_faith` 等正确索引 |
| title 文件里 `k_kingdom → d_duchy → c_county → b_barony` 层层嵌套 | `extractObjects` 递归 title 块 | 128K 嵌套 title 被召回 |
| GUI 文件里 `container`、`icon` 等通用原语产生大量 duplicate | `extractObjects` GUI 黑名单（25 个） | duplicate 减少 |
| `objectTypeForPath` 把 religion 目录返回 "faith" 类型 | 改为 "religion"，faith 嵌套提取 | 宗教/信仰类型正确分离 |

→ **`missing_object_reference` 从 6,649 → 0（-100%）**

### 阶段 5：Reference 提取增强（新 ref 种类）

| 新增 kind | 追踪量 | 触发条件 |
|---|---|---|
| `iterator` | 45,056 | block key 在 `iteratorScopeIn` 中 |
| `scope_transition` | 39,930 | block key 在 `scopeTransitionsIn` 中 |
| `define` | 15,938 | value 以 `@` 开头 |

**修复的 ref 误匹配：**
- `keyRefTypes` 跳过 scope 表达式和 loc key（`.t`）
- `prefixTypes` 跳过 scope 链和双前缀

### 阶段 6：Lint 检查（新诊断码）

| 诊断码 | 检查 | 来源 |
|---|---|---|
| `missing_trigger_else` (1,576) | 2+ `trigger_if` 链缺收尾 | CK3 Wiki M19 |
| `event_no_option` (1,942) | 事件缺 option | CK3 Wiki M17 |
| `on_action_direct_override` (13) | 原版 on_action 直接写 effect | CK3 Wiki M9 |
| `nested_iterator` (479) | 嵌套迭代器 | CK3 Wiki M6 |
| `gui_layout_misuse` (25) | hbox/vbox 里 parentanchor | CK3 Wiki M22 |
| `gui_crash_risk` (0) | GUI 崩模式 | CK3 Wiki M21 |
| `scope_mismatch` (5) | 迭代器感知 scope 校验 | CK3 Wiki M1 |
| `scope_never_saved` (0) | scope 未 save | CK3 Wiki M3 |

→ **`scope_never_saved` 从 11,026 → 0（白名单 90+ 内置 scope + 单字母过滤 + scope 链跳过）**

### 阶段 7：Ck3-tiger 数据完整提取

从 ck3-tiger 的 9 张 Rust 数据表全部提取为 Go map：

| 表 | 条目 | MCP 工具 |
|---|---|---|
| triggers/effects scope | 2,150 | `lookup_scope` |
| triggers/effects shape | 2,150 | `lookup_shape` |
| iterators | 1,324 | scope tracker |
| targets (scope 转换) | 222 | scope transition tracking |
| defines | 1,903 | `lookup_define` |
| on_actions | 200 | `lookup_on_action` |
| modifier kinds | 634 | `lookup_modifier` |
| sounds | 923 | `IsSound` |
| loc macros | 426 | `IsLocMacro` |

从 game logs 额外提取：

| 源 | 条目 | MCP 工具 |
|---|---|---|
| `effects.log` | 1,886 描述+示例 | `lookup_example` |
| `triggers.log` | 1,768 描述+示例 | `lookup_example` |
| `modifiers.log` | 2,210 修饰符 | `lookup_modifier` |

→ MCP 工具从 12 → 18 个

### 阶段 8：数据验证

全量游戏安装（18,664 文件 / 520MB）+ Godherja（7,399）+ 游戏源码（14,704）三重 grep 验证：

| 数据表 | 确认率 | 不可信条目 |
|---|---|---|
| trigger/effect | 85% | 317（DLC 引擎级 / 值触发器 / 真过期） |
| modifier kinds | 97% | 14 |
| defines | 99% | 18 |
| on_actions | 99% | 2 |
| loc_macros | 98% | 6 |

不可信条目存 `deprecated_data.gen.go`，LLM 可参考但不拦截。

### 阶段 9：Server 部署

打包 pure source 上传到 `root@154.12.39.153:/root/运行环境/ck3-index/`，SKILL.md 同步到 `/root/.codex/skills/ck3-coding/`。

### 阶段 10：工具产出

- `CHANGELOG.md` — 完整变更日志
- `SESSION_SUMMARY.md` — 本文件
- SKILL.md 重写（43 → 100+ 行）
- README.md 重写（23 → 70+ 行）
- `tools/char_rank.py` — CK3 原版角色综合排名工具

---

## 最终状态

| 指标 | 之前 | 之后 |
|---|---|---|
| Clean scan | 84.6s | **34s** |
| Incremental scan | — | **12s** |
| `missing_object_reference` | 6,649 | **0** |
| `parse_error` | 23 (3 project) | **0** |
| `missing_localization` | 10,761 | **91** (项目) |
| 总诊断 | 27,810 | **21,190** |
| 项目 actionable | — | **116 条** |
| MCP 工具 | 12 | **18** |
| Tiger 数据表 | 0 | **9** |
| Log 数据源 | 0 | **3** |
| 新 Go 源文件 | — | **5 个** (lint/health/scope_check/scope_tracker/CHANGELOG) |
| 新增 gen.go 文件 | — | **7 个** |
| 新增提取工具 | — | **7 个** |
