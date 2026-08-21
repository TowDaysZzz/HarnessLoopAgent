## 1. 强类型领域模型

- [x] 1.1 在 `internal/workflow` 定义 `WorkflowID`、`WorkflowRunID`、`NodeID`、`WaitID`、定义版本和 `SourceRef`，并用单元测试验证空值和无效来源引用被拒绝
- [x] 1.2 实现泛型 `WorkflowState[T]`、运行元数据、控制状态和业务数据分层，使用编译期测试节点验证相邻节点无需 `map[string]any` 或类型断言即可读取同一业务类型
- [x] 1.3 定义 `pending/running/suspended/completed/failed/cancelled/expired` 状态和集中转换校验，使用表驱动测试覆盖所有允许与拒绝的状态转换及 `StateVersion` 递增
- [x] 1.4 定义稳定领域错误及错误码，验证非法状态、无效契约、步骤耗尽、恢复耗尽、超时和取消能够被调用方分别识别

## 2. 等待点与恢复信封

- [x] 2.1 定义审批、审核、编辑和补充输入等待类型，以及 approve/reject/submit_edit/cancel 动作，使用表驱动测试验证未知类型、空动作和重复允许动作被拒绝
- [x] 2.2 实现带 Wait ID、Run ID、Node ID、版本、内容哈希、允许动作、PayloadRef 和过期时间的 WaitRequest/WaitPoint 校验，验证缺失字段、空动作集和非未来过期时间无法产生等待点
- [x] 2.3 定义恢复信封并实现等待点匹配校验，测试有效恢复、错误 Run/Wait ID、旧版本、不同内容哈希、不允许动作和过期等待点

## 3. 节点事件与审计出口

- [x] 3.1 定义固定字段的 `NodeEvent` 及 `started/completed/suspended/resumed/failed/skipped` 类型，使用 JSON 或反射测试验证不存在任意事件 map、认证凭证或原始业务输入字段
- [x] 3.2 实现同步 Observer、No-op Observer 和线程安全内存 Collector，验证事件保持 Workflow Run 内严格递增的序号和原始产生顺序
- [x] 3.3 实现 Runner 统一事件构造和稳定错误码映射，验证成功、失败与上下文终止分别产生 `started -> completed` 和 `started -> failed`，且失败后不产生 completed

## 4. 泛型顺序 Runner

- [x] 4.1 定义泛型 Node、执行输入、NodeResult、`continue/suspend` Directive 和 RunResult，使用编译期 fake node 验证输入输出保持同一业务状态类型
- [x] 4.2 实现 Runner 构造校验和顺序执行，验证空节点列表、nil 节点、空 Node ID、重复 Node ID 被拒绝，并验证多个节点只按声明顺序执行一次
- [x] 4.3 实现 continue 路径和完成节点记录，验证每个成功节点更新步骤计数、当前节点、已完成节点、状态版本和最终 completed 状态
- [x] 4.4 实现错误、context cancellation 和 deadline 路径，验证不再启动后续节点并分别返回稳定失败、取消或超时结果
- [x] 4.5 传播 Observer 错误并记录其限制，验证 started 事件发送失败时节点不会执行，终态事件发送失败时调用方不会收到虚假的成功审计结果

## 5. HITL-ready 暂停与恢复

- [x] 5.1 实现 suspend 路径，验证有效 WaitRequest 产生 `node.suspended`、Run 进入 suspended、后续节点不执行且结果不携带普通执行错误
- [x] 5.2 验证 suspend 缺少或携带无效 WaitRequest 时 Run 以契约错误失败，且不生成可恢复等待点
- [x] 5.3 实现同一 Workflow Run 的 Resume 入口，验证事件顺序为 `node.resumed -> node.started -> completed|suspended|failed`，恢复计数增加并从原暂停节点继续
- [x] 5.4 实现陈旧、过期、不匹配和重复恢复防护，验证拒绝时保留原 suspended 状态、WaitPoint 和状态版本，且不会调用节点或后续副作用
- [x] 5.5 实现 MaxSteps、MaxResumes 和 Deadline 预算检查，验证预算在节点调用或接受恢复之前生效，恢复后重新执行暂停节点会再次消耗一步

## 6. 兼容与完整验证

- [x] 6.1 替换旧 `map[string]any` Step/Runner 和测试并移除未生效的 `EnableParallel`，使用 `rg` 确认生产包没有遗留旧 Workflow API 调用或新增聊天链路依赖
- [x] 6.2 增加状态和事件安全回归测试，验证 Access Token、Cookie、密码、模型密钥及未经筛选的原始输入没有出现在可序列化运行元数据、控制状态或 NodeEvent 中
- [x] 6.3 运行 `go test ./...`、`go test -race ./internal/workflow` 和 `go vet ./...`，确认强类型、事件、预算、暂停/恢复及现有聊天/路由测试全部通过
- [x] 6.4 运行 `openspec validate add-hitl-ready-workflow-core --strict`，确认 proposal、spec、design 和 tasks 一致且变更可进入 apply 阶段
