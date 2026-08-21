## Purpose

为确定步骤的业务流程提供强类型、可审计、有预算且能够安全暂停并接受人工恢复输入的核心执行契约，同时保持现有聊天运行链路不变。

## ADDED Requirements

### Requirement: 强类型工作流状态
系统 SHALL 使用同一编译期业务数据类型在一次 Workflow Run 的所有节点之间传递状态，并将运行元数据、控制状态、预算状态和业务数据明确分离。

#### Scenario: 节点传递强类型状态
- **WHEN** 两个相邻节点依次更新同一个 Workflow Run 的业务状态
- **THEN** 后继节点 SHALL 以声明的业务类型读取前驱节点输出，且不需要通过字符串键或运行时类型断言访问业务字段

#### Scenario: 状态携带稳定运行身份
- **WHEN** Runner 创建或继续一次 Workflow Run
- **THEN** 状态 SHALL 包含工作流定义 ID、定义版本、Workflow Run ID、状态版本和可选来源引用

### Requirement: 受控节点执行结果
每个节点 SHALL 返回更新后的强类型状态和受控执行指令；等待人工输入 MUST 表示为暂停结果，而不是普通执行错误。

#### Scenario: 节点正常继续
- **WHEN** 节点成功返回继续指令
- **THEN** Runner SHALL 记录节点完成并按定义顺序执行下一个节点

#### Scenario: 节点请求人工处理
- **WHEN** 节点成功返回暂停指令和有效等待请求
- **THEN** Runner SHALL 将 Workflow Run 标记为 `suspended`、停止执行后续节点并返回可恢复结果，且不得把本次暂停报告为失败

#### Scenario: 暂停指令缺少等待请求
- **WHEN** 节点返回暂停指令但未提供有效等待请求
- **THEN** Runner SHALL 以稳定的契约错误码终止本次执行，且不得生成可恢复等待点

### Requirement: 可校验的人工等待点
系统 SHALL 为每次人工等待生成稳定且带版本的等待点，并限制允许的人工动作和有效期。

#### Scenario: 创建等待点
- **WHEN** Workflow Run 因审批、审核、编辑或补充输入而暂停
- **THEN** 等待点 SHALL 包含 Wait ID、Workflow Run ID、Node ID、等待类型、版本、内容哈希、允许动作和过期时间

#### Scenario: 接受有效恢复输入
- **WHEN** 恢复输入匹配当前 Workflow Run 的 Wait ID、版本、内容哈希和允许动作且等待点未过期
- **THEN** Runner SHALL 清除当前等待点、增加恢复计数，并从同一个 Workflow Run 的暂停节点继续执行

#### Scenario: 拒绝陈旧或不匹配的恢复输入
- **WHEN** 恢复输入引用错误的 Wait ID、旧版本、不同内容哈希、不允许的动作或已过期等待点
- **THEN** Runner SHALL 拒绝恢复且保持原等待状态，不得执行暂停点之后的节点

#### Scenario: 重复提交人工动作
- **WHEN** 同一个已接受的等待点和动作被重复提交
- **THEN** 系统 SHALL 返回确定性的重复或无效状态结果，且不得重复执行后续副作用节点

### Requirement: 节点生命周期事件
Runner SHALL 为节点执行生成单调有序、字段受控的生命周期事件，使调用方能够审计成功、失败、暂停和恢复路径。

#### Scenario: 成功节点事件顺序
- **WHEN** 节点成功完成
- **THEN** 该节点的事件顺序 SHALL 为 `node.started` 后跟 `node.completed`

#### Scenario: 失败节点事件顺序
- **WHEN** 节点返回错误或上下文在节点执行期间终止
- **THEN** 该节点的事件顺序 SHALL 为 `node.started` 后跟 `node.failed`，且不得再生成 `node.completed`

#### Scenario: 暂停和恢复事件顺序
- **WHEN** 节点暂停后收到有效恢复输入
- **THEN** 系统 SHALL 先生成 `node.suspended`，恢复时生成 `node.resumed`，并为恢复后的节点执行生成新的 `node.started` 及对应终态事件

#### Scenario: 事件序号单调递增
- **WHEN** 一次 Workflow Run 产生多个节点事件
- **THEN** 每个新事件的序号 SHALL 在该 Workflow Run 内严格大于前一个事件序号

### Requirement: 工作流预算
Runner MUST 在执行节点和接受人工恢复之前强制检查配置的步骤、恢复次数和运行时限预算。

#### Scenario: 步骤预算耗尽
- **WHEN** 执行下一个节点将超过最大步骤数
- **THEN** Runner SHALL 在调用该节点前终止执行并返回稳定的步骤预算错误码

#### Scenario: 恢复预算耗尽
- **WHEN** 接受新的人工恢复将超过最大恢复次数
- **THEN** Runner SHALL 拒绝恢复并保持等待状态

#### Scenario: 运行时限已过
- **WHEN** Workflow Run 的截止时间已到或上下文已取消
- **THEN** Runner SHALL 停止启动新节点并返回可区分的超时或取消结果

### Requirement: 状态与事件安全边界
可暂停状态和节点事件 MUST 使用允许列表字段，且不得包含认证凭证、Cookie、模型密钥或未经筛选的原始用户输入。

#### Scenario: 安全序列化状态
- **WHEN** Workflow State 被检查或准备交给未来 checkpoint adapter
- **THEN** 运行元数据和控制状态 SHALL 不提供 Access Token、Cookie、密码或模型密钥字段

#### Scenario: 安全节点事件
- **WHEN** Observer 接收节点事件
- **THEN** 事件 SHALL 只包含运行身份、节点身份、状态、序号、计数、耗时、Wait ID 和稳定错误码，不得包含任意 `map[string]any` 事件载荷

### Requirement: 现有运行链路兼容
本能力的引入 SHALL 不改变现有聊天 Run、路由、SSE、MySQL 和候选笔记确认行为。

#### Scenario: 工作流核心独立验证
- **WHEN** 强类型 Workflow 核心及其测试被加入项目
- **THEN** 现有聊天请求 SHALL 继续走当前 Router、Executor 和 Agent Runner 链路，且不要求新的数据库迁移或前端事件处理
