## 1. Memory 领域模型与状态机

- [x] 1.1 用强类型的 Owner、Layer、Scope、Kind、EntityRef、Lineage、MemoryRef、SourceRef、Authority 和生命周期状态替换现有 Memory 草图，并通过领域类型单元测试验证会话记忆必须有 scope/TTL、长期记忆必须是 user scope、工作记忆不会进入持久化模型
- [x] 1.2 实现 CanonicalText、StructuredValue schema/version、ContentHash、字段大小与深度限制的规范化和校验，并通过表驱动测试验证稳定哈希、非法结构和超限输入被拒绝
- [x] 1.3 实现 `candidate`、`active`、`rejected`、`superseded`、`revoked`、`expired` 状态机及终态约束，并通过状态转换矩阵测试验证所有允许与禁止路径
- [x] 1.4 定义精确候选、owner-scoped BatchGet、原子 CommitMutation、确认/拒绝、撤销/过期及投影 claim/complete/fail 的用例级 Repository 端口和内存 fake，并通过契约测试验证 fake 的 owner 隔离、版本检查与幂等行为

## 2. 冲突识别与确定性策略

- [x] 2.1 实现基于内容哈希、Namespace/SlotKey、EntityRef 和固定 MemoryRef 的精确候选收集与去重，并通过测试验证相似但 EntityRef 不同的事实保持独立
- [x] 2.2 定义受限的 LLM `RelationProposal` codec，覆盖 duplicate、refinement、correction、contradiction、temporal_change 和 independent，并通过畸形响应、未知 Memory ID、越界置信度及超限原因测试验证模型不能直接生成 Store 命令
- [x] 2.3 实现按固定引用、用户明确纠正、用户确认事实、可信业务事实、模型推断和纯相似度排序的 Policy Engine，并通过决策表测试验证 duplicate no-op、明确纠正创建新版本、低权威冲突进入 candidate/HITL、独立事实不被替代
- [x] 2.4 实现敏感键和值检测、来源证据白名单和有界原因码校验，并通过安全测试验证 Access Token、Cookie、密码和服务密钥不会进入 Memory、事件、日志或模型冲突请求

## 3. MySQL 结构与 Repository

- [x] 3.1 新增顺序数据库迁移，创建 `memory_records`、`memory_events`、`memory_relations` 和 `memory_projection_outbox`，包含 owner/scope 索引、Lineage 唯一约束、非空 active slot 唯一 guard、行版本、幂等输入摘要及 Outbox claim/退避字段，并通过迁移 up 与 schema 检查验证约束生效
- [x] 3.2 实现 owner-scoped 精确查询和 BatchGet，统一处理 active、过期、可见 scope 与 not-found 语义，并通过 MySQL 集成测试验证跨 tenant/user ID 不泄露且非 active 记录不会出现在有效查询中
- [x] 3.3 实现单事务 CommitMutation，原子插入新版本、条件替代旧版本、双向引用、关系、审计事件、幂等结果和 active 投影 Outbox，并通过故障注入集成测试验证任一步失败均完整回滚
- [x] 3.4 实现 expected RowVersion、active slot 唯一性和幂等键/输入摘要冲突处理，并通过并发集成测试验证同一旧版本只能产生一个 active 后继、重复重放返回原结果、同键异内容稳定报错
- [x] 3.5 实现 candidate 确认/拒绝、active 撤销和惰性或批量过期用例，并通过集成测试验证事件 actor/reason 完整、非法终态恢复被拒绝、状态变化不触发旧向量更新或删除

## 4. RAG Memory HTTP 契约

- [x] 4.1 在 `internal/ragclient` 增加独立于笔记 citation 的 Memory Index/Search 数据结构与 Client 端口，支持稳定 Memory ID、score、cursor、layer/kind、投影版本和可信 owner scope，并通过序列化与 mock HTTP 测试验证请求响应契约
- [x] 4.2 在传输边界注入服务身份和受约束 owner claim，拒绝调用方覆盖 tenant/user/Collection，并通过测试验证 checkpoint 或模型参数中的范围字段不会进入可信授权范围
- [x] 4.3 对 RAG 响应实施 body、候选数、Memory ID、score 和 cursor 限制以及临时/永久错误分类，并通过恶意响应、超时、限流和契约错误测试验证安全失败与重试分类
- [x] 4.4 定义按 MySQL 投影集合构建新 generation、校验并切换 Alias 的重建端口或运维入口，并通过 fake RAG 测试验证模型升级或 Collection 丢失时无需改变 Memory 事实即可重建

## 5. Append-only 投影 Outbox

- [x] 5.1 实现 Outbox worker 的小批量 claim、owner-scoped Memory 重载、ID/ContentHash/投影资格复验、幂等 Index 和 complete 流程，并通过集成测试验证只追加 active 会话/长期 Memory 且重复投递不产生重复投影
- [x] 5.2 实现临时错误的有界指数退避、永久错误保留和无正文可观测字段，并通过时钟可控测试验证重试时间、最大错误长度和凭证不会被持久化或记录
- [x] 5.3 验证 supersede、revoke、expire 在线路径不生成 update/delete 投影操作，并通过 fake RAG 调用断言确认旧向量保留原地且 MySQL 状态提交不依赖 RAG 可用性

## 6. Memory Recall 管线

- [x] 6.1 实现精确引用优先、RAG Memory ID 搜索、owner-scoped BatchGet、active/expiry/scope 过滤和跨页去重的 Recall Service，并通过测试验证未知、越权和 obsolete ID 被忽略
- [x] 6.2 实现以目标有效结果数为停止条件的 cursor 自适应 over-fetch，配置最大扫描数、批次数和时间预算，并通过旧版本占满首批结果的测试验证继续扫描且绝不以 obsolete 内容补足
- [x] 6.3 实现结合精确实体匹配、语义分、Authority、Salience、时间和确认状态的确定性 rerank，并通过固定样例测试验证排序稳定
- [x] 6.4 实现字符或 Token 上下文预算、truncated/scanned/obsolete-filtered/降级原因统计和不可信 Memory 数据包装，并通过预算测试验证超限截断且 Memory 中的指令文本不会被提升为系统指令

## 7. Workflow Memory Adapter 与试点

- [x] 7.1 在不让 `internal/workflow` 依赖 `internal/memory` 的前提下实现可组合 Recall、Extract Draft、Resolve Conflict、Review 和 Commit adapter，并通过依赖检查与节点单元测试验证 Core 依赖方向不变
- [x] 7.2 使用 `NodeInput.ExecutionID + mutation index` 构造稳定 Memory 幂等键并绑定输入摘要，通过“Memory 提交成功、checkpoint 提交失败后重放”测试验证返回同一 Mutation Result
- [x] 7.3 在业务 Workflow Data 中只持久化有界 `MemoryRef{ID, LineageVersion, ContentHash}`、结构化 Draft、Policy Result 和 Wait PayloadRef，并通过 codec 测试验证完整聊天、无界召回正文和认证凭证被拒绝
- [x] 7.4 实现恢复前固定 Memory 引用校验，通过测试验证引用未变化时继续原版本、已 superseded/revoked/expired 或哈希不匹配时按业务策略重新召回、进入 Wait 或失败且不静默换版本
- [x] 7.5 实现基于既有耐久 Wait 的批准、编辑和拒绝流程，校验 Wait ID、版本、内容哈希、Actor 与 allowed actions，并通过重启恢复测试验证编辑会重新运行冲突策略、拒绝内容不投影
- [x] 7.6 增加默认关闭且不接管现有聊天路由的 Memory Capture Workflow 试点和装配测试，验证 Chat Run 可在 Memory Review suspended 时正常结束并能由后续认证请求恢复同一 Workflow Run

## 8. 配置、可观测性与回归验证

- [x] 8.1 增加默认关闭的 Memory、RAG Memory、投影和 Workflow 试点配置，覆盖 TTL、候选/扫描/上下文上限、阈值、批量、退避、endpoint、timeout 和服务授权，并通过配置加载测试验证安全默认值及非法组合失败
- [x] 8.2 增加不含正文的召回、obsolete 比率、冲突、HITL、幂等、并发状态冲突、投影延迟/失败和索引 generation 指标及结构化日志，并通过采集测试验证只记录固定字段和 Memory/Workflow ID
- [x] 8.3 运行 Memory 领域与 Repository 的单元、MySQL 集成、并发和 race 测试，验证三层边界、原子替代、租户隔离和状态机在竞态下成立
- [x] 8.4 运行 RAG Client、Outbox、Recall 和 Workflow 的故障恢复与端到端测试，验证 RAG 不可用时事实仍可写读、旧向量会被 MySQL 过滤、HITL 可跨重启恢复
- [x] 8.5 运行全仓格式化、静态检查和测试，验证现有 Note、Chat、SSE、Grounding、Workflow API 与行为兼容且 Agent 未新增对 Milvus 的直接依赖
