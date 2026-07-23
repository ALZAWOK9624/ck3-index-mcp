# CK3 1.19 运行时字段合同与非法字段检测

本文记录 `ck3-index` 对 CK3 1.19 脚本进行的运行时合同检查。目标不是把 PDX 脚本变成一个过度严格的静态类型语言，而是补上“语法能解析、CK3 却不会按预期加载”的高置信度漏洞。

## 判断原则

`ck3-index` 的通用解析器负责识别 PDX 的键、值、块、行号和作用域；它不能仅凭语法判断任意字段是否被游戏对象加载器接受。因此检查按以下优先级组合证据：

1. 当前 CK3 1.19 原版的 `.info` 合同、原版脚本实例和引擎生成的 modifier/engine 数据。
2. 当前索引的对象、引用、作用域和覆盖关系。
3. 项目文件只作为待验证输入，不能反过来证明一个字段合法。

`.info` 是导航和结构提示，不是完整引擎 schema；例如事件 `.info` 明确说明字段表并不穷尽所有运行时字段。所以新增规则只接受原版实例、引擎数据或明确的跨文件关系支持，并尽量把检查限制到对应目录和直接子节点，避免把合法的 Mod 扩展误判为错误。

## 模块、用法与非法形态

| 模块 | 作用 | 合法用法 | 当前检测的非法形态 |
| --- | --- | --- | --- |
| PDX parser / AST | 读取块、标量、数组、行列位置 | 使用 CK3 的 `key = value`、`key = { ... }` 结构 | 语法错误、无法解析的块会进入已有 parser 诊断；解析器不会因为“字段陌生”自动判非法 |
| `events/` | 定义事件、事件选项和效果 | 由调用方、事件 trigger 或 on_action 控制事件是否触发 | 命名事件直接写 `is_triggered_only`，报告 `unsupported_event_field`；CK3 1.19 会在该字段处停止正常加载该事件 |
| `common/casus_belli_types/` | 定义宣战理由和 AI 评分 | `ai_score` / `ai_score_mult` 用 `value = ...` 初始化，再使用 `add`、`multiply`、`subtract` 等 script-value 操作 | 在这些块内使用 `base`，报告 `invalid_script_value_field`；`base` 属于 scripted modifier 形态，不是 script value 操作 |
| `common/governments/` | 定义政府对象和政府规则 | `government_rules` 只放规则枚举成员；政府对象自身的字段放在政府块直接子层；全部生效政府至少有一个正数 `fallback` 优先级；每个 `mechanic_type` 家族恰好一个默认政府 | 把 `court_generate_commanders` 放进 `government_rules`，报告 `invalid_government_rule_context`；没有任何正数 fallback 报 `government_missing_fallback`；默认政府缺失或重复分别报 `government_missing_mechanic_default`、`government_duplicate_mechanic_default` |
| `common/defines/` | 注册引擎使用的全局 ID 集合 | 自定义政府 ID 同时出现在 `NGovernment = { GOVERNMENT_TYPES = { ... } }` | 项目定义了政府对象但没有注册 ID，报告 `unregistered_government_type`；这是跨文件检查，需全量或相关扫描后生成 |
| `common/on_action/` | 定义游戏生命周期回调和事件分发 | 顶层使用 `trigger`、`weight_multiplier`、`events`、`random_events`、`first_valid`、`on_actions`、`random_on_action(s)`、`first_valid_on_action`、`effect`、`fallback`；事件/分发集合可追加 | 未知顶层字段报告 `illegal_field_context`；同一个命名 on_action 重复声明 `trigger` 或 `effect` 报告 `duplicate_on_action_field` |
| modifier definition formats | 给代码定义的 modifier tag 指定显示格式 | 只为当前引擎已存在的 modifier 写 `decimals`、`color`、`prefix`、`suffix`、`negative_suffix`、`percent`、`already_percent`、`hidden`、`no_difference_sign`、`dlc_feature` | 发明新的 modifier 类型报告 `unknown_modifier_definition`；格式块出现其它直接字段报告 `illegal_field_context` |
| modifier containers | 把数值 modifier 应用到 character、province、county、culture、religion、travel plan 或 scheme | 容器的接收对象必须与 modifier 的引擎 `UseAreas` 相交；选择器/元数据放在原版允许的位置，例如 `name`、`parameter`、`terrain`、`object`、`target`、`holding` | 不存在的 tag 报告 `unknown_modifier_field`；tag 存在但接收对象不匹配报告 `invalid_modifier_context` |
| `common/buildings/` modifier 容器 | 建筑向角色、地产或省份提供修正 | 使用原版建筑合同中的 `character_modifier`、`province_modifier`、`county_modifier` 及有文档证据的 culture/faith/situation/government 容器 | `country_modifier` 不是 CK3 1.19 建筑 modifier 容器，报告 `illegal_modifier_container` |

### modifier 容器的关键区别

容器名里的选择条件不等于 modifier 的接收对象。例如 `province_culture_modifier` 仍然把数值应用到 province；`character_culture_modifier` 仍然把数值应用到 character。检测先跳过原版记录的选择器元数据，再用引擎 modifier 的 `UseAreas` 判断接收对象。

因此以下两类错误要区分：

- `garrison_size_mult` 是未知 modifier tag，属于 `unknown_modifier_field`；不是把它移动到别的容器就能修好的上下文问题。
- `scheme_secrecy` 是存在的 modifier，但只适用于 scheme；放进 `character_modifier` 属于 `invalid_modifier_context`。

`monthly_income` 这类 modifier 必须以实际引擎使用区域判断。原版建筑的 `province_modifier = { monthly_income = ... }` 是合法实例；引擎日志中的 `character and province` 也必须被解析为两个区域，不能当成一个名为 `character and province` 的单独区域，否则会制造错误的假阳性。

## MCP / CLI 使用方式

- 写新文件或改完整文件前，用 `ck3_prepare_edit` 查询对象、字段实例和引擎证据。
- 审查候选完整文件用 `ck3_review`；最终接受前用 `ck3_preflight` 的 `operation=patch`。
- 当前工作区的小改动用 `ck3_preflight` 的 `operation=dirty` 或 `ck3-index scan --files`。
- 文件已经改变但诊断仍是旧 generation 时，先执行 `ck3_refresh`；规则版本变化或需要重算跨文件关系时执行完整 `ck3-index scan`。
- 用 `ck3_diagnostics` 的 `operation=summary` 看总量，用 `operation=explain` 按 code、source 或 path 查询证据。
- 修改解析、引擎字段、引用或诊断规则后，必须执行 `go test`、`go build`、`ck3-index scan`、`ck3-index validate` 和 `ck3-index diag_stats`。诊断数量变化不是自动的成功标准；要检查原版基线和具体误报/漏报样例。

新增运行时合同检查会同时进入：

- 扫描阶段的持久化 diagnostics；
- `ck3_review` / `ck3_preflight` 对候选内容的内存分析；
- `ck3_prepare_edit` 对 script-value 字段的编辑提示；
- diagnostic hint 和下一步建议。

## 2026-07-23 全量扫描结果

扫描使用索引规则版本 `2026-07-23-v0.2.35-runtime-contract-lint-11`，项目源的高置信度错误共 338 条；本轮新增模块规则在当前项目中均为 0：

| 诊断 code | 项目数量 | 代表性问题 |
| --- | ---: | --- |
| `unsupported_event_field` | 68 | 命名事件中的 `is_triggered_only` |
| `invalid_script_value_field` | 3 | CB 的 `ai_score_mult` 使用 `base` |
| `unknown_modifier_field` | 194 | `garrison_size_mult` 等不存在的 1.19 modifier tag |
| `invalid_modifier_context` | 60 | `scheme_secrecy` 放入 `character_modifier` 等 |
| `illegal_modifier_container` | 12 | 建筑使用 `country_modifier` |
| `unregistered_government_type` | 1 | `kaitai_government` 未进入 `NGovernment.GOVERNMENT_TYPES` |
| `duplicate_on_action_field` | 0 | 当前项目未发现重复 `trigger` / `effect` |
| `illegal_field_context` | 0 | 当前项目未发现本轮覆盖的未知 on_action/format 直接字段 |
| `unknown_modifier_definition` | 0 | 当前项目未发现；上游 Godherja 依赖中有 1056 条待兼容性处理 |

本轮新增规则的项目计数为：`event_option_selection_conflict` 0、全部 `opinion_modifier_*` 0、`unknown_scripted_relation_modifier` 0、`scripted_relation_flag_limit` 0、`religion_doctrine_order` 0、`name_list_probability_sum` 0、全部 `activity_*` 0、`situation_takeover_conflict` 0。第三轮新增的 traits、innovation asset、event transition、event 2D、event theme、house aspiration、dynasty perk、struggle 和 situation phase 规则在项目与原版 `game/` 中也全部为 0。Godherja 依赖中发现 2 条 `opinion_modifier_invalid_value` 和 2 条 `opinion_modifier_time_conflict`，它们分别来自 `GH_opinion_modifiers.txt` 与 `GH_sarradon_opinion_modifiers.txt`，属于上游依赖兼容性问题。

全量索引统计为 36,263 个文件、219,550 个对象、772,203 个引用、2,522 个 schema 字段；当前 `diag_stats` 的全来源总诊断为 9,672 条（error 3,389、warning 5,975、info 308），包含既有 warning/info，不等同于本轮新增错误数。MCP 的项目视图只汇总当前项目与 global diagnostics，因此当前应显示为 338 个 error、4,793 个 warning、308 个 info。

上游 Godherja 的 `unknown_modifier_definition` 需要单独处理：`common/modifier_definition_formats` 不能凭空创建引擎 modifier 类型。该结果属于依赖兼容性风险，不应误报成当前项目文件的问题。

## 第二轮原版模块契约补齐

本轮从原版 `.info`、原版实例和当前索引的 modifier/engine 数据中补上了之前没有进入 MCP 规则的五组模块，并审计了 story cycle 的字段契约。启用的检查都接入同一个运行时契约入口，因此既会出现在全量 `scan` / `diag_stats` 中，也会在 `ck3_preflight operation=patch` 的虚拟文件分析中被检测。

| 模块 | 作用 | 合法用法 | 非法形态与诊断 |
| --- | --- | --- | --- |
| `events/` option AI 选择 | 决定事件选项的 AI 选择方式 | 一个 option 使用 `ai_chance` 或 `ai_will_select` 其中一种 | 同时声明两者；`ai_will_select` 会静默覆盖另一套选择语义，报告 `event_option_selection_conflict` |
| `common/modifiers/` | 定义可被脚本引用的 modifier 及其图标、叠加和缩放显示 | 顶层 modifier 下使用当前 engine modifier tag；允许 `icon`、`stacking`、`hide_effects`；`scale` 可含 `value`、`desc`、`display_mode` | 拼写错误、移除或不存在的数值 tag 报 `unknown_modifier_field`；`scale` 使用其他字段报 `illegal_field_context`。这里是 modifier 定义，不把 modifier 的 UseAreas 当作文件容器接收者，因此不在此处误报 scheme/province 等合法 modifier |
| `common/opinion_modifiers/` | 定义角色之间的 opinion 修正及衰减/增长方式 | 使用文档字段并可设置 `opinion`；固定时长用 `days`/`months`/`years`，变化率用非负 `monthly_change`；`delay_*` 只与 `decaying = yes` 配合 | `monthly_change` 与固定时长并用报 `opinion_modifier_time_conflict`；同时 decaying/growing 报 `opinion_modifier_mode_conflict`；无 decaying 的 delay 报 `opinion_modifier_invalid_delay`；缺少时长/变化率报 `opinion_modifier_missing_duration`；负字面变化率报 `opinion_modifier_invalid_value`；未知字段报 `illegal_field_context` |
| `common/scripted_relations/` | 定义角色关系名称、关系 modifier 与关系 flags | `modifier` 使用当前静态角色 modifier；关系最多 32 个 flags | 使用不存在或由 scheme/lifestyle 生成的 modifier 报 `unknown_scripted_relation_modifier`；超过 32 个 flags 报 `scripted_relation_flag_limit` |
| `common/story_cycles/` | 定义故事循环的阶段效果组与条件效果 | `.info` 描述了 `random_valid`/`first_valid` 的选择器，但当前 1.19 原版实例也大量使用同一 `effect_group` 的多个直接 `triggered_effect`；`triggered_effect` 可以继承效果组的 `trigger` | 原版实例与 `.info` 的“只能一个独立 triggered_effect”描述相冲突；因此不启用重复数量规则，只检查 effect_group 的时长、chance，以及 triggered_effect 必须有 `effect` |
| `common/religion/religion_types/` | 定义宗教级默认 doctrine 与其 faith 集合 | 宗教级 `doctrine` 全部位于 `faiths` 之前；faith 内的 doctrine 仍嵌在对应 faith 块中 | `faiths` 之后再出现宗教级 `doctrine` 报 `religion_doctrine_order`，避免被宗教加载器按错误阶段解释 |
| `common/culture/name_lists/` | 定义文化姓名、宗族名和按亲属命名的概率 | 父系/母系祖父或祖母及父母命名概率分别求和，每组不超过 100；脚本化或 define 值交给运行时 | 字面概率和超过 100 报 `name_list_probability_sum` |
| `common/activities/activity_types/` | 定义活动选项类别、选项和活动阶段 | 同一活动中类别名唯一；同一类别内选项名唯一；活动至少有一个阶段且阶段名唯一 | 重复类别/选项/阶段分别报 `activity_duplicate_category`、`activity_duplicate_option`、`activity_duplicate_phase`；空阶段报 `activity_missing_phase` |
| `common/situation/situations/` | 定义 situation 的阶段和 future phase 转移 | 一个 future phase 转移使用 `takeover_points` 或 `takeover_duration` 其中一种 | 空阶段报 `situation_missing_phase`；同一 future phase 同时出现两者报 `situation_takeover_conflict` |

## 第三轮原版模块契约补齐

本轮继续从原版 `.info` 和可运行的原版实例中补充“字段能被解析、但组合方式会被 CK3 拒绝或失效”的约束。数值规则只对字面数字下结论；脚本值、变量和触发器计算出的动态值不会被静态规则伪装成已知结果。

| 模块 | 作用 | 合法用法 | 非法形态与诊断 |
| --- | --- | --- | --- |
| `common/traits/` | 定义 trait、遗传方式、性别限制和 trait tracks | `genetic = yes` 与手写 `inherit_chance` 体系二选一；`triggered_opinion` 只使用一个性别限制；track 名唯一，字面 XP 阈值在 0 到 100 且递增 | 遗传方式冲突报 `trait_genetic_inheritance_conflict`；同时 male/female 限制报 `trait_opinion_gender_conflict`；重复 track 名、越界或倒序阈值分别报 `trait_track_duplicate_name`、`trait_track_xp_range`、`trait_track_xp_order` |
| `common/culture/innovations/` | 定义文化创新及其图标/名称资源 | 每个 `asset` 至少提供 `name` 或 `icon` 之一 | 两者都缺失报 `innovation_asset_display_missing` |
| `common/event_transitions/` | 定义事件场景或流程的 transition | `transition.duration` 使用大于 0 的值 | 零或负字面 duration 报 `event_transition_invalid_duration` |
| `common/event_2d_effects/` | 定义事件 2D 效果资源和播放时长 | `effect_2d.duration` 可以省略或为 0，但不能为负 | 负字面 duration 报 `event_2d_invalid_duration` |
| `common/event_themes/` | 为事件主题提供背景、图标和声音 | 主题根块直接提供 `background`、`icon`、`sound` | 缺少任一必需字段报 `event_theme_missing_required_field` |
| `common/house_aspirations/` | 定义家族抱负及其等级 | 抱负根块至少包含一个 level | 没有等级报 `house_aspiration_missing_level` |
| `common/dynasty_perks/` | 定义王朝 perk 树及 trait 选择 | `traits` 块至少有一个非零字面 AI chance | 空块或所有字面 chance 都为零报 `dynasty_perk_trait_chance`；动态 chance 留给运行时验证 |
| `common/struggle/struggles/` | 定义 struggle 的阶段、初始阶段和结束路径 | 至少有 `phase_list`、`start_phase`，且至少一个 phase 有 `ending_decisions` | 缺少阶段列表、初始阶段或结束决策分别报 `struggle_missing_phase_list`、`struggle_missing_start_phase`、`struggle_missing_ending_decision` |
| `common/situation/situations/` | 定义 situation 当前运行阶段和 future phase 转移 | 至少有一个 `phases`，future phase 的 takeover 方式二选一 | 空阶段报 `situation_missing_phase`；同时使用 `takeover_points` 与 `takeover_duration` 报 `situation_takeover_conflict` |
| `common/laws/` | 定义 title、succession 和其他法律对象 | `title_division` 与 inheritance/noble_family 配合；partition 与 children 配合；`election_type`、`appointment_type`、`pool_character_config` 分别与允许的 succession order 配合 | 继承法字段的组合违反 `_laws.info` 条件，或 appointment 同时定义 traversal/division/rank，报告 `law_succession_field_context` |
| `common/council_tasks/` | 定义内阁职位任务、进度和任务克隆 | clone 任务只重新定义 `position`；county 目标使用 `task_type_county`；current/max 使用 `task_progress_value`；default task 使用 general/infinite | clone 任务缺 position 或重定义其它字段报 `council_task_clone_context`；任务字段与 task type/progress 不匹配报 `council_task_field_context` |
| `common/house_relation_types/` | 定义 dynasty house 之间的关系等级和有效期 | `levels` 至少包含一个命名等级 | 空或缺少 `levels` 报 `house_relation_missing_level` |
| `common/flavorization/` | 定义角色、头衔和 domicile 的称谓风格化 | `type = domicile` 时提供 `domicile_type` 数据库键 | 缺少 domicile 数据库键报 `flavorization_missing_domicile_type`；其余“only applies”字段暂不做全局拒绝，因为原版 title-holder 实例存在更宽用法 |
| `common/lease_contracts/` | 定义领主、承租人和 lease liege 的税收/征召兵分成 | `lease_liege` 与 `rest.max` 使用 0 到 100 的份额；`lease_liege` 只用于 hierarchy；beneficiary/rest 使用 ruler 或 lessee | 份额越界报 `lease_contract_value_range`；无 hierarchy 使用 lease_liege 报 `lease_contract_hierarchy_context`；枚举值错误报 `lease_contract_enum` |
| `common/subject_contracts/contracts/` | 定义封臣/属邦合同的义务等级、显示方式和贡献率 | 合同 `display_mode` 使用四种 UI 模式；义务等级的税、兵、群体资源和最低值使用 0 到 1，复杂 script math 留给运行时 | 字面贡献率越界报 `subject_contract_contribution_range`；display_mode 不在枚举中报 `subject_contract_enum` |

traits 的 track 检查会跳过原版使用的命名等级块，例如 `trait_second_level`；只有可以确定为数值 XP 阈值的直接键才参与范围和顺序检查。event 2D 的零时长是原版允许的默认/立即播放形态，因此不能误报为非法。story cycle 的 `triggered_effect` 重复规则仍然不启用，因为当前原版实例与 `.info` 的单数描述互相矛盾。

这些检查不是把 `.info` 的字段列表机械扩展成全局拒绝表：只在能够由原版文档、原版实例或引擎数据确认的模块边界内报告错误。动态 scripted modifier、scripted effect、script value 等仍使用各自的语义检查，不能因为名字相似就当作普通 modifier tag。

## 第四轮原版模块契约补齐

本轮继续遍历原版 1.19 的 `.info` 与实际脚本实例，补上此前 MCP 没有检查的“数量必须对应、枚举/范围必须合法、跨字段/跨文件必须一致”规则。它们都通过同一个 `checkRuntimeContracts` 入口进入虚拟文件分析，因此 `ck3_preflight operation=patch` 也能在写盘前发现这些非法形态；宫廷类型的唯一默认值属于跨文件关系，则由扫描阶段的 validator finalizer 检查。

| 模块 | 干什么 | 怎么用 | 什么是非法，如何检测 |
| --- | --- | --- | --- |
| `common/accolade_names/` | 用获勋骑士、持有者和勋章类型拼动态名称 | `num_options = N`，随后按替换顺序写恰好 N 个 `option = { ... }` | 数量缺失、负数、非整数或与 option 数不相等，报 `accolade_name_option_count`；原版 `_accolade_name.info` 明确说数量不匹配会触发系统错误 |
| `common/culture/eras/` | 定义文化时代开始年份及时代修正、解锁提示 | 每个时代根块写 `year = 0` 或更晚的年份；其它 modifier/unlock 字段按原版直接子块使用 | 缺少 year 或字面 year 为负，报 `culture_era_year`；动态年份只记录结构，不假装能静态求值 |
| `common/ai_war_stances/` | 按战争双方强弱选择 AI 战争姿态，并按目标优先级移动军队 | `side = attacker/defender`；`behaviour_attributes` 至少启用 stronger、weaker、desperate 之一；普通目标写整数优先级；只有 `enemy_unit_province` 能写带 `priority`/`area` 的对象块 | side 枚举错误、行为字段错误/全为 no、未知目标、对象目标放错上下文、优先级不在 0..1000、area 枚举错误或重复，分别报 `ai_war_stance_*`；area 跨所有 objectives 块去重 |
| `common/house_unities/` | 定义家族团结值、阶段、参数、modifier 和阶段决策 | 根块下每个阶段写正整数 `points`，再按需要写 `parameters`、`modifiers`、`on_start`、`on_end` | points 缺失、非正或非整数报 `house_unity_stage_points`；原版合同说明非正 points 的阶段会被忽略。`.info` 里“government_key 必须存在”的注释与原版实际文件矛盾，因此没有启用该条 |
| `common/story_cycles/` | 定期运行故事循环的 effect group、阶段和条件效果 | 每个 `effect_group` 选择一个 `days/weeks/months/years`；`chance` 使用 0..100；`triggered_effect` 至少有 `effect`，`trigger` 可以省略并继承 effect_group | 缺时长报 `story_cycle_duration_missing`；混用时长单位报 `story_cycle_duration_conflict`；chance 字面越界报 `story_cycle_chance_range`；缺 effect 报 `story_cycle_triggered_effect_shape`。不把原版实际使用的多个直接 triggered_effect 判错 |
| `common/activities/activity_types/` | 定义活动的阶段、选项类别、参与者意图和 AI 计划频率 | 使用 `ai_check_interval_by_tier` 时补齐 barony/county/duchy/kingdom/empire/hegemony；`host_intents`/`guest_intents` 的 `default` 与 `player_defaults` 必须来自对应 `intents` 裸列表 | 缺分层字段报 `activity_ai_tier_missing`；默认意图不在 intents 报 `activity_intent_default_invalid`；裸列表节点按 key 读取，避免把合法原版意图误判为空值 |
| `common/decisions/` | 定义角色决议、显示/有效条件、成本、效果和 AI 目标 | 普通决议写 `ai_check_interval` 或完整 `ai_check_interval_by_tier`；只有 `ai_goal = yes` 的决议可以省略 AI 检查间隔 | 两者都没有且不是 ai_goal 报 `decision_ai_interval_missing`；分层块缺任一 rank 报 `decision_ai_tier_missing` |
| `common/character_interactions/` | 定义角色发起者、接受者、可用条件和 AI 互动频率 | 使用 `ai_frequency_by_tier` 时写六个 title tier；代码控制的互动可按原版使用直接 `ai_frequency` | 分层块缺字段报 `interaction_ai_tier_missing`；recipient 的最终作用域可能由 redirect/code 决定，因此不做静态强制 |
| `common/great_projects/types/` | 定义大型工程、贡献条目和 AI 规划/投资 | 根工程或 contribution 使用 `ai_check_interval_by_tier` 时写六个 title tier；也可按原版使用统一 `ai_check_interval` | 任一分层块缺字段报 `great_project_ai_tier_missing` |
| `common/struggle/struggles/` | 定义 struggle 的阶段流转、阶段持续时间、效果和结束阶段 | 非终局 phase 至少有一个 `future_phases`；`start_phase` 和 future phase 名必须在同一 `phase_list`；point duration 用至少 1 的整数，时间 duration 用正值 | 缺 future phase 报 `struggle_missing_future_phase`；持续时间非正报 `struggle_invalid_duration`；阶段引用不存在报 `struggle_phase_reference`；终局 phase 写 `ending_decisions`、future phases 或 modifier 报 `struggle_ending_phase_fields` |
| `common/court_types/` | 定义宫廷类型及宫廷默认类型 | 多个宫廷类型可以并存，但全局至多一个 `default = yes`；当前原版只有外交宫廷作为默认 | 活跃来源层合并后出现多个字面 `default = yes`，扫描 finalizer 为每个冲突位置报 `court_type_duplicate_default`；动态 default 不静态判定 |

本轮还明确排除了几类不能安全静态化的候选：`men_at_arms.can_recruit` 与 innovation 解锁在原版存在合法的条件组合，不能按“同时出现”误判；`genes/_genes.info` 自称不完整；domicile 的 `map_pin_anchor` 注释与原版 estate 实例存在方向差异；宗教 doctrine 的“多选 doctrine 不能放 religion 层”需要跨 doctrine 类型解析；character interaction 的 recipient 可能由代码 redirect 完成。这些会留在审计清单，不伪装成已完成的非法字段检查。

## 有意保留的边界

1. 不根据 `.info` 的有限字段表对所有陌生 key 做全局拒绝；否则会把合法的脚本扩展和未覆盖的 CK3 合同变成假阳性。
2. modifier 检查针对数值引擎 tag；自定义 scripted modifier、scripted effect 和 script value 有各自的合同，不能互相套用。
3. 自定义政府的注册关系必须跨 `common/governments/` 与 `common/defines/` 检查，所以单文件 MCP patch 可以做局部检查，但只有索引刷新后才能给出完整注册结论。
4. 规则依赖活动来源、覆盖关系和 source rank；原版、Godherja 依赖、当前项目的诊断应分开阅读。

## 主要证据位置

- 原版事件合同：`game/events/_events.info`
- 原版政府合同：`game/common/governments/_governments.info`
- 原版法律合同：`game/common/laws/_laws.info`
- 原版 CB 与 script value 合同：`game/common/casus_belli_types/_casus_belli.info`、`game/common/script_values/_script_values.info`
- 原版 on_action 合同：`game/common/on_action/_on_actions.info`
- 原版建筑与 modifier 容器：`game/common/buildings/_buildings.info`
- modifier 格式合同：`game/common/modifier_definition_formats/_definitions.info`
- 原版角色 modifier 合同：`game/common/modifiers/_modifiers.info`
- 原版 opinion modifier 合同：`game/common/opinion_modifiers/_opinions.info`
- 原版 scripted relation 合同：`game/common/scripted_relations/_scripted_relations.info`
- 原版 story cycle 合同：`game/common/story_cycles/_story_cycles.info`
- 原版宗教类型合同：`game/common/religion/religion_types/_religion_types.info`
- 原版姓名列表合同：`game/common/culture/name_lists/_name_lists.info`
- 原版活动类型合同：`game/common/activities/activity_types/_activity_type.info`
- 原版奖章命名合同：`game/common/accolade_names/_accolade_name.info`
- 原版文化时代合同：`game/common/culture/eras/_culture_eras.info`
- 原版战争姿态合同：`game/common/ai_war_stances/_ai_war_stances.info`
- 原版家族团结合同：`game/common/house_unities/_house_unities.info`
- 原版 situation 合同：`game/common/situation/situations/_situations.info`
- 原版决议合同：`game/common/decisions/_decisions.info`
- 原版角色互动合同：`game/common/character_interactions/_character_interactions.info`
- 原版大工程合同：`game/common/great_projects/types/_great_project_types.info`
- 原版宫廷类型合同：`game/common/court_types/_court_types.info`
- 原版 trait 合同：`game/common/traits/_traits.info`
- 原版文化创新合同：`game/common/culture/innovations/_culture_innovations.info`
- 原版 event transition 合同：`game/common/event_transitions/_event_transitions.info`
- 原版 event 2D 合同：`game/common/event_2d_effects/_event_2d_effects.info`
- 原版 event theme 合同：`game/common/event_themes/_event_themes.info`
- 原版 house aspiration 合同：`game/common/house_aspirations/_house_aspiration.info`
- 原版 dynasty perk 合同：`game/common/dynasty_perks/_dynasty_perks.info`
- 原版 struggle 合同：`game/common/struggle/struggles/_struggles.info`
- 原版 council task 合同：`game/common/council_tasks/_council_tasks.info`
- 原版 house relation 合同：`game/common/house_relation_types/_house_relation.info`
- 原版 flavourization 合同：`game/common/flavorization/_flavourization.info`
- 原版 lease contract 合同：`game/common/lease_contracts/_lease_contracts.info`
- 原版 subject contract 合同：`game/common/subject_contracts/contracts/_subject_contracts.info`
- 政府注册实例：`game/common/defines/00_defines.txt`
- 引擎 modifier 使用区域：`logs/modifiers.log` 与生成的 modifier 数据
- 实现：`internal/indexer/runtime_contracts.go`、`internal/indexer/runtime_contracts_extended.go`、`internal/indexer/runtime_contract_validation.go`

## Error-log contract lint (2026-07-23)

Index rule version: `2026-07-23-v0.3.1-publication-perf-contracts`.

This pass turns the deterministic parts of the current `error.log` into static checks. The checks are deliberately split by module so that a parser-accepted key is not treated as legal merely because it parses:

| Module | What it does | Valid use | Illegal form detected |
| --- | --- | --- | --- |
| Package metadata | Validates a package descriptor's `supported_version` grammar | Use a numeric major/minor, for example `1.19.*` or `1.19.0.6` | Wildcards in major/minor positions such as `1.*` or `1.*.*`; packager code `package_supported_version_invalid` |
| `common/decisions/` | Checks the decision's display picture contract | At least one direct `picture = { reference = "gfx/..." }` block; variants may be trigger-gated | No picture block, a picture block without `reference`, or a bare string path; `decision_picture_missing` |
| Localization | Recovers quoted values and checks high-confidence syntax | UTF-8 entry values, closed outer quote, balanced `[...]`, and balanced `Concept`/`SelectLocalization`/`Select_CString`-style calls; escaped quotes and trailing comments are allowed | Replacement/control character inside the key or quoted entry value, missing outer quote, unbalanced square bracket, or unterminated known macro; `localization_invalid_character`, `localization_entry_syntax`, `localization_macro_syntax` |
| `history/characters/` | Checks only the direct `name` field of an indexed character object | Quote a literal name or provide an active localization key for an unquoted name | An unquoted direct name without an active localization value; `history_character_name_localization_missing` |
| Project variables | Checks literal variable setter/read relationships across active source layers | A script reads a variable written by the project, or localization evaluates a literal `.Var('name')` / `GetGlobalVariable('name')` expression | A project variable is set but has no indexed script or literal localization runtime read; ordinary localization text is not a read; `variable_write_only` |

The history check uses the indexed character object's direct `name` field, not a line-wide search. Nested fields such as `set_variable = { name = ... }` therefore do not become false character-name warnings. The variable check is project-only because upstream history commonly writes state for later runtime handoff that is outside this Mod's static source boundary.

The following remain intentionally outside these rules: runtime scope/state failures, invalid landed-title capital state, duplicate law materialization, dynamic custom localizers such as `GetWhiteBookPage`, and missing localization keys whose names are constructed dynamically. Those need a runtime trace or a more complete engine execution model and are not converted into high-confidence static blockers.

## Final v0.3.1 verification (2026-07-24)

The final staged full scan indexed 36,263 files, 219,550 objects, 865,625 references, 2,712,541 localization rows, and 12,269 resources. The scan-time diagnostic count was 9,801; after the explicit read-only validation pass materialized its additional checks, the stable count was 11,086.

Project-layer diagnostics remained 338 errors, 4,895 warnings, and 308 informational findings. Representative error-log-derived project findings are 68 `unsupported_event_field`, 3 `invalid_script_value_field`, 12 `illegal_modifier_container`, 1 `unregistered_government_type`, and 102 `variable_write_only` findings. The dependency layers contain 15 `localization_entry_syntax`, 11 `localization_macro_syntax`, 2 `opinion_modifier_invalid_value`, and 2 `opinion_modifier_time_conflict` findings.

Version v0.3.1 narrows `localization_invalid_character` to the localization key and quoted value. It deliberately ignores trailing comments. This removed exactly nine false positives caused by a control byte in vanilla Chinese-name annotations while the regression fixture still rejects the same byte inside a localization value.

## v0.5.0 false-positive calibration (2026-07-24)

Index rule version: `2026-07-24-v0.5.0-diagnostic-provenance-2`.

The calibration pass compared the full project/dependency/game index with the installed CK3 `game`, `clausewitz`, and `jomini` resource trees. Resource-only sources index files under `gfx/`, `map_data/`, and `sound/` without importing a second copy of script definitions. Reference resolution now understands source-root paths, layer-relative suffixes, bare filenames, extensionless graphic prefixes, and repeated path separators.

The stable full-scan total fell from 11,086 to 873 without suppressing the confirmed runtime-contract errors:

| Diagnostic | Before | After | False-positive cause removed |
| --- | ---: | ---: | --- |
| `missing_resource` | 4,667 | 1 | Incomplete copied game tree, missing engine resource layers, relative/bare path forms, and duplicate slash normalization |
| `resource_resolution_uncertain` | 308 | 0 | Contextual basename, suffix, and graphic-prefix resolution |
| `unknown_modifier_field` | 1,653 | 148 | Concrete expansions of engine-published `$PLACEHOLDER$` modifier formats and module-specific metadata fields |
| `unknown_modifier_definition` | 1,056 | 0 | Generated modifier-definition format expansions |
| `invalid_modifier_context` | 592 | 68 | Trait/court-position `culture_modifier` receiver semantics and council-task `county_modifier.scale` metadata |
| `missing_event_loc` | 1,282 | 0 | Hidden events, option localization, implicit decision keys, and dotted helper blocks incorrectly indexed as events |
| `on_action_direct_override` | 683 | 454 | Vanilla source files warning about their own definitions and live-log names used as unsupported override evidence |
| `variable_write_only` | 102 | 15 | Reads in dependency/game layers, global-variable relations, and literal localization runtime reads |
| `missing_localization` | 86 | 66 | Engine/game-owned keys and the literal `...` GUI placeholder |
| `missing_object_reference` | 16 | 3 | CK3's `title = 0` null sentinel |
| `gui_layout_misuse` | 43 | 0 | `parentanchor` in flow containers was an unsupported style heuristic, not an engine illegality |
| `nested_iterator` | 477 | 0 in this workspace | Upstream nesting was treated as an error-like finding; the rule is now project-only and explicitly advisory |

`scripted_effect_recursion` remains one warning rather than an error because a static self-call can terminate through scope or state changes. The one remaining `missing_resource` is `gfx/interface/icons/culture_innovations/_default.dds`, which is absent from every configured resource layer. Full-scan reference resolution is indexed by source/override/path provenance; the final 69,487-file scan resolved references in about 4 seconds and wrote validation diagnostics in about 5 seconds.
