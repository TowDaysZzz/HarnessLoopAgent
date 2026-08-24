## Purpose

让已认证用户通过自然语言安全创建、查询、修改和取消一次性提醒，并在需要时精确关联现有 Memory；Reminder 的时间、状态与所有权始终由 MySQL 和确定性业务规则控制。

## ADDED Requirements

### Requirement: 系统区分 Reminder 与 Memory 和 Note 意图
系统 SHALL 将自然语言请求分类为 Reminder 创建、查询、修改、取消或 Memory Recall，并 MUST 确保同一请求只进入一个有副作用的业务处理器，不得仅因出现“记住”“请记得”或“提醒我”而同时创建 Note、Memory 和 Reminder。

#### Scenario: 创建一次性提醒
- **WHEN** 已认证用户输入“提醒我明天上午九点提交周报”且表达了未来触发动作
- **THEN** 系统 SHALL 选择 Reminder 创建意图，不得将该请求作为 Note 创建或普通长期 Memory Capture 提交

#### Scenario: 查询已有记忆
- **WHEN** 用户输入“提醒我之前说过喜欢喝什么”且语义是在询问既有事实
- **THEN** 系统 SHALL 选择 Memory Recall 或请求澄清，且不得创建新的 Reminder

#### Scenario: 意图无法可靠区分
- **WHEN** 自然语言无法确定是在创建 Reminder 还是查询 Memory
- **THEN** 系统 SHALL 返回有界澄清问题，且不得产生任何写入副作用

### Requirement: LLM 只生成严格且受限的 Reminder 结构
系统 SHALL 使用版本化、严格解码且有大小边界的结构化契约将自然语言转换为 Reminder 命令；模型只能提出操作、正文、一次性触发时间、时区、稳定 Memory 选择器、置信度与澄清信息，MUST NOT 控制 owner、SQL、Reminder 或 Memory 状态、任意资源 ID、审核结果或投递结果。

#### Scenario: 合法结构化输出
- **WHEN** 模型输出符合 schema、枚举、字段长度、时间格式和置信度边界的 Reminder 命令
- **THEN** 系统 SHALL 使用认证 owner 和确定性时间规则继续处理该命令

#### Scenario: 模型尝试扩大权限
- **WHEN** 模型输出 tenant、user、SQL、状态覆盖、未知字段、任意资源 ID 或绕过审核指令
- **THEN** 系统 MUST 拒绝该输出，且不得执行 Reminder 或 Memory 查询和变更

#### Scenario: 相对时间被解析
- **WHEN** 模型把“明天上午九点”转换为具体时间
- **THEN** 系统 MUST 以请求接收时间和 `Asia/Shanghai` 为锚点验证结果，并在时间已过去、超出允许范围或存在歧义时请求澄清

### Requirement: MySQL 是 Reminder 唯一事实来源
系统 MUST 以独立的结构化 Reminder 记录保存 owner、正文、触发时间、时区、状态、版本、来源和内容哈希；Memory 记录、Workflow checkpoint、Chat Run、Wait 和投递系统均不得决定 Reminder 的当前状态。

#### Scenario: Reminder 成功创建
- **WHEN** 已审核的一次性 Reminder 被提交
- **THEN** 系统 SHALL 原子创建一条 `scheduled` Reminder 和审计事件，并保存未来的 `next_fire_at`

#### Scenario: 相同创建请求被重放
- **WHEN** 同一 owner 以相同幂等键和相同输入重复提交创建请求
- **THEN** 系统 SHALL 返回第一次创建的 Reminder，且不得创建重复记录或重复审计事实

#### Scenario: 幂等键对应不同输入
- **WHEN** 已使用的幂等键被同一 owner 用于不同正文、时间或目标
- **THEN** 系统 MUST 返回稳定冲突并保持原 Reminder 不变

### Requirement: Reminder 变更遵守版本化状态机
系统 SHALL 支持 `scheduled`、`processing`、`fired`、`cancelled` 和 `failed` 状态，并 MUST 使用 expected row version 拒绝陈旧修改；首版一次性 Reminder 进入 `fired`、`cancelled` 或 `failed` 后不得重新变为 `scheduled`。

#### Scenario: 修改尚未触发的 Reminder
- **WHEN** 当前 owner 使用匹配版本批准新的正文或未来触发时间
- **THEN** 系统 SHALL 增加 Reminder 版本、记录审计事件并使 Dispatcher 只采用新版本

#### Scenario: 取消尚未触发的 Reminder
- **WHEN** 当前 owner 使用匹配版本取消 `scheduled` Reminder
- **THEN** 系统 SHALL 将其原子转换为 `cancelled`，且后续 Dispatcher 不得创建新的投递

#### Scenario: 陈旧修改与触发并发
- **WHEN** 修改或取消基于的版本已经因其他请求或 Dispatcher claim 改变
- **THEN** 系统 MUST 返回状态冲突，且不得覆盖当前状态或生成部分事件

### Requirement: Reminder 通过稳定选择器精确关联 Memory
Reminder Workflow MAY 使用现有 Memory Recall Plan 的固定引用、EntityRef、`namespace + slot_key` 或内容哈希加载 owner-scoped active Memory；采用的 Memory MUST 固定其 ID、Lineage Version 和 Content Hash，且不得通过开放式 owner 全量扫描推断关联。

#### Scenario: 精确关联用户偏好
- **WHEN** Reminder 命令包含合法的 `preferences + weekly_report_format` 事实槽且只命中一条 active Memory
- **THEN** 系统 SHALL 在 Reminder 中保存该 Memory 的固定引用，并可在审核内容中展示有界关联摘要

#### Scenario: Memory 选择器无命中或多义
- **WHEN** 选择器没有命中、命中多个无法确定的实体或低于置信度阈值
- **THEN** 系统 SHALL 请求澄清或在用户明确同意后创建不带该关联的 Reminder，不得选择文本相似的其他 Memory

#### Scenario: 固定 Memory 在 Reminder 触发前变化
- **WHEN** 关联 Memory 已 revoked、expired、superseded 或版本与哈希不匹配
- **THEN** 系统 SHALL 将其标记为不可用并按 Reminder 策略省略关联上下文或报告明确原因，不得静默替换为最新 Memory

### Requirement: Reminder 写操作由耐久 Workflow 控制
系统 SHALL 使用与 Chat Run 解耦的 Durable Workflow 执行 Reminder 结构化、时间解析、Memory 精确召回、冲突检查、澄清、审核和提交；所有创建、修改和取消在提交前 MUST 具有明确用户授权，并使用 Wait 版本、内容哈希、Actor 和稳定执行身份。

#### Scenario: 用户批准 Reminder 候选
- **WHEN** 当前 owner 使用匹配 Wait ID、版本和内容哈希批准候选
- **THEN** 系统 SHALL 恢复同一 Workflow Run，以稳定幂等身份提交 Reminder 并正常结束原 Chat Run 之外的 Workflow

#### Scenario: 用户编辑候选
- **WHEN** 用户提交有界编辑内容
- **THEN** 系统 SHALL 重新执行结构化、时间校验、Memory 精确召回和冲突检查，并生成新的候选与 Wait 版本

#### Scenario: 服务在审核期间重启
- **WHEN** Reminder Workflow 处于澄清或审核 Wait 时服务重启
- **THEN** 后续认证请求 SHALL 从 MySQL checkpoint 恢复同一 Run，不依赖原 Chat 连接或进程内状态

### Requirement: 用户能够有界精确管理自己的 Reminder
系统 SHALL 提供 owner-scoped、分页和数量受限的 Reminder 查询，并允许通过可信 Reminder 引用或唯一候选完成修改和取消；领域列表查询不得退化为开放式 Memory 扫描。

#### Scenario: 查询待触发提醒
- **WHEN** 用户查询“我有哪些待办提醒”
- **THEN** 系统 SHALL 只返回当前 owner 的有界 `scheduled` Reminder，并按下一触发时间和稳定 ID 确定性排序

#### Scenario: 按时间窗口查询
- **WHEN** 用户查询“明天有什么提醒”且时间窗口可确定
- **THEN** 系统 SHALL 只返回该 owner 在对应 UTC 时间窗口内的 Reminder

#### Scenario: 修改目标不唯一
- **WHEN** 用户说“把周报提醒改到十点”但精确条件命中多个 Reminder
- **THEN** 系统 SHALL 返回不泄露其他 owner 数据的候选澄清，且不得修改任一 Reminder

### Requirement: Reminder 全链路实施认证所有者隔离
系统 MUST 从可信认证上下文构造 tenant、user、Workflow Owner 和审核 Actor，并在结构化、查询、状态转换、Memory 关联和审计操作中同时应用该范围。

#### Scenario: 提交其他 owner 的 Reminder 或 Wait
- **WHEN** 用户持有其他 owner 的 Reminder ID、Workflow Run ID、Wait ID 或 MemoryRef
- **THEN** 系统 MUST 返回 not found 或等价拒绝，且不得泄露资源是否存在

#### Scenario: 凭证出现在结构化内容
- **WHEN** Reminder 正文、Memory 关联或 Workflow checkpoint 包含 Token、Cookie、密码或服务密钥
- **THEN** 系统 MUST 在持久化前拒绝该数据，且不得将敏感内容写入日志、事件或投递 Outbox

