## Context

See `proposal.md` for motivation and `specs/durable-hitl-workflow-runtime/spec.md` for behavior requirements. 当前 `internal/workflow` 提供泛型 `WorkflowState[T]`、顺序 Runner、WaitPoint、ResumeCommand 和同步 Observer，但不保存任何状态；调用方必须保管 suspended state 才能调用 `Resume`。项目已有基于 `database/sql` 的 MySQL store、嵌入式顺序迁移、事务写入、条件状态更新和 Outbox 先例，但 Workflow Core 目前没有生产调用点。

节点执行可能调用模型、工具或外部业务系统，不能长期置于数据库事务中。因而本设计只能对数据库中的 Workflow、Wait 和 Event 提供原子提交，并通过租约与幂等身份实现 at-least-once 的故障恢复；外部副作用的 exactly-once 不能由数据库 checkpoint 单独保证。

## Goals / Non-Goals

**Goals:**

- 在不让泛型 Runner 依赖 MySQL 的前提下建立耐久运行协调层和存储端口。
- 让 pending 或 suspended Workflow 在进程重启后能够从最后一个完整检查点继续。
- 用原子 claim、租约、StateVersion 和 Wait 版本防止同一时刻的并发执行及旧处理者覆盖新状态。
- 在一个数据库事务中提交 Workflow checkpoint、Wait 变化和 NodeEvent，形成一致的审计事实。
- 为业务状态提供显式、受控、可演进的 codec，而不是直接持久化任意 Go 值。

**Non-Goals:**

- 不承诺节点外部副作用 exactly-once，不为任意节点实现分布式事务。
- 不实现审批人权限判定、待办列表 HTTP API、前端交互或聊天 SSE 映射。
- 不引入 Eino Graph、DAG、循环、并行、自动节点重试或通用任务队列。
- 不迁移 `note_drafts`、routing executor 或现有 Agent Runner。
- 不提供跨 Workflow 查询后台；本阶段只定义未来 API 可安全使用的所有者范围和存储查询键。

## Decisions

### 1. 领域端口与 MySQL adapter 分离

在 `internal/workflow` 增加耐久协调所需的非 SQL 端口和记录类型，在 `internal/platform/mysqlstore` 实现 MySQL adapter。Runner 继续只关心强类型状态转换；协调器组合 Runner、codec 和 store。

存储端口按一致性用例组织，而不是暴露三个可任意组合的 CRUD Repository。核心操作至少包括：创建幂等 Run、读取 checkpoint、claim 初始执行、claim Wait 恢复、带 claim 条件提交执行结果、释放或回收过期 claim。`CommitExecution` 在一次事务中更新 Workflow、创建或解决 Wait 并插入事件。

相比让 Runner 自己调用数据库，这保持单元测试简单并允许未来增加其他 adapter。相比把 Workflow、Wait、Event 拆成独立 Save 调用，用例级端口能把必须原子的写入固化为接口契约。

### 2. 使用版本化持久化信封而不是泛型 Repository

协调层仍以 `WorkflowState[T]` 执行业务节点，但存储边界使用非泛型 `StoredWorkflow` 和字节或 JSON 信封。每个业务 Workflow 注册 `StateCodec[T]`，至少暴露稳定 `SchemaID`、`SchemaVersion`、Encode 和 Decode；信封同时保存 Workflow 定义版本。

控制状态、预算、来源和小型脱敏业务 Data 构成 checkpoint。认证凭证只通过 `context.Context` 或受控端口传入，禁止进入 codec。Decode 在 claim 前完成兼容性检查；未知 schema 或定义版本直接拒绝，避免取得租约后才发现无法执行。

相比直接 `json.Marshal(WorkflowState[T])`，显式 codec 能约束 schema 演进、安全检查和测试。相比只保存业务对象 ID，完整的小型 checkpoint 更容易恢复；大型内容仍通过稳定 ID、版本、哈希或 PayloadRef 引用业务存储。

### 3. 三张独立表保存当前事实和审计历史

新增顺序迁移，建立：

- `workflow_runs`：Run/定义/来源/所有者身份、状态、StateVersion、checkpoint schema 与内容、预算计数、事件序号、执行 claim、租约和时间戳。
- `workflow_waits`：Wait 身份、Run/Node、版本、内容哈希、允许动作、PayloadRef、状态、claim、租约、解决动作与解决者引用。
- `workflow_node_events`：以 `(run_id, sequence)` 为主键保存固定字段 NodeEvent，不提供任意事件 payload。

所有者范围至少包含稳定的 `tenant_id` 和 `owner_id`，由未来认证后的 Service 提供；Store 的读取和 claim 操作必须带范围条件。来源引用只用于关联 Chat Run 或业务对象，不替代授权范围。

不复用 `agent_runs` 和 `agent_run_events`，因为 Chat Run 会被服务启动恢复逻辑标记 `interrupted`，生命周期、active guard、事件契约和保留策略都不同。当前事件只用于耐久审计，不增加投递 Outbox；未来需要向消息系统发布时再从已提交事件生成 Outbox。

### 4. 执行使用短事务 claim 和有期限租约

不在节点执行期间持有数据库事务或行锁。执行协议为：

1. 在短事务中按所有者、状态、StateVersion 和 Wait 版本原子写入随机 claim token 与 `lease_until`。
2. 读取并解码最后一个已提交 checkpoint，在事务外调用 `Runner.Run` 或 `Runner.Resume`，用内存 Collector 暂存本次事件。
3. 在短事务中用 claim token、原 StateVersion 和未过期租约作为条件，提交新 checkpoint、Wait 变化和全部事件。
4. 提交成功后 claim 清除；提交失败则数据库保持上一个完整 checkpoint。租约到期后其他实例可以重新 claim。

初始 Start 先以 `(tenant_id, owner_id, workflow_id, idempotency_key)` 唯一键创建 pending Run，再 claim 执行，避免请求在持久化身份前启动首个节点。Resume 先验证 codec/定义可用，再以 pending Wait 和 suspended Run 的版本条件共同 claim。

租约不是心跳任务队列。本阶段使用明确的 `LeaseDuration` 配置并允许协调器在长节点执行时续租；续租也必须匹配 claim token。旧处理者无论节点是否已返回，都无法以过期 token 覆盖新检查点。

### 5. Wait 状态机与 Workflow 状态机分离

Wait 使用 `pending -> processing -> resolved|expired|cancelled`。`processing` 表示某实例暂时持有恢复权，不表示人工决定已最终提交。claim 租约过期时可从 `processing` 再次取得处理权；只有 Workflow 新检查点和事件成功提交时，Wait 才转为 `resolved`。

ResumeCommand 保持领域校验信封；协调入口另外接收已认证的 `ActorRef` 和 `WorkflowOwner`。Runtime 不负责判断角色是否有审批权限，但 Store 必须按 owner scope 查询并在 resolved 记录中保存 actor reference，供未来审批 Service 和审计使用。

如果恢复节点再次 suspend，则旧 Wait 在同一事务中 resolved，并创建新的 pending Wait；新 Wait 必须有不同 Wait ID，允许同一节点多轮人工往返。

### 6. 事件先缓冲，再与 checkpoint 一致提交

耐久协调器为每次 Runner 调用注入批量 Collector，而不是直接让 Runner 的同步 Observer 逐条写数据库。Runner 仍负责生成事件序号和固定字段；协调器在 `CommitExecution` 中校验事件连续、Run ID 一致且首序号紧接已提交 EventSequence。

这避免 `node.started` 已落库但 checkpoint 未提交的半状态，并满足状态、Wait、事件的数据库原子性。代价是节点执行期间事件不可实时查询；本阶段没有实时 UI，这一取舍可接受。未来需要实时进度时，需单独设计 attempt 日志与最终审计事件的关系，不能直接破坏当前原子提交语义。

### 7. 故障恢复语义是 at-least-once，副作用必须幂等

实例可能在节点产生外部副作用后、提交 checkpoint 前退出；租约回收后该节点会再次执行。因此协调器向节点提供稳定执行身份，至少由 Workflow Run ID、Node ID 和 CurrentAttempt 派生，恢复同一个未提交 attempt 时身份保持不变。

写数据库、发送通知、调用外部 API 等节点必须使用该身份作为幂等键，或把业务写入与 Workflow commit 接入同一业务事务/Outbox。没有幂等能力的节点不得放在可恢复边界内。相比宣称 exactly-once，这一约束诚实反映跨系统故障模型。

### 8. 不接入生产链路但提供可验证装配

本 change 提供内存 fake store 的协调器契约测试和 MySQL adapter 集成测试，不在 server composition root 注册业务 Workflow，也不新增路由。仓库搜索继续确认聊天、SSE、Agent Runner 和 `note_drafts` 无新依赖。

后续试点 change 选择一个业务状态类型、codec、节点集合和审批 Service，再显式装配 durable runtime。Eino adapter 若被采用，也应复用相同的 Run/Wait/事件领域契约，而不是建立第二套审批身份。

## Risks / Trade-offs

- [风险] 节点副作用发生后进程崩溃会导致重复执行 → [缓解] 稳定执行幂等键、业务条件写入或 Outbox，并在首个试点中只接入可幂等节点。
- [风险] 固定租约过短会让长节点被并发重领，过长会延迟故障恢复 → [缓解] 配置有界租约并支持 token 条件续租，测试过期、续租和旧处理者提交竞争。
- [风险] checkpoint JSON 随业务结构演进而不可解码 → [缓解] 显式 SchemaID/SchemaVersion、定义版本检查和 codec 兼容测试；未知版本只读失败，不覆盖原数据。
- [风险] 业务 Data 可能包含敏感内容或过大对象 → [缓解] codec 允许列表、安全回归和大小上限；大对象使用版本化引用与内容哈希。
- [风险] 批量提交事件使运行中的节点不可实时观察 → [缓解] 本阶段只承诺已提交审计；实时 attempt 遥测留给独立能力。
- [风险] Workflow 与 Wait 分表可能出现不一致 → [缓解] 只允许通过事务性用例端口改变二者，数据库约束与集成测试覆盖回滚和并发。

## Migration Plan

1. 增加领域持久化记录、codec、owner/actor、claim 和稳定错误类型，并用内存 fake store 验证协调协议。
2. 增加新的幂等 MySQL 迁移和 adapter；部署迁移只创建独立表，不回填或改写现有 Chat Run 数据。
3. 运行 MySQL 集成测试验证创建、加载、claim、续租、提交、回滚、版本冲突和事件唯一性。
4. 增加未接生产路由的装配测试与全仓回归；部署后新表为空，现有行为不变。
5. 回滚应用代码时保留新表以避免删除未来可能写入的 Workflow 数据；需要完全回滚时先确认无记录，再由独立运维迁移删除表。

后续业务试点必须以新的 change 显式注册 codec、Workflow 定义和审批入口，不通过本次迁移自动启用。
