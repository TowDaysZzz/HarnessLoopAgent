## 1. Skill 领域契约与注册表

- [ ] 1.1 在 `internal/skill` 定义 Skill ID/版本、执行模式、风险等级、依赖、预算、严格参数信封、Invocation 和稳定错误码，并用表驱动测试验证非法标识、未知模式、越权字段和无效预算被拒绝
- [ ] 1.2 实现不可变 Skill Registry 及 Direct、Streaming、Workflow、Durable Workflow 执行端口，并用单元测试验证重复 ID/版本、缺失依赖、固定版本解析和未知 Skill fail closed
- [ ] 1.3 实现 Skill Executor 到现有 `agent.Event` 的有界适配，测试即时完成、失败、取消和 Durable Wait 不占用 Chat active guard 的事件终态

## 2. 统一路由与 Invocation 事实

- [ ] 2.1 扩展 RouteDecision 为唯一 `builtin|skill` Target 和版本化 SkillRef，保持现有 Note、Memory、Reminder 写意图优先，并用路由回归测试验证每个请求最多选择一个目标
- [ ] 2.2 实现 Daily Review 自然语言 Matcher 和严格日期参数解析，语料测试覆盖今天、昨天、明确日期、歧义日期、“回顾并保存”、低置信度及 Prompt Injection
- [ ] 2.3 新增顺序 MySQL 迁移和 Repository 保存 `skill_invocations` 的 owner/session/chat-run/skill/version/参数哈希/状态字段，并用集成测试验证幂等、状态转换、索引和跨 owner 等价 not found
- [ ] 2.4 在 Chat Service 路由完成后、Skill 执行前原子或条件写入 Invocation，并用 Chat 测试验证普通聊天不产生 Invocation、Skill 失败有终态且不存在 HTTP 副作用旁路

## 3. 每日活动快照与固定证据加载

- [ ] 3.1 在 Daily Review 领域定义日期窗口、规范化选项、Chat/Note/Memory EvidenceRef、SourceSnapshot、覆盖警告和规范化 SHA-256，单元测试验证排序稳定、字段边界和等价输入得到相同摘要
- [ ] 3.2 扩展 Chat Repository/MySQL adapter，按 owner 和 UTC 半开窗口有界读取跨会话消息元数据及固定正文，并用集成测试验证稳定排序、会话配额、时间边界和跨 owner 隔离
- [ ] 3.3 在 Chat 消息快照查询中通过 `skill_invocations` 排除 Daily Review 触发与输出，测试同一天连续三次触发时第二、三次不会因 Skill 自身消息改变 Chat 摘要
- [ ] 3.4 扩展 Note Repository/MySQL adapter，按 owner 和 `occurred_at` 窗口返回版本、状态、更新时间和内容哈希并支持固定加载，测试新增、修改、删除、边界时间和不可见状态改变摘要
- [ ] 3.5 实现 metadata-first `Snapshot/LoadPinned` Activity Reader 和有界有限并发，测试快照后记录变化返回 `stale_snapshot`、超限数据产生 coverage warning 且不会无界读取正文

## 4. Memory 变更版本与召回关联

- [ ] 4.1 增加 owner-scoped 单调 Memory Mutation Version 的领域端口、MySQL 事实和兼容迁移，验证已有 owner 可建立初始/惰性基线且跨 owner 版本互不影响
- [ ] 4.2 在 Memory 创建、审核转换、supersede、撤销、拒绝和过期事务中同步推进 Mutation Version，MySQL 集成测试验证事务回滚不推进、成功重放不重复推进且所有可见性变化都会推进
- [ ] 4.3 将 Memory Mutation Version 纳入 Daily Review SourceSnapshot，并复用现有 Recall 获取目标、偏好、约束、摘要和结果，测试新相关 Memory 即使不在旧引用集合中也会使旧缓存失效
- [ ] 4.4 实现固定 MemoryRef 的生成前与缓存提交前校验，测试 superseded、revoked、rejected、expired、版本/hash 不匹配和跨 scope 数据不会进入回顾

## 5. Daily Review 缓存与并发协调

- [ ] 5.1 定义逻辑缓存键、源数据指纹、状态、claim/lease、valid-until、结果信封和稳定错误，并用单元测试验证 owner、窗口、时区、选项、Skill/Schema/Prompt 版本全部参与身份
- [ ] 5.2 新增 Daily Review Cache 顺序迁移和 owner-scoped Repository，包含唯一键、状态/租约/过期索引及有限结果字段，并用迁移和跨 owner 集成测试验证约束
- [ ] 5.3 实现 lookup、创建/claim、等待 ready、过期 lease 重领、token 条件提交和失败终止，集成测试验证同一指纹并发请求只有一个生成者、旧 token 不能覆盖新结果
- [ ] 5.4 实现 TTL、相关 Memory 最早 expiry 和策略有效期共同计算 valid-until，并测试 TTL 未过但源指纹变化时 miss、Memory 到期后不返回旧结果
- [ ] 5.5 实现缓存清理和脱敏观测，测试清理只删除过期终态记录且日志、指标和缓存键不包含 Chat、Note、Memory 正文或凭证

## 6. 结构化回顾生成与证据校验

- [ ] 6.1 定义 `DailyReviewReportV1`、事实条目、建议、反思问题、EvidenceRef 和 CoverageWarning 的严格 schema/decoder，测试未知字段、超限数组、非法引用和 owner 字段均被拒绝
- [ ] 6.2 实现基于现有 ConversationRunner 的 Daily Review 结构化 Adapter 和有界 repair，测试只接受单个 JSON 对象、模型失败/超时消耗正确预算且 Prompt 将 Memory 标记为不可信数据
- [ ] 6.3 实现 Evidence Validator 和确定性 Renderer，测试 Chat/Note 版本与哈希、Memory lineage/hash/active 状态、引用白名单以及无效事实删除或整体失败行为
- [ ] 6.4 实现无 Chat/Note 活动时的确定性空回顾，测试不调用模型、不把 Memory 描述为当天事件且结果可以按未变化指纹缓存

## 7. Daily Review Workflow 与 Harness

- [ ] 7.1 定义有界 `DailyReviewState` 和 `ResolveWindow -> SnapshotSources -> LookupOrClaimCache -> LoadDailyEvidence -> RecallMemoryContext -> GenerateStructuredReview -> ValidateEvidence -> RecheckSnapshotAndCommitCache -> Render` 节点，节点顺序和状态安全测试验证不包含凭证或无界正文
- [ ] 7.2 实现缓存命中 no-op 路径，测试命中时不加载证据正文、不执行 Memory Recall、不调用模型/工具并返回与已提交内容哈希一致的结果
- [ ] 7.3 实现提交前快照复查和最多一次重建，测试生成期间新增消息/笔记或 Memory 变化时不提交旧缓存，连续变化返回 `daily_review_source_changing`
- [ ] 7.4 实现 Workflow NodeEvent 到 Harness Observer 的关联 Adapter，测试 Chat Run、Skill Invocation、Workflow、模型、工具、证据和答案验证共享 correlation，且步骤/模型/工具/时间/输出预算可终止执行
- [ ] 7.5 实现 Workflow 结果到 Skill/agent 事件的映射，测试 `skill.started`、cache hit/miss、允许的 step、`text.delta` 和唯一终态顺序兼容现有 SSE 消费者

## 8. 配置、装配与 Chat 集成

- [ ] 8.1 增加默认关闭的 Skill/Daily Review 开关、时区、日期范围、数据上限、Harness 预算、缓存 TTL/lease/wait 和版本配置，配置测试验证非法组合与缺失依赖 fail fast
- [ ] 8.2 在 composition root 装配 Registry、Daily Review Matcher、Activity Reader、Memory Recall、缓存、Workflow 和 Skill Executor，启动测试验证关闭时不改变现有 Chat/Note/Memory/RAG/readiness
- [ ] 8.3 将 Skill Target 接入现有 Chat Executor 和 Run/SSE 生命周期，Chat 集成测试验证生成与缓存命中都会保存助手回复、失败终态一致且 Skill 消息不污染后续快照
- [ ] 8.4 对 `integrate-routing-executor` 和 Reminder/Memory 路由语料运行互斥回归，验证“回顾”“记住”“保存笔记”“提醒我”和草稿确认不会双重匹配或产生多个业务副作用

## 9. 端到端验证与文档

- [ ] 9.1 增加 MySQL 端到端测试覆盖跨会话消息、每日笔记、Memory 目标关联、有效引用、首次生成、无变化缓存命中和新增数据失效
- [ ] 9.2 增加并发与故障测试覆盖 single-flight、claim 过期、生成后提交前退出、Chat 取消、模型超时、快照持续变化和服务重启后缓存复用
- [ ] 9.3 增加安全测试覆盖跨 tenant/user、伪造证据 ID、Prompt Injection、超限输入、敏感缓存日志和凭证不进入 Workflow 状态
- [ ] 9.4 更新实现状态、Daily Review API/SSE、Skill 注册、缓存新鲜度、灰度和回滚文档，并核对自动调度、每日洞察和自动保存仍明确为非目标
- [ ] 9.5 运行 `go test ./...`、相关 MySQL 集成测试、`go test -race ./internal/skill ./internal/dailyreview ./internal/workflow ./internal/runtime`、`go vet ./...` 和 `openspec validate add-daily-review-skill-workflow --strict`，保存通过结果或修复全部失败
