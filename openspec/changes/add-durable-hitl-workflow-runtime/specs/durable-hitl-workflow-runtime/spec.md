## Purpose

为暂停时间超过单次进程生命周期的 Human-in-the-loop 工作流提供耐久状态、原子恢复权、版本兼容和可查询审计事实，使服务重启及多实例并发下仍能安全继续执行。

## ADDED Requirements

### Requirement: 耐久 Workflow Run
系统 SHALL 在执行节点前建立稳定的 Workflow Run 记录，并在每次可观察的暂停或终态结果后保存可恢复状态。

#### Scenario: 创建可恢复运行
- **WHEN** 调用方以稳定幂等键启动新的 Workflow Run
- **THEN** 系统 SHALL 在调用首个节点前持久化运行身份、定义版本、所有者范围、预算和初始状态，并为相同范围和幂等键返回同一个 Workflow Run

#### Scenario: 保存暂停检查点
- **WHEN** 节点产生有效人工等待点并使 Workflow Run 进入 `suspended`
- **THEN** 系统 SHALL 原子保存 suspended 状态、业务状态检查点、当前等待点和本次已提交节点事件

#### Scenario: 保存终态检查点
- **WHEN** Workflow Run 完成、失败、取消或过期
- **THEN** 系统 SHALL 保存终态、最终状态版本和本次已提交节点事件，且后续执行请求不得重新启动节点

### Requirement: 版本化强类型状态恢复
系统 MUST 使用显式 schema 标识和版本序列化业务状态，并在恢复执行前验证 Workflow 定义与业务状态兼容性。

#### Scenario: 兼容状态恢复
- **WHEN** 已注册 codec 支持检查点中的业务 schema、schema 版本和 Workflow 定义版本
- **THEN** 系统 SHALL 解码为声明的业务类型，并保持运行元数据、控制状态、预算和业务数据的原有语义

#### Scenario: 不兼容状态拒绝
- **WHEN** codec 缺失、schema 版本未知、定义版本不匹配或检查点内容无效
- **THEN** 系统 SHALL 返回稳定的状态兼容错误且不得取得执行权、调用节点或覆盖原检查点

#### Scenario: 状态安全边界
- **WHEN** Workflow State 被序列化到耐久存储
- **THEN** 持久化信封 SHALL 只包含恢复所需的允许列表字段，业务 codec MUST 拒绝认证凭证、Cookie、模型密钥和未经筛选的原始用户输入

### Requirement: 原子等待点处理权
系统 SHALL 通过等待点版本、Workflow 状态版本和有期限的唯一处理权，保证同一个等待点在任一时刻最多只有一个恢复请求能够启动节点。

#### Scenario: 取得恢复处理权
- **WHEN** 恢复请求匹配 pending 等待点的 Run ID、Wait ID、版本、内容哈希、允许动作和所有者范围，且等待点未过期
- **THEN** 系统 SHALL 原子取得带期限的处理权，并仅允许持有该处理权的请求恢复 Workflow Run

#### Scenario: 并发恢复冲突
- **WHEN** 两个请求并发处理同一个等待点
- **THEN** 最多一个请求 SHALL 取得处理权，其他请求 SHALL 返回稳定冲突结果且不得调用暂停节点或后续节点

#### Scenario: 陈旧或越权恢复
- **WHEN** 请求的状态版本、Wait 版本、内容哈希或所有者范围不匹配当前记录
- **THEN** 系统 SHALL 拒绝请求并保持现有 Workflow、Wait 和事件记录不变

#### Scenario: 重复已解决动作
- **WHEN** 已成功提交的等待点再次收到相同或不同动作
- **THEN** 系统 SHALL 返回确定性的已解决或冲突结果，且不得重复执行节点副作用

### Requirement: 故障后的处理权回收
系统 SHALL 允许未完成提交的执行处理权在租约到期后被安全回收，同时保持已提交结果不可重复处理。

#### Scenario: 服务在恢复执行前退出
- **WHEN** 实例取得等待点处理权后在调用节点前退出且租约到期
- **THEN** 后续实例 SHALL 能够重新取得该等待点的处理权并从最后一个已提交 suspended 检查点恢复

#### Scenario: 服务在节点执行期间退出
- **WHEN** 实例在节点开始后、提交新检查点前退出且租约到期
- **THEN** 后续实例 SHALL 从最后一个已提交检查点重新执行该节点，并使用相同的 Workflow Run 和节点幂等身份

#### Scenario: 旧处理者延迟提交
- **WHEN** 已失去租约的实例尝试提交执行结果
- **THEN** 系统 SHALL 因处理权或状态版本不匹配拒绝提交，不得覆盖新处理者保存的状态和事件

### Requirement: 一致的耐久节点事件
系统 SHALL 将已提交检查点对应的节点事件作为不可变审计事实持久化，并保持 Workflow Run 内序号唯一且严格递增。

#### Scenario: 原子提交状态和事件
- **WHEN** 一次节点执行结果被接受并形成新检查点
- **THEN** 系统 SHALL 在同一一致性边界内提交 Workflow 状态、Wait 变化和对应节点事件，不得出现已提交状态缺少对应事件的结果

#### Scenario: 事务提交失败
- **WHEN** 保存 Workflow、Wait 或任一事件失败
- **THEN** 系统 SHALL 回滚本次持久化变更，并保留此前最后一个完整检查点供后续重试

#### Scenario: 事件重复提交
- **WHEN** 相同 Workflow Run 和事件序号被再次提交
- **THEN** 系统 SHALL 拒绝不一致的重复事件，并不得覆盖既有审计事实

### Requirement: 独立于聊天链路的运行时
耐久 Workflow Runtime SHALL 与现有 Chat Run 生命周期和传输协议保持解耦。

#### Scenario: 持久化能力独立启用
- **WHEN** Workflow 持久化表、领域端口和 MySQL adapter 被部署
- **THEN** 现有聊天 Router、Agent Runner、`agent_runs`、SSE 和前端行为 SHALL 保持不变，且没有业务 Workflow 时不得产生新的生产执行路径

#### Scenario: 来源关联不改变聊天状态
- **WHEN** Workflow Run 通过来源引用关联现有 Chat Run
- **THEN** Workflow Run 的长期 `suspended` 状态 SHALL 不占用聊天会话 active guard，也不得要求 Chat Run 保持运行
