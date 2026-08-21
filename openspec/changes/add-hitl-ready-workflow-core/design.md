## Context

See `proposal.md` for motivation and `specs/hitl-workflow-execution/spec.md` for behavior requirements. 当前 `internal/workflow` 只有 `Step.Run(context.Context, map[string]any)` 和顺序 Runner，除单元测试外没有生产调用点；`EnableParallel` 字段也未参与执行。聊天侧已经有独立的持久化 Run 和 SSE 事件，但其状态机不支持长期等待，服务重启还会把活跃 Run 标为 `interrupted`。

现有 `note_drafts` 通过候选 ID、内容哈希、用户/租户/会话范围和过期时间实现了跨 Chat Run 确认，但并未暂停和恢复同一个工作流。Eino v0.9.9 已提供泛型 Compose、Local State、`StatefulInterrupt`、`ResumeWithData`、`CheckPointStore` 和最大图步骤，后续可以承担图调度和 checkpoint；本次不复制这些引擎能力。

## Goals / Non-Goals

**Goals:**

- 建立与 Eino、聊天传输层和持久化实现解耦的强类型工作流领域契约。
- 让顺序 Runner 能验证一次内存 Workflow Run 的成功、失败、暂停和恢复语义。
- 为节点事件、等待点和预算定义稳定身份、状态转换和安全字段。
- 保持状态和事件可被未来 checkpoint/audit adapter 安全序列化。

**Non-Goals:**

- 本次不提供进程重启后的恢复保证，不把“内存可恢复”描述为耐久 HITL。
- 本次不实现 DAG、条件分支、循环、并行、节点自动重试或自研 checkpoint 引擎。
- 本次不增加 `workflow_runs`、checkpoint 或 approval 表，不增加审批 HTTP API。
- 本次不改变 `chat.RunStatus`、单会话活跃 Run 约束、SSE 事件或前端交互。
- 本次不迁移 `note_drafts`、`routing.ComplexHandler` 或 Eino Runner。

## Decisions

### 1. Workflow Run 与 Chat Run 使用不同生命周期

定义独立的 `WorkflowID`、定义版本和 `WorkflowRunID`，并通过只包含类型和 ID 的 `SourceRef` 可选关联 Chat Run 或其他触发源。Workflow 可以长期 `suspended`，而触发它的 Chat Run 将来仍可正常完成；新的 Chat Run 或审批 API 可以恢复原 Workflow Run。

相比直接给 `chat.RunStatus` 增加 `waiting`，该方案不会让等待审批占用聊天会话的 active guard，也避免第一阶段修改 MySQL 和 SSE。代价是后续必须明确 Chat Run、Workflow Run 和 Wait ID 三者的查询关系。

### 2. 使用泛型状态信封承载业务数据

以 `WorkflowState[T]` 包装 `RunMetadata`、`ControlState`、`BudgetState` 和业务 `Data T`。控制状态记录当前节点、已完成节点、状态版本、步骤计数、恢复计数、事件序号和可选等待点；认证凭证继续通过 `context.Context` 或受控端口传递。

不使用通用属性包或 `map[string]any`，因为它会把类型错误推迟到运行时并污染 checkpoint。也不要求对泛型业务状态进行通用深拷贝；Runner 保持单线程顺序执行，节点负责返回更新后的状态，包含 slice/map 的业务类型需遵守不并发共享的约束。

### 3. 节点返回 Directive，而暂停不是 error

节点接口接收并返回相同业务类型的状态，结果包含 `continue` 或 `suspend` Directive。`suspend` 必须同时提供有效 `WaitRequest`；节点错误只表示执行失败。Runner 返回包含状态和运行状态的结果，使调用方可以区分 `completed`、`suspended`、`failed` 和上下文终止。

把暂停表示为专用 error 虽然更接近 Eino API，但会把领域语义绑定到执行引擎，并容易被通用错误处理误记为失败。未来 adapter 负责在领域 `suspend` 与 Eino `StatefulInterrupt` 之间转换。

### 4. 恢复同一个暂停节点并使用显式恢复信封

Runner 提供独立 Resume 入口，输入至少包含 Workflow Run ID、Wait ID、等待点版本、内容哈希、动作和可选 PayloadRef。Runner 先验证状态为 `suspended`、等待点匹配、动作被允许、未过期且恢复预算充足，再生成 `node.resumed`，清除等待点并重新调用当前节点。

恢复后的节点通过执行输入识别恢复动作；节点不得在产生等待点之前执行不可幂等副作用。第一阶段只验证内存状态上的恢复，同一个已接受 Wait ID 再次提交会因状态或等待点不匹配而被拒绝。未来持久化层仍需唯一约束或条件更新保证并发幂等。

### 5. 节点事件由 Runner 统一产生

定义固定字段的 `NodeEvent` 和返回 error 的同步 `Observer`。Runner 而不是节点负责计算序号、Attempt、ResumeCount、Duration 和终态事件，以保证事件配对。默认使用 No-op Observer，测试使用内存 Collector；Observer 错误向调用方传播，不静默声称审计成功。

事件顺序为：普通成功 `started -> completed`，失败 `started -> failed`，暂停 `started -> suspended`，恢复 `resumed -> started -> completed|suspended|failed`。`skipped` 类型为后续条件图预留，当前顺序 Runner 不主动产生。事件只携带允许列表元数据和稳定错误码，不携带业务状态、原始输入或任意 map。

同步 Observer 仍不能保证“业务副作用与审计事件原子提交”。在持久化阶段需要事务事件表或 Outbox；在此之前接入的副作用节点必须具备稳定幂等键。

### 6. 状态转换集中校验

为 Workflow Run 定义 `pending/running/suspended/completed/failed/cancelled/expired`，通过集中转换函数拒绝非法跳转。`suspended` 是不占用执行资源的非终态；`completed/failed/cancelled/expired` 为终态。每次有效转换增加 `StateVersion`，为未来乐观并发控制保留语义。

不复用 `chat.RunStatus` 或 `notedraft.Status`，因为三者的事实来源、终态和恢复规则不同。

### 7. 第一版预算保持确定且可测试

预算包括 MaxSteps、MaxResumes 和可选 Deadline；在调用节点或接受恢复前检查。步骤计数表示实际节点调用次数，因此恢复后重新调用暂停节点会再次消耗一步；恢复次数独立计数，避免把人工往返误当成失败重试。

模型调用、工具调用和 Token/成本预算仍由现有 `internal/runtime` 或未来 adapter 管理，本次不把两套计数强行合并。后续 Compose adapter 同时设置领域预算和 `WithMaxRunSteps`，以领域预算作为业务契约、引擎预算作为防御性上限。

### 8. 直接替换未使用的旧 Runner

删除或改写当前 `Step`、非泛型 `Runner` 和无效的 `EnableParallel`，同步替换唯一的 workflow 单测。仓库搜索和全量测试用于确认没有生产调用方，不增加 deprecated wrapper 或双轨入口。

这种方式会造成 Go 内部 API 不兼容，但当前零生产调用点使迁移成本最低；保留旧 API 反而会延长共享可变 map 的生命周期。

## Risks / Trade-offs

- [风险] 第一阶段只有内存暂停状态，进程退出后不能恢复 → [缓解] API 和文档明确标注 HITL-ready；耐久 checkpoint、等待请求 Repository 和恢复 API 作为后续独立 change。
- [风险] Resume 会重新调用暂停节点，暂停前副作用可能重复 → [缓解] 规定等待点必须先于不可幂等副作用，未来所有写节点使用幂等键或事务 Outbox。
- [风险] 泛型业务数据可能包含 map/slice，值传递仍会共享底层数据 → [缓解] 顺序执行且不并发暴露状态，测试节点遵守返回新状态约定，不宣称深度不可变。
- [风险] Observer 完成事件失败时节点可能已经产生副作用 → [缓解] 当前不接生产副作用工作流；持久化接入前设计事务审计或 Outbox。
- [风险] 领域中断模型与 Eino checkpoint 细节不完全一致 → [缓解] 保持领域 Wait ID 与引擎 Interrupt ID 分离，在 adapter 集成测试中验证映射和恢复事件顺序。
- [风险] 预留过多状态导致首版复杂 → [缓解] Runner 只实现顺序 continue/suspend；分支、并行、重试和 checkpoint 不进入本次代码。

## Migration Plan

1. 在 `internal/workflow` 增加强类型 ID、状态、等待点、预算、事件和错误码，不触碰外部包。
2. 用泛型节点和 Runner 替换旧 `map[string]any` 接口，删除无效并行开关。
3. 增加成功、失败、取消、预算、暂停、有效恢复、陈旧恢复、重复恢复和安全字段测试。
4. 运行 `go test ./...`、`go test -race ./internal/workflow` 和 `go vet ./...`，并用仓库搜索确认没有聊天/路由生产调用变化。
5. 回滚时只需恢复 `internal/workflow` 旧骨架及测试；本次没有数据库、配置或外部协议迁移。

后续 change 将依次增加 Eino Compose adapter、持久化 Workflow Run/checkpoint/wait request、审批 API，以及单个候选或洞察流程试点；验证稳定后再选择性映射到聊天 SSE。

## Open Questions

无。跨进程 checkpoint 存储格式、人工 Payload 的领域 schema 和首个生产试点属于后续 change，不影响本次核心契约和任务拆分。
