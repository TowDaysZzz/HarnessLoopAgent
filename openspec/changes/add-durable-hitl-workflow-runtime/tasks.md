## 1. 耐久领域契约

- [x] 1.1 在 `internal/workflow` 定义 `WorkflowOwner`、`ActorRef`、幂等启动请求、claim token/租约和持久化状态记录，并用表驱动测试验证空身份、越界租约和无效状态被拒绝
- [x] 1.2 定义稳定的持久化错误码，区分 not found、idempotency conflict、claim conflict、lease lost、state conflict、codec incompatibility 和 checkpoint too large，并验证调用方可用 `errors.Is` 或错误码可靠识别
- [x] 1.3 定义 Wait 的 `pending/processing/resolved/expired/cancelled` 状态及合法转换，验证 processing 只表示临时 claim、终态不可回退且版本变化可观测
- [x] 1.4 为节点输入增加稳定执行幂等身份，验证同一个未提交 attempt 被租约回收后身份保持一致，而已提交后的新 attempt 使用不同身份

## 2. 状态 Codec 与持久化信封

- [x] 2.1 定义泛型 `StateCodec[T]` 与非泛型 checkpoint 信封，包含 Schema ID、Schema Version 和 Definition Version，并用 round-trip 测试验证 `WorkflowState[T]` 的 Meta、Control、Budget 和 Data 语义保持一致
- [x] 2.2 实现 codec 注册与兼容性前置检查，验证未知 schema、未知版本、定义版本不匹配和损坏内容在 claim/节点调用前返回稳定错误
- [x] 2.3 增加 checkpoint 大小上限和安全回归测试，验证凭证、Cookie、模型密钥及未经筛选的原始输入无法通过测试 codec 写入，超限内容不会进入 Store
- [x] 2.4 验证大型业务内容可以仅以 ID、版本、内容哈希或 PayloadRef 放入 checkpoint，且不要求通用 codec 深拷贝或序列化外部对象

## 3. 事务性 Store 端口与协调器

- [x] 3.1 定义用例级 DurableStore 端口，覆盖幂等创建、读取、初始执行 claim、Wait 恢复 claim、续租和条件提交，并用编译期 fake 确认接口不暴露 SQL 或可绕过事务的独立 Save 组合
- [x] 3.2 实现内存 fake Store 的版本、所有者、claim 和租约语义，使用可控时钟验证相同幂等键返回同一 Run，不同输入复用幂等键返回冲突
- [x] 3.3 实现泛型耐久协调器 Start 路径，验证 Run 在首个节点前已持久化、成功/暂停/失败结果提交完整 checkpoint 与事件，重复 Start 不重复调用节点
- [x] 3.4 实现耐久协调器 Resume 路径，验证兼容性和 ResumeCommand 校验在节点前完成、有效请求从持久化 suspended checkpoint 恢复同一节点
- [x] 3.5 实现批量事件 Collector 与 commit 校验，验证事件 Run ID 一致、序号连续且紧接已提交序号，缺失、乱序或重复批次被拒绝
- [x] 3.6 实现恢复后再次 suspend 的提交语义，验证旧 Wait 原子 resolved、新 Wait 原子 pending 且拥有不同 Wait ID，Workflow 仍可继续下一轮恢复
- [x] 3.7 实现 claim 续租与提交失败处理，验证只有当前 token 能续租或提交，Store 错误保留上一个完整 checkpoint 且不向调用方报告虚假成功

## 4. MySQL Schema

- [x] 4.1 增加顺序迁移创建 `workflow_runs`，包含所有者、幂等键、定义/来源、状态/版本、checkpoint、预算/计数、claim/lease 和时间索引，并验证 `Migrate` 重复运行成功
- [x] 4.2 在同一迁移创建 `workflow_waits`，包含 Wait 契约、状态、claim/lease、解决动作与 actor，并用外键、唯一键和索引验证每个 Wait 与 Run 的关联及查询范围
- [x] 4.3 在同一迁移创建固定字段的 `workflow_node_events`，以 `(run_id, sequence)` 为主键且不含任意事件 payload，并验证级联关系和重复序号约束
- [x] 4.4 增加 schema 集成测试，确认迁移不修改 `agent_runs`、`agent_run_events`、chat active guard 或现有表数据

## 5. MySQL DurableStore Adapter

- [x] 5.1 实现按 `(tenant_id, owner_id, workflow_id, idempotency_key)` 原子创建或读取 Workflow Run，集成测试覆盖相同请求幂等返回和不同请求参数冲突
- [x] 5.2 实现初始执行 claim、Wait 恢复 claim 和续租的短事务条件更新，集成测试覆盖 owner、状态版本、Wait 版本、内容哈希、动作、过期时间和 token 条件
- [x] 5.3 实现 `CommitExecution` 事务，原子更新 checkpoint、清除 Run claim、创建/解决 Wait 并插入事件，使用故障注入测试验证任一步失败都完整回滚
- [x] 5.4 实现按 owner scope 加载 Run、当前 Wait 和有序事件，验证其他 tenant/owner 得到 not found 或拒绝结果且不会泄露记录存在性
- [x] 5.5 验证 MySQL adapter 对事件重复、不连续 StateVersion、过期租约和旧 claim token 返回稳定冲突，并保持已提交数据不变

## 6. 并发与故障恢复

- [x] 6.1 增加两个协调器实例并发 Resume 同一 Wait 的测试，验证最多一个实例调用节点，失败实例得到 claim conflict 且没有新事件
- [x] 6.2 增加 claim 后、节点前崩溃模拟，推进可控时钟至租约到期并验证第二实例可从原 suspended checkpoint 重领和完成
- [x] 6.3 增加节点执行后、commit 前崩溃模拟，验证第二实例以相同执行幂等身份重跑节点，旧实例的延迟提交因 lease/token/version 冲突被拒绝
- [x] 6.4 增加 terminal Run、resolved Wait、过期 Wait、陈旧 StateVersion 和重复动作测试，验证所有拒绝路径都不会启动节点或追加事件
- [x] 6.5 运行 `go test -race ./internal/workflow ./internal/platform/mysqlstore`，确认内存 fake、Collector 和并发协调测试无数据竞争

## 7. 兼容与完整验证

- [x] 7.1 使用仓库搜索确认 server composition、chat、routing、SSE、Eino Agent 和 `note_drafts` 未新增 Durable Runtime 生产调用，且没有把 Workflow suspended 映射为 Chat Run 状态
- [x] 7.2 运行 `go test ./...` 和 `go vet ./...`，确认持久化 runtime、MySQL adapter 及现有聊天、路由、Agent、RAG 和笔记测试全部通过
- [x] 7.3 运行 MySQL 集成测试，验证迁移、幂等创建、原子 claim、租约回收、事务回滚、owner scope 和事件唯一性全部通过
- [x] 7.4 运行 `openspec validate add-durable-hitl-workflow-runtime --strict`，确认 proposal、delta spec、design 和 tasks 一致且 change 可进入 apply 阶段
