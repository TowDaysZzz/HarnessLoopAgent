## Context

See `proposal.md` for motivation and the two delta specs for observable behavior. 当前 Chat Service 已形成 `Router -> Executor Facade -> Handler/Eino Runner -> SSE` 主链路，但每个业务意图仍由固定枚举和 switch 分派；`internal/workflow` 提供强类型顺序 Runner、NodeEvent、Wait 和 DurableRuntime，`internal/runtime` 另行提供模型/工具预算与 Harness Observer，两套观测尚未桥接。

现有 Chat Repository 只能按单个 Session 读取消息，Note 与 Memory 各有 owner-scoped 事实和版本，但缺少跨会话的日期窗口读取、统一活动快照以及回顾缓存。Memory 是经过审核的用户事实，不是每日行为流水；Daily Review 必须以当天 Chat/Note 为活动证据，只把有效 Memory 用作目标、偏好和约束上下文。

同时存在 `integrate-routing-executor` 与 `add-memory-aware-reminder-workflow` 活动 change。前者已建立统一执行入口，后者计划移除 HTTP Memory Capture 旁路并继续扩展业务意图。本设计在 Router 与 Executor 之间增加通用 Skill Target，不为每个后续 Skill 继续增加平行 Facade 字段，并保持所有写意图的既有 fail-closed 约束。

## Goals / Non-Goals

**Goals:**

- 建立与具体 Skill、模型框架和持久化实现解耦的版本化 Skill Registry 与执行适配层。
- 让 `daily_review-v1` 作为即时只读 Workflow Skill 从自然语言 Chat 请求进入现有 Run/SSE 生命周期。
- 以稳定的 owner、日期窗口和版本化证据快照生成可验证的每日回顾。
- 在输入未变化时跳过证据正文加载后的模型生成，并对并发重复请求进行 single-flight 协调。
- 将 Workflow 节点事件、模型/工具预算、证据门禁和 Chat Run 关联到统一 Harness 轨迹。

**Non-Goals:**

- 本次不实现自动定时生成、主动推送、订阅设置或外部通知；这些能力后续复用 Reminder/Dispatcher。
- 本次不实现每日洞察、跨日行为模式、统计归因或多 Agent。
- 本次不让 Daily Review 自动保存 Note、Memory 或 Reminder，也不把缓存记录当作新的长期记忆事实。
- 本次不把所有普通聊天和单步业务 Handler 强制迁移到 Workflow。
- 本次不实现通用 DAG、循环或并行 Workflow 引擎；数据读取的有限并发封装在单个受控节点内。

## Decisions

### 1. Skill 是业务契约，Workflow 是可选执行模式

新增 `internal/skill` 领域边界。Registry 保存非泛型的版本化 `Definition`，至少包含 ID、Version、ExecutionMode、Risk、Matcher、InputCodec、OutputCodec、Budget 和依赖声明；具体 Handler 或 Workflow Factory 通过受控接口注册。执行模式包括 `direct/stream/workflow/durable_workflow`，Daily Review 选择即时 `workflow`。

Registry 在 composition root 一次性构造，注册冲突和已启用 Skill 的缺失依赖使启动失败。路由只携带 `SkillRef` 与校验后的参数信封，不携带可执行函数或任意 owner 字段。相比让每个 Skill 增加新的 `HandlerSet` 字段，这允许后续昨日回声、那年今日、每日新知和行为洞察独立注册；相比把所有 Skill 固定为 Workflow，它仍允许简单查询使用 Direct Handler。

### 2. Router 产生统一 Target，并保持内建写意图优先

扩展路由决策为 `TargetKind=builtin|skill`，Skill Target 包含 `skill_id/version/arguments_hash` 和有界参数。现有草稿确认、Note/Memory/Reminder 明确写操作和拒绝删除规则先匹配；Daily Review Matcher 再识别“回顾今天/昨天/明确日期”等只读表达。一个请求只能产生一个 Target，“回顾并保存”第一版进入澄清，避免同时生成回顾和启动写 Workflow。

匹配成功后先在 `skill_invocations` 保存 Chat Run、owner、session、Skill/版本、参数哈希和状态。这个关联既用于审计与 SSE，也使活动查询能够排除 Daily Review 自己的用户触发消息和助手输出。相比从自由文本或 JSON 事件反推来源，显式 Invocation 事实稳定且可索引。

### 3. Daily Review 使用即时强类型顺序 Workflow

定义 `DailyReviewState`，只保存窗口、规范化选项、源快照摘要、缓存身份、允许证据引用、结构化结果、覆盖警告和控制标志，不保存 Access Token 或无界正文。`daily-review-v1` 节点顺序为：

```text
ResolveWindow
  -> SnapshotSources
  -> LookupOrClaimCache
  -> LoadDailyEvidence
  -> RecallMemoryContext
  -> GenerateStructuredReview
  -> ValidateEvidence
  -> RecheckSnapshotAndCommitCache
  -> Render
```

缓存命中后，后续加载、召回、生成和校验节点根据 `CacheHit` 做确定性 no-op，Render 输出缓存结果；这些节点不得消耗模型或工具预算。第一版复用现有顺序 Runner，不为了性能扩展 DAG。Chat/Note 元数据读取可在 `SnapshotSources` 内进行有限并发，但共享 context、deadline 和结果上限。

Daily Review 没有 Wait 和业务写副作用，因此不使用 DurableRuntime。缓存 claim 自身持久化并带短租约，可处理生成期间实例退出；如果后续增加后台生成或确认保存，再通过新 change 切换到 Durable Workflow，而不改变 Skill ID 的读语义。

### 4. 活动证据采用 metadata-first 快照和版本固定加载

新增 Daily Activity Reader 端口，分为 `Snapshot` 与 `LoadPinned` 两阶段。Snapshot 在 owner 和 `[start,end)` 范围内返回稳定排序的元数据：

- Chat：Message ID、Session ID、Run ID、Sequence、Role、CreatedAt 和 Content Hash，并排除关联到 `daily_review` Invocation 的消息。
- Note：Note ID、业务版本/Row Version、状态、OccurredAt、UpdatedAt 和 Content Hash，只包含当前 owner 可见记录。
- Memory：不在每日窗口中全量扫描。Snapshot 使用 owner-scoped 的单调 Memory Mutation Version；缓存未命中后再由现有 Recall 选择目标、偏好、约束、摘要和结果，并保存固定 MemoryRef。

Chat 与 Note 元数据按配置上限查询并生成规范化 SHA-256；总数、截断游标和覆盖标志也参与摘要。Memory Mutation Version 在 Memory 创建、状态转换、supersede、撤销和过期事务中单调增加，因此新增的潜在相关 Memory 也会使缓存失效，而不必为缓存命中执行语义检索。部署时为每个已有 owner 建立初始版本或惰性基线，已有缓存为空，不需要回填回顾结果。

缓存未命中后，`LoadPinned` 按元数据中的 ID、版本和哈希重新读取正文；任何不匹配都返回 `stale_snapshot` 并有界重建一次。Memory Recall 结果继续使用不可信标记，生成节点只能引用本次允许列表。相比直接把查询结果拼进 Prompt，这个两阶段协议既支持廉价缓存判断，也关闭查询与生成之间的 TOCTOU 窗口。

### 5. 缓存键分离逻辑身份与源数据指纹

缓存逻辑身份由以下字段的规范化哈希组成：

```text
tenant_id + user_id
+ local_date + timezone + [start,end)
+ normalized_options_hash
+ skill_id + skill_version
+ output_schema_version + prompt_policy_version
```

源数据指纹再包含 Chat 摘要、Note 摘要和 Memory Mutation Version。缓存表使用 `(owner, logical_key, source_fingerprint)` 唯一约束，记录 `generating/ready/failed`、claim token、lease、结果 JSON、渲染文本、证据清单哈希、最早有效截止时间和创建/更新时间。缓存读取永远带 owner 条件。

TTL 只用于容量控制和安全上限，不决定数据新鲜度。有效期取配置 TTL、相关 Memory 最早 `expires_at` 和策略版本有效期的最小值；即使 TTL 未到，只要任一源摘要或版本变化就 miss。缓存不保存完整证据正文，只保存结构化回顾、允许的稳定引用和必要展示文本。

相比仅使用 `MAX(updated_at)` 或固定 TTL，完整摘要能检测新增、修改和删除；Memory Mutation Version 能检测未进入旧 Recall 结果的新 Memory。代价是每次命中仍需做有界元数据查询，但避免了更昂贵的正文加载、RAG/Recall 和模型生成。

### 6. 缓存生成使用租约 single-flight，并在提交前二次校验

第一个 miss 请求通过唯一键创建或 claim `generating` 记录，在短事务外运行 Workflow。相同 owner、逻辑键和指纹的并发请求观察到有效 claim 后有界等待 ready 结果；租约过期后允许重新 claim。生成者使用 claim token 条件提交，旧实例不能覆盖新结果。

在写 ready 缓存前重新计算轻量源快照。若指纹变化，丢弃本次候选并最多重启一次；再次变化时返回稳定 `daily_review_source_changing`，而不是缓存或声称输出是最新结果。模型失败、引用失败或 Chat 取消只提交脱敏失败状态/错误码，不提交可返回结果。

### 7. 生成使用严格结构化输出和证据白名单

定义 `DailyReviewReportV1`，包含 window、highlights、completed、unfinished、goal_progress、reflection_questions、suggestions、coverage_warnings 和 evidence_refs。事实性数组项必须携带允许的证据引用；建议和反思问题必须标明它们是建议而非历史事实。结构化 Adapter 只接受单个有界 JSON 对象，拒绝未知字段、越权字段、无效 ID 和超限数组，并允许有限 repair。

Validator 对 Chat/Note 引用检查 owner、ID、版本/序号和哈希，对 Memory 引用检查 ID、Lineage Version、Content Hash 和当前 active 状态。无法验证的事实项不进入渲染；如果剩余内容不足，返回确定性覆盖提示。当天无 Chat/Note 活动时直接产生可缓存的空结果，不调用模型，也不把 Memory 单独描述为当天活动。

### 8. Workflow 与 Harness 通过适配器关联，但保留各自事实来源

Skill Executor 从 Chat Run context 启动 Harness budget，并为 Workflow 注入一个 Observer Adapter。Adapter 将 NodeEvent 映射为 `skill.step.*` 观测，同时保留 Workflow 自己的有序事件；Eino/Tool 调用继续通过现有 runtime 消耗模型与工具预算并产生 `model.*`、`tool.*`、`evidence.evaluated` 和 `answer.validated`。

Chat SSE 只暴露允许的 `skill.started/cache.hit|miss/step/completed|failed` 摘要和 `text.delta`，不暴露原始 Prompt、Memory 正文、缓存键或凭证。相比让 Workflow Observer 直接替代 Harness，这个桥接保持现有 Runner 与模型/工具拦截器职责清晰。

### 9. Feature Flag 和兼容路径默认安全

新增 Skill 总开关、Daily Review 开关、日期范围、记录/字符上限、Workflow/模型/工具预算、缓存 TTL/lease/wait、repair 次数和版本配置。Skill 总开关关闭时不注册任何 Skill Target；Daily Review 关闭或依赖不完整时不允许 Router 命中该 Skill。普通聊天和现有内建意图继续走当前 Executor。

由于 `add-memory-aware-reminder-workflow` 也会修改统一 Router，实施时先落通用 Target/Registry，再让 Memory、Reminder 和 Daily Review 各自注册或适配，避免并行维护两套互斥分类器。HTTP 层不得新增 Daily Review 副作用旁路。

## Risks / Trade-offs

- [每次缓存命中仍需元数据查询] → 使用 owner/time-window 索引、严格上限和 metadata-only 投影；缓存目标是避免正文加载、Recall 和模型成本，而不是零数据库访问。
- [Daily Review 自己的消息导致指纹变化] → 以显式 `skill_invocations` 关联排除该 Skill 的触发和输出，并增加连续多次触发回归测试。
- [Memory 新增但旧 Recall 引用未变化] → 将 owner-scoped Memory Mutation Version 纳入源指纹，并把 cache valid-until 限制到最早 Memory expiry。
- [生成期间持续有新活动导致饥饿] → 提交前只允许一次重建，之后返回稳定的“数据仍在变化”提示，由用户稍后重试。
- [缓存持有敏感回顾文本] → owner-scoped 条件、最小结果字段、有限 TTL、清理任务和脱敏日志；缓存键、指标和事件不包含正文。
- [规则匹配漏掉自然语言表达] → 维护中文语料评测并允许后续引入严格结构化 Matcher；低置信度无副作用地澄清，不把 Skill 权限交给模型。
- [Workflow 与 Harness 双事件造成重复指标] → 明确映射表和唯一 correlation ID，Workflow 负责业务节点事实，Harness 负责资源调用和聚合指标。
- [活动过多造成摘要偏向最近数据] → 稳定分层采样、每会话配额和 coverage warning；首版不宣称完整行为洞察。

## Migration Plan

1. 部署 Skill Invocation、Daily Review Cache 和 Memory Mutation Version 所需的兼容迁移与索引，保持 Skill/Daily Review 开关关闭；已有业务数据不改写，缓存为空。
2. 实现 Registry、统一 Target、Skill Executor 和 Workflow/Harness Observer Adapter，只注册测试 Skill，运行现有 Chat/Note/Memory/Reminder 路由回归。
3. 实现 Chat/Note metadata snapshot、Memory mutation/version 维护和 pinned load，验证跨 owner、时间边界、排除 Skill 自身消息及所有变更类型的缓存失效。
4. 装配 `daily_review-v1` Workflow、结构化生成、证据校验、缓存 claim 和 SSE 映射，在测试环境开启并验证连续/并发触发。
5. 小范围开启 Daily Review，观察匹配歧义、生成延迟、缓存命中、失效原因、覆盖警告和证据失败率，再逐步放量。

回滚时先关闭 Daily Review 和 Skill 路由，再停止新的 cache claim；普通 Chat 与内建 Handler 保持可用。已写入的 Invocation、缓存和版本记录可以保留并由 TTL 清理，不回滚用户 Note、Memory 或 Chat 数据，因为本 Skill 默认只读。

## Open Questions

无。首版采用用户主动触发、`Asia/Shanghai` 默认时区、即时只读 Workflow 和 MySQL owner-scoped 缓存；自动调度、跨日洞察与保存行为留给后续独立 change。
