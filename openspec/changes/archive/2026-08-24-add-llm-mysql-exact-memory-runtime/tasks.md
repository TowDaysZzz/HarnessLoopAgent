## 1. 结构化契约与配置边界

- [x] 1.1 在 Memory 领域定义版本化 `StructuredRecallPlan`、selector、clarification 和匹配来源类型，加入字段白名单、枚举、数量、长度、置信度及无稳定 selector 校验，并用单元测试验证 owner/SQL/状态/任意 Memory ID 和未知字段均被拒绝
- [x] 1.2 补齐 Memory Draft 与 Recall Plan 的共享 namespace、slot、EntityRef、scope、layer、kind 规范化规则，并用表驱动测试覆盖合法别名、空字段、超限和多实体歧义
- [x] 1.3 扩展 Memory 配置以明确 exact-only 模式、结构化计划阈值和候选边界，保持所有新开关默认关闭，并用配置测试验证 `MEMORY_ENABLED=true`、`RAG_ENABLED=false`、`PROJECTION_ENABLED=false`、`WORKFLOW_PILOT_ENABLED=true` 可合法加载

## 2. MySQL 精确查询与召回

- [x] 2.1 扩展 `ExactQuery`、Repository fake 和 MySQL `FindExact`，支持 layer/kind、active/expiry、多个有界 selector 与稳定排序，同时保证每个查询强制 tenant、user、scope 和 limit；用 Repository 测试验证无 selector 不执行全量扫描且跨 owner 返回空
- [x] 2.2 评估现有 owner/scope、entity、content hash 和 active status 索引的查询计划，仅在必要时增加兼容迁移，并通过 MySQL 集成测试或 `EXPLAIN` 证明事实槽和实体查询使用有界索引路径
- [x] 2.3 将 Recall Service 重构为显式 exact-only 与 exact-plus-semantic 模式，使 searcher 在 exact-only 下可为空且不进入向量循环；用单元测试验证 pinned、EntityRef、namespace/slot、hash 去重、obsolete/expiry 过滤和空结果行为
- [x] 2.4 实现 pinned > entity > slot > hash > authority > salience > recency > ID 的确定性排序及 Prompt 字符预算，并用测试验证结果稳定、截断计数正确且 Memory 始终封装为 `UNTRUSTED_MEMORY`
- [x] 2.5 增加 Recall Plan 到有界 ExactQuery 的执行编排和澄清结果，验证低置信度、多实体或无稳定 selector 时不查询全量 Memory、不伪造候选

## 3. 生产 LLM 适配器

- [x] 3.1 基于现有模型 Runner 实现严格 JSON 的 DraftExtractor，执行响应大小限制、未知字段拒绝、规范化、内容哈希重算和有界 repair，并用 fake Runner 测试合法提取、非 JSON、超限、敏感内容及模型伪造 owner
- [x] 3.2 实现严格 JSON 的 StructuredQueryPlanner，将消费查询转换为受限 Recall Plan，并用中文偏好、用户资料、目标、task/reminder EntityRef、低置信度和 prompt injection 语料测试输出边界
- [x] 3.3 将 ConflictResolver 改为显式接收 owner-scoped 候选，限制模型只能引用允许 Memory ID 和六类关系，再交给确定性 Policy 决策；用测试验证未知 ID、重复提议、低权威冲突和用户 correction
- [x] 3.4 实现 owner-scoped EditLoader 及有界编辑 payload 持久化/读取，确保 payload 不含凭证且不能被其他 owner 加载，并用单元测试验证越权、过期和重复读取行为

## 4. Memory Capture Workflow 重排

- [x] 4.1 新增 ExactCandidateLookup 节点并将定义顺序调整为 Extract → ExactCandidateLookup → Conflict → Review → Commit，更新 `CaptureData`/codec 只保存有界候选 MemoryRef、匹配来源和统计；用节点顺序及 checkpoint 大小测试验证
- [x] 4.2 在 Conflict 前 owner-scoped 重载并验证候选 active/version/hash，使 Resolver 只能处理固定候选集；用测试验证 Workflow 暂停期间候选被替代时显式失败或重新召回
- [x] 4.3 修正 Review 编辑路径，使新 Draft 重新执行精确候选加载和冲突策略后创建新 candidate，旧审核正文不可原地修改；用测试覆盖 approve、reject、submit_edit 和陈旧 row_version
- [x] 4.4 保持 Execution ID + mutation index 的稳定幂等身份，扩充 DurableRuntime 重启、checkpoint 提交失败重放、并发 resume 和跨 owner resume 测试

## 5. MySQL-only Projection 语义

- [x] 5.1 让 MySQL Memory Store 从运行配置获得 Projection Version，移除 Outbox ID 和 `model_version` 中硬编码的 `default`，并用事务测试验证 active 写入、candidate 激活和幂等重放生成唯一正确版本的 Outbox
- [x] 5.2 明确 Projection 关闭时不 Claim、不重试且不影响 readiness，保留 pending 作为未来回填日志；用测试验证 MySQL-only 提交成功、无 RAG 调用且 backlog 可观测
- [x] 5.3 保持未来启用 Projector 时重新检查 active 和 content hash、跳过 obsolete 的兼容行为，并用现有 Projector 测试补充历史 pending、superseded 与版本匹配场景

## 6. Memory 应用服务与 HTTP 控制面

- [x] 6.1 新增封装 typed DurableRuntime 的 Memory Capture 应用服务，提供 start、get state、get review 和 resume，并用服务测试验证稳定 idempotency key、状态映射和有界响应 DTO
- [x] 6.2 增加认证后的 `POST /v1/memory-captures`、状态、review 和 resume 路由，从 Principal 构造 WorkflowOwner/Actor 且忽略请求 owner 字段；用 HTTP 测试验证 approve/reject/edit、非法 action、陈旧 wait 和未认证请求
- [x] 6.3 将跨 tenant/user 的 run、wait、candidate 和 edit payload 访问统一映射为 not found 或等价拒绝，并用隔离测试验证不泄露资源是否存在
- [x] 6.4 将显式“记住/修改偏好”作为默认关闭的 Chat Intent Pilot，只启动独立 Capture Run 而不占用 Chat/SSE active guard；用路由测试验证普通聊天、工具结果和节点完成事件不会隐式写入 Memory

## 7. 生产启动装配与可观测性

- [x] 7.1 在服务启动入口按配置装配 MySQL Memory Repository、exact-only Recall、LLM 适配器、Memory Capture DurableRuntime 和 HTTP option，确保 Memory 关闭时不改变现有依赖图；用装配测试覆盖开关矩阵
- [x] 7.2 在 Memory/Pilot 开启但模型结构化适配器或数据库不可用时返回脱敏启动错误，在 RAG 明确关闭时跳过 RAG Client、Projector 和相关 readiness；用启动测试验证两类行为
- [x] 7.3 扩展 Memory Telemetry，区分 exact-only、功能关闭、selector 命中、无 selector、澄清、过滤、截断及 Capture 生命周期，并用指标测试确认不记录正文、凭证或结构化敏感值
- [x] 7.4 将 Memory Capture Workflow 作为首个生产与消费闭环：第一次显式写入产生 active Memory，第二次同 slot/entity 修正精确召回旧版本并完成 supersede；用应用层集成测试验证仅新版本进入后续上下文

## 8. 端到端验证与交付

- [x] 8.1 增加真实 MySQL 的 MySQL-only E2E，覆盖 start → candidate → restart → approve → exact recall、duplicate noop、correction supersede、用户编辑重新冲突和 Outbox pending，并确保测试在无 RAG 环境通过
- [x] 8.2 增加安全与故障 E2E，覆盖跨 owner、prompt injection、LLM 非法 JSON、候选超限、并发修改、陈旧审核和 Memory 开关回滚，验证失败不产生部分事实或敏感日志
- [x] 8.3 运行 `go test ./...`、相关 race/集成测试和 OpenSpec strict validation，修复所有回归并记录环境门控测试的执行方式与结果
- [x] 8.4 更新配置示例、API 文档和实现状态，说明 exact-only 能力边界、显式 Pilot、Outbox deferred 语义、回滚步骤及未来启用 RAG 的迁移路径，并核对文档与实际默认值一致
