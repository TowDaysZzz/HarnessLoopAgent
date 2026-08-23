## Context

See `proposal.md` for motivation and the three delta specs for behavior requirements. 当前 `internal/memory` 只有未接生产链路的 `Item`、通用 Store 和 CandidateService；聊天上下文只保留有界最近消息，RAG Client 只支持笔记文档与最多 20 条的证据检索。另一方面，项目已有 MySQL 迁移与事务 Outbox、严格 tenant/user scope、强类型耐久 Workflow、版本化 checkpoint、HITL Wait 和稳定节点 `ExecutionID`。

现有架构要求 Agent 通过 HTTP 使用独立 RAG 服务，不直接连接 Milvus；Access Token、Cookie 和服务密钥不得进入 Memory、Outbox 或 Workflow checkpoint。耐久 Workflow 节点具有 at-least-once 副作用语义：节点副作用可能成功而 checkpoint 提交失败，因此 Memory 写入必须独立幂等。

## Goals / Non-Goals

**Goals:**

- 建立可替换当前 Memory 草图的强类型领域模型和用例级 Store 端口。
- 让 MySQL 对 Memory 当前事实、版本、关系、审计与幂等性拥有唯一权威。
- 建立旧向量不变的 append-only RAG 投影，以及必须回查 MySQL 的召回管线。
- 以确定性规则约束 LLM 关系判断，并提供可复用的 Workflow Memory 节点协议。
- 保持现有 Note、Chat、SSE、Grounding 和 Workflow Core 的外部行为不变。

**Non-Goals:**

- 不在本 change 内建立 Task/Reminder 表、调度 Worker 或自然语言提醒更新 API。
- 不自动分析全部 Chat Run，也不在通用 Observer 或 checkpoint hook 中隐式写 Memory。
- 不增加 Memory 管理 UI、公开 CRUD API、知识图谱或多 Agent 共享空间。
- 不宣称跨 MySQL 与 RAG exactly-once；RAG 投影采用幂等的最终一致性。
- 不让 RAG citation 契约或笔记知识库承担 Memory 当前事实查询。

## Decisions

### 1. 三层是生命周期边界，不是三个相同的 Store

工作记忆继续使用 `WorkflowState[T]`、Chat Run 和有界上下文，不写入 Memory 表。会话记忆与长期记忆共享统一的版本化 Memory 聚合，但通过 `Layer`、`Scope` 和过期策略区分：会话记忆必须绑定 session/workflow scope 和 TTL；长期记忆绑定 user scope，并受来源权威与确认策略控制。

相比为三层各建独立仓库，共享聚合能复用版本、冲突、审计和召回策略；相比把全部 checkpoint 写成 Memory，本边界避免临时推理污染长期事实。

### 2. 一个 Memory ID 表示一个不可变语义版本

Memory 领域至少包含：

- `Owner{TenantID, UserID}`、`Layer`、`Kind`、`Scope{Type, ID}`。
- `Namespace` 与 `SlotKey`，用于表示同一作用域内只能有一个当前值的事实槽。
- 可选 `EntityRef{Type, ID}`，用于关联未来 Task、Reminder 或其他业务实体。
- `LineageID`、`LineageVersion` 和 `RowVersion`：分别表示逻辑事实链、语义版本和数据库并发版本。
- `CanonicalText`、有界 `StructuredValue`、`ContentHash`、`Authority`、`Confidence`、`Salience`。
- `SourceRef`、证据引用、有效期、状态以及 `SupersedesID/SupersededBy`。

正文或结构化值实质变化时创建新 Memory ID，并沿用 Lineage ID、增加 Lineage Version；旧记录只允许状态、替代引用、Row Version 和审计时间发生变化。相比原地改写同一个向量与数据库行，这使历史可审计，并与 append-only 索引一致。

### 3. MySQL 使用当前事实表、不可变事件、关系和 Outbox

新增顺序迁移建立：

- `memory_records`：每个语义版本一行，包含 owner、层级、scope、事实槽、实体引用、版本、内容、状态、权威、来源和有效期。
- `memory_events`：不可变保存 proposed、activated、superseded、rejected、revoked、expired 及旧/新状态、actor、原因、Workflow 身份和幂等键。
- `memory_relations`：保存 supersedes、duplicate_of、conflicts_with、refines、derived_from 和 related_to 的多对多关系。
- `memory_projection_outbox`：只保存 Memory ID、内容哈希、投影模型/版本、claim、退避和处理状态，不保存凭证。

`memory_records` 使用 generated `active_slot_guard` 和唯一键约束 `(tenant,user,scope_type,scope_id,namespace,slot_key,active_slot_guard)`；空 Slot 不参与唯一 current 约束。`is_obsolete` 若提供则由状态生成，不能成为独立可写真相。所有 owner-scoped BatchGet 和转换查询同时带 tenant 与 user 条件。

相比把所有历史塞进一个 JSON 字段，拆表支持唯一性、条件更新、关系查询与审计；相比只保存事件再运行时重放，当前事实表更适合高频 BatchGet。

### 4. 用例级 Repository 原子提交 Memory Mutation

替换通用 `Put/List/Delete`，领域服务使用用例级端口：精确候选查询、owner-scoped BatchGet、候选创建、确认/拒绝、原子 Mutation、撤销、过期、投影 claim/complete/fail。`CommitMutation` 输入包含新 Memory、待替代目标及 expected Row Version、关系、actor、原因和幂等键。

Store 在单个短事务中：

1. 按 owner 锁定并验证全部目标仍为预期状态和版本。
2. 插入新 Memory；更新旧 Memory 的状态与替代引用。
3. 插入关系、事件和需要的投影 Outbox。
4. 提交后返回确定的 Mutation Result。

幂等键唯一约束绑定 owner，并保存输入摘要；相同键与相同摘要返回原结果，不同摘要返回稳定 idempotency conflict。相比 Service 组合多个 Save，此端口不允许绕过事务形成半替代状态。

### 5. 冲突解析使用“双通道召回 + LLM 提议 + Policy 裁决”

Memory 写入先规范化文本、结构化值、事实槽、EntityRef、来源权威与内容哈希。候选集合为 MySQL 精确候选与 RAG 语义候选的并集：

- 精确候选：相同 EntityRef、Namespace/SlotKey、内容哈希或当前 Workflow 固定引用。
- 语义候选：RAG 返回的相关 Memory ID，经 MySQL BatchGet 和 active/expiry/scope 过滤。

LLM 只返回结构化 `RelationProposal`：候选 Memory ID、关系类型、冲突字段、置信度、原因码和是否建议确认。Policy 使用稳定优先级裁决：显式实体/当前 Workflow 引用 > 当前用户明确纠正 > 用户确认事实 > 可信业务事实 > 模型推断 > 纯相似度。LLM 输出不得成为 Store 命令。

明确 Hash 重复走 no-op/追加证据；明确用户纠正可 supersede；模型推断与 confirmed 事实冲突时进入 candidate/HITL；不同 EntityRef 默认 independent。相比 Mem0 早期由模型直接选择 ADD/UPDATE/DELETE，本设计保留其相关候选和历史思路，但把业务状态权交给确定性策略。

### 6. RAG 使用 Memory 专用的 append-only HTTP 契约

扩展 `internal/ragclient` 的独立接口，不复用笔记 citation 响应：

- Index 请求携带稳定 `memory_id`、不可变文本、内容哈希、层级、类型、创建时间和投影版本；相同投影键幂等，旧 Memory 后续 obsolete 时不更新或删除向量。
- Search 请求携带 query、允许的 layer/kind、候选数量与 cursor；owner scope 由可信运行时注入并在 RAG 服务端强制应用。响应只返回 Memory ID、score 和分页信息。
- Rebuild 使用从 MySQL 选择的投影集合构建新 generation，验证后切换 Alias；不原地清理历史向量。

RAG Memory API 使用 Agent 服务身份和受约束 owner claim，不依赖持久化用户 Access Token。相比 Agent 直接使用 Milvus，本契约保持现有服务边界；相比复用 `/v1/retrieve`，专用返回避免 citation/chunk 语义与 Memory ID 混淆。

### 7. Outbox 投影只追加 active Memory

直接创建的 active Memory 和 candidate 后续激活时在同一 MySQL 事务写唯一 Outbox。Projector claim 小批量事件，重新 owner-scoped 加载 Memory，验证 ID、内容哈希和投影资格，调用 RAG Index，再幂等 complete；临时失败按有界指数退避，永久契约错误保留 failed 状态和可观测错误。

被 supersede/revoke/expire 的 Memory 不产生向量更新或删除事件。其旧向量仍可能召回，因此任何在线读取都必须回查 MySQL。相比同步双写，此协议允许 RAG 故障而不破坏事实事务；代价是投影延迟与旧向量污染，需要下一项的自适应召回。

### 8. Recall 以有效结果数量驱动自适应扫描

Recall 接受 owner、query、可见 scope、layer/kind、目标有效数量、最大扫描数量和上下文预算。它先加载精确引用，再调用 RAG 取得过量候选，按顺序 BatchGet、过滤 owner/status/expiry/scope、对已处理 ID 去重；有效数量不足且仍有 cursor 时继续拉取，直到满足目标或命中扫描/时间上限。

有效候选再按精确实体匹配、语义分、Authority、Salience、时间与确认状态重排，并按 Token/字符预算输出。返回值包含 truncated、scanned、obsolete filtered 和降级原因，便于评测。Memory 正文作为标记后的不可信数据上下文，不得成为系统指令。

固定 top20 在大量历史版本时可能完全被 obsolete 命中占满；自适应分页在保证上限的同时提高 active recall。若 RAG 初期不支持 cursor，可临时以递增 TopK 模拟，但对外端口保持分页语义。

### 9. Workflow 通过业务 adapter 接入，不修改 Core 依赖方向

`internal/workflow` 不依赖 `internal/memory`。在 Memory 或具体业务包提供可组合 adapter/节点：Recall、Extract Draft、Resolve Conflict、Review 和 Commit。Workflow 的业务 Data 只保存有界 `MemoryRef{ID, LineageVersion, ContentHash}`、结构化 Draft、Policy Result 和 Wait PayloadRef。

副作用节点使用 `NodeInput.ExecutionID + mutation index` 作为 Memory 幂等键。如果 Memory 事务成功但 Workflow checkpoint commit 失败，重放获得相同 Mutation Result。恢复前精确验证固定引用；若已 obsolete，则按业务定义重新召回、进入 Wait 或失败，绝不静默换版本。

Review 使用已有 Wait ID、版本、内容哈希、Actor 和 allowed actions。Chat Run 只报告已启动或待审核业务状态并可完成，不将 Workflow suspended 映射成 Chat active guard。首个试点采用内部可验证的 Memory Capture Workflow 与装配测试，不自动接管现有聊天路由。

### 10. 安全校验位于领域和传输边界

Memory codec/validator 对结构化值、证据和 checkpoint 使用字段白名单、条数/字节上限与敏感键检测；事件和指标只保存固定字段及有界原因码。服务端从认证上下文或耐久 Workflow Owner 构造 owner，调用者不能通过 LLM 参数覆盖。

RAG 返回属于不可信外部输入：限制响应大小、候选数量、ID 格式、score 范围和 cursor 长度。BatchGet 再次执行 owner 条件，实现纵深隔离。相比仅依赖向量 metadata filter，这能在 RAG 配置或索引异常时防止跨租户内容进入 Prompt。

### 11. 配置、开关和可观测性

新增独立 Memory 配置段，至少包括 enable、默认 TTL、候选/扫描/上下文上限、冲突阈值、投影批量与退避、RAG Memory endpoint/timeout、服务授权方式和 Workflow 试点开关。默认关闭生产接入；迁移部署后表为空，旧行为不变。

记录但不含正文的指标：召回候选数、obsolete 命中率、扫描深度、有效结果数、冲突类型、HITL 率、幂等重放、状态冲突、投影延迟/失败和索引 generation。日志使用 Memory/Workflow ID，禁止记录凭证与完整敏感内容。

## Risks / Trade-offs

- [风险] append-only 索引积累大量 obsolete 向量，增加扫描成本并挤压 active TopK → 使用 cursor/自适应 over-fetch、最大扫描预算和 obsolete 比率指标；超过阈值时从 MySQL 构建新 generation 并切换 Alias。
- [风险] RAG 投影最终一致导致新 Memory 短期无法语义召回 → 精确 EntityRef/Slot 查询仍走 MySQL，暴露 projection pending 状态并重试 Outbox。
- [风险] LLM 漏判或误判冲突 → LLM 只产生提议；事实槽、EntityRef、Authority、版本和状态由确定性 Policy/Store 校验，低置信度进入 HITL。
- [风险] JSON StructuredValue 演化失控或包含敏感数据 → 按 Kind 注册 schema/version validator、限制大小和深度，并增加敏感键与序列化回归测试。
- [风险] Memory 副作用与 Workflow checkpoint 不在同一事务 → 用 ExecutionID 幂等和输入摘要重放，测试副作用后崩溃；不宣称跨边界 exactly-once。
- [风险] 同一 Slot 的 unique guard 与无 Slot 的情景记忆语义不同 → 只有非空 Slot 参与 active 唯一约束，情景事件使用独立 Lineage 和显式关系。
- [风险] 服务身份携带 owner claim 设计不当可能扩大 RAG 权限 → 使用短期签名 claim、受众限制、服务端主体校验和双重 owner 回查，不持久化用户 Token。
- [风险] 本 change 同时跨领域、数据库、RAG 和 Workflow，回归面较大 → 按下述迁移阶段保持功能开关关闭，分别完成领域、Store、RAG、Recall 与试点 Workflow 验证后再启用。

## Migration Plan

1. 替换未接生产的 Memory 草图，先加入领域类型、状态机、Policy 和内存 fake；不修改服务装配。
2. 增加幂等 MySQL 迁移和 Repository，建立空的 Memory/事件/关系/Outbox 表；部署不回填 Note、Draft 或聊天历史。
3. 增加 RAG Memory Client 契约与 mock/integration tests；RAG 端尚未部署时保持投影和召回开关关闭。
4. 增加 Outbox Projector、Recall Service、自适应过滤与重排；用 fake RAG 和 MySQL 集成测试验证失败恢复、跨租户过滤和 obsolete 污染。
5. 增加未接现有聊天路由的 Memory Capture Workflow 试点，验证 HITL、重启恢复、checkpoint 数据最小化与 ExecutionID 重放。
6. 完成安全、并发、索引重建、race、全仓测试和灰度指标后，再由后续 change 选择业务 Workflow 或 Chat Context 接入。
7. 回滚应用时关闭 Memory/Workflow 开关并停止 Projector；保留 MySQL 表和 RAG generation，避免删除事实。完全移除前必须确认没有生产 Memory，再用独立运维迁移处理数据。
