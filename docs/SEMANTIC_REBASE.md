# CK3 语义 Rebase 迁移 V1

`ck3-index migrate` 用一次可审计的三方事务，把正式版相对旧上游的**定制意图**重新应用到新上游。它不是把新上游整份复制进项目：构建时会先完整复制正式版项目树，再只在 CK3 加载根内重放经过证明的迁移决议。这样工具、文档、备份和其他非加载内容会原样保留；加载根中纯旧上游副本则会被移除，使其自然继承新上游。

## 三方模型与准备

每次迁移都要在 `ck3-index.toml` 中配置三个互不重叠的来源名：

- **Base**：更新前仍可读取的旧上游。
- **Ours**：当前正式版覆盖层。
- **Theirs**：已更新的新上游。

Base 必须保留到迁移副本已经构建完成。工具只在事务中保存文件清单和哈希，不会归档完整上游。`project`、`base`、`target` 的来源名不能相同，输出目录必须尚不存在、且不能与任一配置来源重叠。

输出目录还必须与正式版处于**同一个文件系统改名域**，因为晋升要靠两次 `os.Rename` 完成。仅比较卷名不够：Windows 上它是盘符，但 Linux/macOS 上任何绝对路径的卷名都是空串，两个不同挂载点会被误判为相同，于是晋升会在第一次改名后失败于 `EXDEV`。因此 `plan` 现在接受"与正式版同父目录的兄弟目录"，否则会真正试一次跨父目录改名来验证；`promote` 在第一次改名前会再验证一次。

先创建一次项目策略文件：

```text
ck3-index migrate init migration.toml
```

下面是正式版核心地图继续由项目维护时的最小示例；所有值都是 `ck3-index.toml` 里的来源名，而不是机器路径：

```toml
schema_version = 2
name = "my-ck3-project"
project = "formal"
base = "upstream_before_update"
target = "upstream_after_update"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"

# 当前正式版的地图为权威地图。
map_authority = "project"

# V1 对未知类型永远阻断，不做猜测性文本合并。
unknown_policy = "block"

# 可稳定识别为项目自有的文件名前缀。
owned_prefixes = ["k10_"]

# 可选：真实加载栈中的额外来源；这里列出的每个来源都排在新上游**下方**，
# target 和迁移副本会自动加入。
validation_sources = ["game"]

# 可选，且与 validation_sources 互斥：当某个依赖在实际 playset 中排在新上游
# 上方时，用 validation_stack 显式描述完整有序加载栈，避免验证测量的是一个
# 游戏里并不存在的顺序。position 只能是 above_target 或 below_target。
#
# validation_stack = [
#   { source = "compatibility_patch", position = "above_target" },
#   { source = "game", position = "below_target" },
# ]

# 可选：项目根目录中属于"外部状态"而非 Mod 内容的顶层条目。它们不进入覆盖层
# 清单、不进入迁移副本、也不参与任何事务指纹，而是在晋升和回滚时**随正式版
# 目录移动**，而不是被复制。省略该键即保持旧行为：整棵项目树原样复制。
#
# preserve_paths = [".git", "cache", "logs"]
```

`map_authority` 只能是 `project`、`target` 或 `disabled`（核心地图未变时可留空）。当三方核心地图不同，必须显式选择：

- `project`：`map_data/` 与 `gfx/map/` 下的核心地图均以正式版为权威；只对有精确 RGB 省份映射的受支持引用做重放。目标独有而没有 Base/正式版对应物的核心资产、不能证明的映射和近似几何都会阻断。
- `target`：移除正式版的核心地图阴影文件，采用新上游地图；但若正式版自定义了省份引用，必须先有可证明的精确 ID 重写，工具不会静默保留旧 ID。
- `disabled`：不进行地图迁移；发现不同核心地图即阻断，等待人工调整策略。

## 标准事务流程

为本次运行写一个很小的计划规格，例如：

```json
{
  "profile_path": "migration.toml",
  "output_dir": "migration-copy"
}
```

然后按下面顺序运行。每条命令的 JSON 输出都会给出事务 ID；常规阶段仍须显式选择命令，`status` 只负责查看事务或恢复已检查点化的中断阶段。

```text
ck3-index migrate plan migration-plan.json
ck3-index migrate review <transaction-id>
ck3-index migrate build <transaction-id>
ck3-index migrate validate <transaction-id>
ck3-index migrate approve-smoke <transaction-id>
ck3-index migrate promote <transaction-id>
```

阶段中断或需要确认当前门禁时，使用：

```text
ck3-index migrate status <transaction-id>
ck3-index migrate status <transaction-id> --resume
```

`--resume` 不是“自动跑完迁移”：它会把失败的 `plan` 从已保存的计划输入重新计算为一个**新的事务 ID**，重试已失败的构建/验证阶段，或继续已经写入持久意图的晋升/回滚阶段。尚未开始的普通阶段仍需显式运行 `build`、`validate`、`promote` 或 `rollback`。

`plan` 先对所有文件做三方 SHA-256 分类，只把真正双方修改的对象送入语义适配器。纯旧上游副本会被标为删除，以继承 Theirs；项目自有文件会保留；Jomini、localization、GUI/on_action 命名对象、PNG/TGA 像素、定位器和受支持的地图引用只在有确定证据时合并。未知类型、`replace_path` 影响、同一字段/像素/定位器的竞争修改、模糊对象移动或省份映射都会生成阻塞冲突。

**Jomini 对象自动合并使用领域白名单**，而不是按扩展名全面开放：`events/`、`common/scripted_effects|scripted_triggers|scripted_modifiers/`、`common/culture/cultures|traditions/`、`common/religion/religions|faiths/`、`common/landed_titles/`，以及 `history/characters|provinces|titles/`。`.gui`、`.asset`、`.settings`、`.mod` 等格式经常重复同一个顶层 key，真正身份藏在子字段里，泛化的 key 合并会改错对象或追加重复定义；白名单之外的双方修改文件按普通冲突进入人工审查。

对象身份还区分"不存在"和"有歧义"。同一语义 ID 在旧上游或新上游出现多次时，不再被当作"该对象不存在"从而追加项目版本，而是一律阻断。GUI 有专门身份（`gui:type:*`、`gui:template:*`、`gui:root:<kind>:<name>`）；匿名块、无 `name` 的根控件等无法唯一命名的声明会让整个文件拒绝自动合并，而不是折叠成 `gui:widget` 这类通用 ID。

`review` 仅绑定本机回环地址，输出的 URL 可查看三方分类、对象树、候选文件和冲突。该 URL 带有本次本机服务的写入 capability；不要把它当作普通可公开链接。读取报告无需把源目录暴露给网页，保存决议和导入人工文件则必须携带该 capability。事务资料位于配置的 `artifact_root/rebase-transactions/<transaction-id>/`：

- `transaction.json`：输入指纹、冲突、阶段与验证结果，以及单调递增的 `revision`；
- `decisions.jsonl`：逐文件决议日志，一行一条；
- `progress.json`：当前 phase/completed/current_path 的小型进度投影；
- `transaction.lock`：跨进程事务锁（运行期存在）；
- `report.html`：离线审查报告；
- `resolutions.json`：网页/API 保存的人工决议；
- `candidates/`：工具生成的安全合并候选；
- `manual/`：人工文件决议所引用的审定文件。

决议之所以不再放进 `transaction.json`，是因为规划和构建原本每处理一个文件就要重写并 fsync 整份事务，写入量随文件数呈平方增长；现在完整清单只在阶段边界写一次。

所有会修改事务的操作（`plan`、`review` 的写入接口、`build`、`validate`、`approve-smoke`、`promote`、`rollback`、`status --resume`）都必须先取得 `transaction.lock`，并且每次写入 `transaction.json` 都要求磁盘上的 `revision` 与内存副本一致。原子替换只能防止 JSON 撕裂，挡不住两个进程各自通过状态检查、再在长时间复制的两端写出互相矛盾的终态。持锁进程会持续刷新心跳；只有心跳超过两分钟、主机名相同且进程确实已不存在时，锁才会被自动回收。

若冲突允许 `manual`，网页会把选中的人工候选导入该事务的 `manual/` 子目录，并自动记录相对 `manual_path` 与 SHA-256。通过 API 或导入 JSON 时也必须提供这个受限路径和精确哈希；其余决议只能选择该冲突实际允许且可物化的操作。未解决的冲突不能进入构建。

`build` 先把 Ours 的**完整项目树**复制到临时目录，再只对 CK3 加载根中的计划决议删除阴影、写入候选并原子发布**迁移覆盖副本**。因此项目根目录的工具、文档、备份、缓存和其他非加载文件不会因迁移被静默丢弃。唯一的例外是 `preserve_paths` 显式列出的顶层条目：它们不被复制，而是在晋升后从备份目录移动进新的正式版（回滚时反向移动），保证始终只有一份且永不丢失。该副本配合 Theirs 与配置的验证来源形成真实加载栈；它不是一份独立的新上游拷贝。

`validate` 分别检查三个真实加载栈：旧版 `Ours + Base`、仅新上游 `Theirs`、以及 `迁移副本 + Theirs`；每个栈都会加上配置的验证来源，按 `validation_sources`（全部排在新上游下方）或 `validation_stack`（逐项声明上下位置）确定顺序。目标单独栈用于把新上游已有诊断与迁移引入的诊断分开。解析、引用、资源、项目错误增量、完整加载栈门禁诊断和地图审计都会记录；迁移新增的项目、完整栈或地图问题会阻断。

在隔离 playset 中实际启动游戏并确认后，再运行 `approve-smoke` 记录人工烟雾测试确认。这个确认是 `promote` 的前置门禁，不会替代游戏启动。

## 安全边界与恢复

- `plan` 只写事务目录；`build` 只写新的输出副本。二者绝不修改正式版、Base 或 Theirs。
- 构建、验证、烟雾确认和晋升前都会重新核对 Base、正式版、Theirs、完整正式版项目树以及每个额外验证来源的文件清单哈希；三个主来源还固定了非公开的规范化源根身份，避免同名配置被改指向另一份字节相同的目录。任何漂移都会拒绝继续，必须重新计划。
- 每个阶段保留检查点、报告和失败原因。计划失败可在修复输入后用 `status <id> --resume` 重新生成一个新事务；构建/验证失败可原 ID 重试；取消或失败不会让工具静默改写正式版。
- `plan`、`review`、`build`、`validate` 和 `approve-smoke` 都不会写入正式版。唯一会执行正常迁移晋升的命令是 **`ck3-index migrate promote <transaction-id>`**：它先把原正式版移动为同级可恢复备份，再把已验证的副本原子晋升。不要把 `promote` 当作普通测试命令。
- 晋升和回滚会在第一次目录移动前持久化意图与哈希回执；进程在两次移动之间中断时，`status <id> --resume` 只会在正式版、备份和迁移副本的布局与哈希仍安全匹配时继续或收敛。布局不匹配会拒绝操作，而不是猜测性覆盖。
- 如需恢复，显式运行 `ck3-index migrate rollback <transaction-id>`；这是唯一额外会触碰正式版的恢复操作。只有晋升后的正式版及其备份仍与回执哈希一致时才会回滚，避免覆盖后来的人工作业。
- **晋升和回滚都会在第一次目录改名之前把主索引标记为 stale，并且标记失败就不继续。** 正式版目录被整体替换后，SQLite 缓存记录的仍是旧项目的对象图，而它保存的绝对路径已经指向新文件；如果索引继续报告 `scan_status=ready`，GUI 查询会用旧 `files.sha256` 组 cache key，再从磁盘读到新内容，一个会话里就会混合新旧两代数据。标记为 stale 后，所有依赖索引的 MCP 工具都会返回 `INDEX_STALE`（附带 reason 与 required_action），直到运行一次完整 refresh 重新发布索引为止。晋升回执里的 `index_invalidated` 与 `index_recovery` 记录了这一点。

## 与旧地图迁移命令的关系

旧接口仍保持兼容：

```text
ck3-index map migration-snapshot <spec.json>
ck3-index map migrate <spec.json>
```

它们继续服务既有的地图快照/Fork 工作流。新的 `migrate` 命名空间面向整个 CK3 覆盖层的语义 rebase；在 V1 的黄金用例和项目迁移验证稳定前，旧命令不会被移除。
