## Context

参见 `proposal.md` 的动机。当前 `internal/memory` 已实现结构化 Record、状态机、MySQL Repository、确定性冲突 Policy、Recall 排序和 Projection Outbox；`internal/memoryworkflow` 已实现可耐久的 Recall/Extract/Conflict/Review/Commit 节点。现存约束是：

- `RecallService` 构造时强制要求 RAG searcher，无法以合法配置运行 MySQL-only。
- Capture 当前在 Extract 之前 Recall，尚无 namespace、slot 或 EntityRef 可供精确定位。
- `DraftExtractor`、`ConflictResolver` 和 `EditLoader` 只有端口与测试 fake，没有生产 LLM 适配器。
- 服务入口没有读取 `cfg.Memory` 或装配 Memory Runtime、控制面和指标。
- active Memory 总会产生 Outbox，但其版本被硬编码为 `default`；RAG 关闭时没有 Worker，适合未来回填但需要明确运行语义。
- 现有 DurableRuntime、MySQL workflow store、认证 principal 和 HTTP server option 模式可复用，不另建 Workflow 引擎。

## Goals / Non-Goals

**Goals:**

- 在 RAG 和 Projection 关闭时运行完整的显式 Memory 生产、审核、提交和精确消费闭环。
- 让 LLM 只承担自然语言到受限结构的归一化与候选关系提议，所有权、候选集合和状态变化仍由确定性代码控制。
- 以稳定选择器定位冲突和召回对象，避免无条件扫描或把文本相似当作业务身份。
- 保持已有 Memory 数据、状态机、Outbox 和未来语义召回兼容。
- 使用默认关闭的开关和独立 Pilot，确保现有 Chat/Note 行为不受影响。

**Non-Goals:**

- 不实现或修改外部 RAG 项目的 `/v1/memories/*`。
- 不实现 MySQL FULLTEXT、Embedding 或开放式语义召回。
- 不实现 Task/Reminder 领域、定时调度或把 Memory 作为提醒事实源。
- 不自动分析每条消息，也不保存完整聊天历史或模型思维过程。
- 不改变普通 Chat Run、SSE active guard 和已有通用 Workflow 的公开行为。

## Decisions

### 1. 将自然语言归一化拆成 Draft 和 Recall Plan 两种严格契约

生产路径使用 `MemoryDraft`，消费路径使用 `StructuredRecallPlan`。两者共享 namespace、slot、EntityRef、scope、layer 和 kind 的规范化规则，但职责不同：Draft 描述待保存事实，Plan 只描述如何精确寻找事实。

生产适配器调用现有模型 Runner，要求单个 JSON 对象，随后执行：响应大小限制、严格解码、未知字段拒绝、枚举与数量校验、规范化和内容哈希重算。允许使用现有有界 repair budget 修复语法错误，但 repair 仍必须通过相同校验。Owner、状态、SQL、Memory ID 和审核决策不在 schema 中。

选择该方案而非让 LLM 输出 SQL，是因为它可以在 Repository 前建立稳定安全边界；选择两个契约而非一个万能 schema，是为了避免消费查询意外携带写入权限或写入 Draft 混入查询控制字段。

### 2. Recall Service 支持显式 exact-only 与 exact-plus-semantic 模式

`RecallService` 保留统一调用入口，但语义 searcher 改为可选依赖。运行时根据配置确定模式：

```text
exact-only          = MySQL exact + pinned refs
exact-plus-semantic = MySQL exact + optional RAG candidates
```

exact-only 不构造 RAG Client、不进入向量分页循环，也不产生 `rag_unavailable`。两种模式共享 owner/status/expiry 过滤、确定性排序、去重、上下文预算与不可信 Prompt 封装，以便未来启用 RAG 时上层 Workflow 无需改接口。

替代方案是注入永远返回空数组的 Noop Searcher。该方案改动小，但会把“功能关闭”伪装成一次成功的语义调用，污染扫描量、降级原因和指标，因此不采用。

### 3. Recall Plan 只展开为有界的稳定选择器

Plan 经校验后展开成若干 `ExactQuery`，支持：

1. checkpoint 中可信的 pinned MemoryRef；
2. EntityRef；
3. namespace + slot key；
4. content hash；
5. 显式 session/workflow scope 内的上述选择器。

Repository 每次查询始终附加 tenant、user 和 scope，使用有界 limit，并增加 layer、kind 和 active/expiry 过滤能力。多个 selector 可批量或分组执行，结果按 ID 合并。没有稳定 selector 时直接返回空结果或澄清信号，不允许退化为 owner 全量列表。

确定性排序为：pinned 精确版本 > EntityRef > namespace/slot > content hash > authority > salience > recency > ID。LLM 不参与最终排序。

### 4. Capture 改为先 Extract，再加载冲突候选

节点图调整为：

```text
Extract
  → ExactCandidateLookup
  → Conflict
  → Review
  → Commit
```

`CaptureData` 保存有界候选 `MemoryRef`、候选匹配类型和必要的统计，不把无界正文写入 checkpoint。Conflict 节点按 owner 重新 BatchGet 固定候选并验证 active/version/hash 后，将完整但有界的记录传给 `ConflictResolver`。

`ConflictResolver` 的端口改为显式接收候选集；LLM 只能针对允许 ID 返回 duplicate、refinement、correction、contradiction、temporal_change 或 independent 提议。现有 `DecidePolicy` 仍是唯一能够选择 noop、supersede、review、reject 或 independent 的组件。

这避免 Concrete Resolver 在内部隐式查询 Repository，使候选边界、重放和单元测试可观察。

### 5. 所有长期写入先进入耐久 Review

Pilot 首版对显式“记住/修改”意图生成 candidate，并进入 Review Wait。即使用户原句看似明确，也不在 Chat 请求内直接激活长期记忆；批准、拒绝和编辑通过后续认证请求恢复同一 Run。编辑内容保存为有界 payload，由 owner-scoped `EditLoader` 加载，重新 Extract/Normalize、精确找候选和冲突判断。

保留未来对 trusted-system 或明确用户确认的自动 active 策略空间，但不在本变更开放，以降低误写长期记忆的风险。

### 6. 提供 Memory 专用应用服务和薄 HTTP 控制面

新增 Memory Capture 应用服务封装 typed DurableRuntime，不让 Handler 直接编排节点。控制面提供：

```text
POST /v1/memory-captures
GET  /v1/memory-captures/:run_id
GET  /v1/memory-captures/:run_id/review
POST /v1/memory-captures/:run_id/resume
```

resume body 携带 wait ID、version、content hash、action 和可选 edit payload。Handler 从认证 Principal 构造 WorkflowOwner 和 Actor，忽略 body 中任何 owner 字段；跨 owner 的 run/wait 统一映射为 not found。响应只包含有界 Draft、策略、Wait 和 MemoryRef，不返回凭证、完整 checkpoint 或审计内部字段。

显式记忆意图的 Chat Pilot 只负责异步/独立启动 Capture，并允许原 Chat 正常结束；Review 不占用 Chat active guard。

### 7. 在启动入口按配置组合运行时

装配矩阵如下：

| Memory | Pilot | RAG | Projection | 行为 |
|---|---|---|---|---|
| off | 任意 | 任意 | 任意 | 不注册 Memory 能力，保持现状 |
| on | off | off | off | 可构造底层服务但不暴露 Pilot 控制面 |
| on | on | off | off | 启动 MySQL-only Recall、Capture Runtime 与 API |
| on | 任意 | on | on | 保留未来语义 Recall 和 Projector 装配路径 |

Pilot 开启时，模型 Runner、数据库、严格 LLM 适配器和 Durable Store 初始化失败属于启动错误。RAG 明确关闭不参与 readiness。所有 goroutine 都派生自进程根 context，并在 HTTP shutdown 前停止；本变更的 MySQL-only 模式不启动 Projector goroutine。

### 8. Projection Outbox 在关闭期间作为延迟回填日志

active Memory 继续与事实事务一起创建唯一 Outbox，以免未来启用 RAG 时遗漏历史数据。Projection 关闭时不 Claim、不重试、不告警，只暴露 disabled/deferred 状态和可选 backlog gauge。启用后 Worker 处理仍为 active 且哈希匹配的记录；已 obsolete 的记录会被跳过并完成 Outbox。

MySQL Store 在构造时接收配置的 Projection Version，并用于 Outbox ID 与 `model_version`，替代硬编码 `default`。这样不需要改变 Memory 内容或在回填时猜测索引代际。

替代方案是不创建 Outbox、启用 RAG 后全表扫描 active Memory。该方式需要额外水位、分页和重建作业，且难以区分历史遗漏，因此本次保留事务 Outbox。

### 9. 指标区分 exact-only、功能关闭与依赖故障

Recall 指标增加 mode、selector 命中、无选择器、obsolete 过滤、截断和澄清计数。Capture 指标记录 started、suspended、approved、rejected、edited、completed 和错误码，不记录正文。Projection disabled 不计为错误；只有配置启用后调用失败才记录 `rag_unavailable` 或投影失败。

## Risks / Trade-offs

- [自然语言无法稳定映射到 selector，召回率低于向量检索] → 对低置信度和多实体请求澄清；先聚焦偏好、资料、目标和业务实体等有稳定槽的场景，保留统一 Recall 接口供后续加 RAG。
- [LLM 生成错误 namespace 或 slot 导致找不到旧事实] → 使用版本化 taxonomy、严格 schema、规范化别名和测试语料；无匹配时不自动新增冲突版本，交由 Review 暴露给用户。
- [候选过少造成重复 Memory] → 写入冲突查找同时使用 EntityRef、namespace/slot 和 content hash，并对无稳定定位的长期写入强制 Review。
- [候选过多增加 LLM 成本或 checkpoint 大小] → 每 selector、总候选和正文均设上限；checkpoint 只保存 MemoryRef 与匹配元数据，Conflict 时 owner-scoped 重载。
- [关闭 Projection 时 Outbox backlog 持续增长] → 这是预期的延迟回填日志；关闭告警但暴露容量指标，并通过唯一键保证每个 active 内容版本至多一条。
- [启动装配改变现有服务稳定性] → 所有新能力默认关闭，先通过 API 显式试点，再接 Chat 意图；关闭开关即可回到原行为。
- [编辑与其他请求并发修改同一事实槽] → 使用现有 row_version、active slot 唯一约束和 pinned version 校验，陈旧审核返回状态冲突而不覆盖新事实。

## Migration Plan

1. 先部署代码和必要的查询索引/兼容迁移，保持 `MEMORY.ENABLED=false`，验证旧功能回归。
2. 在测试环境启用 `MEMORY.ENABLED=true`、`WORKFLOW_PILOT_ENABLED=true`，保持 RAG 与 Projection 关闭，执行 MySQL-only 端到端场景。
3. 验证显式 Capture、重启恢复、跨 owner 拒绝、重复提交幂等、用户编辑后 supersede 和精确消费。
4. 对小范围用户开放显式 Memory API/Intent Pilot，观察无 selector、澄清、审核和 Outbox backlog 指标。
5. 稳定后逐步把精确 Recall 接入选定业务 Workflow 节点；不自动扩大到所有 Chat。
6. 未来 RAG Memory API 就绪后，单独启用 RAG/Projection，消费历史 Outbox 并比较 exact-only 与 hybrid 指标。

回滚时先关闭 Workflow Pilot，再关闭 Memory；已提交 MySQL Memory、审计事件和 Outbox 均保留，不执行破坏性迁移。重新启用后可以继续恢复未过期 Wait 和读取既有 active Memory。
