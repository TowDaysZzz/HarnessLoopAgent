# daily-review-generation Specification

## Purpose

让认证用户通过自然语言获得基于指定本地日期内跨会话活动、每日笔记和相关有效 Memory 的可追溯每日回顾，并在所有可见输入事实未变化时安全复用已生成结果，避免重复模型成本和不一致回答。

## Requirements

### Requirement: 自然语言日期解析确定且有界
系统 SHALL 将“回顾今天”“总结昨天”及明确日期等 Daily Review 请求解析为一个本地日期、受支持时区和半开 UTC 时间窗口，并固定到本次 Invocation。缺少日期时 MUST 使用用户当前本地日期；不支持的时区、不可确定的日期或超出允许范围的窗口 MUST 要求澄清。

#### Scenario: 回顾今天
- **WHEN** 用户在受支持时区中请求“回顾一下今天”
- **THEN** 系统使用请求接收时刻解析用户本地当天的 `[start,end)` UTC 窗口，并在整次执行和缓存判断中保持不变

#### Scenario: 日期表达歧义
- **WHEN** 用户给出的日期无法唯一解析或请求跨越超过配置上限的范围
- **THEN** 系统返回日期澄清提示，不读取活动数据、不生成回顾且不写入缓存

### Requirement: 每日活动按 owner 和时间窗口有界收集
系统 SHALL 在认证 owner 范围内收集目标窗口中的跨会话用户/助手消息和每日笔记，并以配置的记录数、字符数、会话数和查询时间预算限制结果。系统 MUST 排除 Daily Review 触发消息、该 Skill 生成的历史回复、失败或未提交的业务结果以及不可见数据，防止重复触发自身改变回顾输入。

#### Scenario: 收集多个会话和笔记
- **WHEN** 用户在目标日期内有多个会话消息和多条有权访问的笔记
- **THEN** 系统按稳定顺序返回有界活动证据，并为每项保留类型、稳定 ID、版本或序号、发生时间和内容哈希

#### Scenario: 重复触发消息不污染输入
- **WHEN** 用户在同一天再次发送 Daily Review 触发消息
- **THEN** 系统不把本次或历史 Daily Review 触发与生成消息计入每日活动内容或输入指纹

#### Scenario: 数据超过预算
- **WHEN** 目标窗口中的会话或笔记超过配置的数据预算
- **THEN** 系统按确定性规则截断或分层汇总，返回覆盖警告，并不得无界加载或把截断结果描述为完整事实

### Requirement: Memory 只提供相关且有效的用户上下文
系统 SHALL 在相同 owner 范围内通过现有 Memory Recall 契约获取与当日活动相关的目标、偏好、约束、摘要和结果，并过滤 rejected、superseded、revoked、expired、跨 scope 或版本不匹配的记录。Memory MUST 标记为不可信上下文，且不得在没有当日活动证据时被表述为当天发生的事件。

#### Scenario: 使用 Memory 关联目标进展
- **WHEN** 当日活动证据与用户有效目标或约束存在可验证关联
- **THEN** 系统可以在目标进展中引用固定 Memory ID、Lineage Version 和 Content Hash，并同时引用支持该进展的当日活动证据

#### Scenario: Memory 已失效
- **WHEN** 缓存判断或生成前发现相关 Memory 已被 supersede、撤销、拒绝或过期
- **THEN** 系统排除该 Memory、改变源数据指纹并不得返回依赖旧 Memory 的缓存结果

### Requirement: Daily Review 使用强类型 Workflow 生成和校验
Daily Review 组件 SHALL 在被独立、显式调用时通过版本化 Workflow 依次完成窗口解析、源数据快照、缓存判断、证据加载、Memory 召回、结构化生成、证据校验和渲染。回顾输出 MUST 至少包含窗口、重点、已完成、未完成、目标进展、反思问题、建议、证据引用及覆盖警告；每个事实性条目 MUST 引用允许的活动或 Memory 证据。Chat Runtime MUST 不自动匹配、启动或输出 Daily Review。

#### Scenario: 成功生成每日回顾
- **WHEN** 独立调用方提供合法日期和 owner 范围，目标日期存在足够活动证据且缓存未命中
- **THEN** Daily Review 组件在预算内生成并校验结构化结果，返回给调用方并写入允许的缓存，不要求存在 Chat Run

#### Scenario: Chat 中请求每日回顾
- **WHEN** 用户在 Chat 中发送“回顾今天”或等价消息
- **THEN** Chat Runtime 不匹配或启动 Daily Review Workflow，也不产生 Skill Invocation

#### Scenario: 生成包含无效引用
- **WHEN** 模型输出不存在、跨 owner、版本不匹配或未被本次证据集合允许的引用
- **THEN** 组件拒绝该事实项或整个生成结果并执行有界修复；修复预算耗尽后返回证据校验失败，不缓存无效结果

#### Scenario: 当日没有活动证据
- **WHEN** 目标窗口内没有可用会话或笔记证据
- **THEN** 组件不调用模型编造回顾，返回“暂无足够活动数据”的覆盖提示，并可以缓存该确定性空结果直到源数据发生变化

### Requirement: 缓存命中由完整源数据指纹决定
系统 SHALL 使用 owner、日期窗口、时区、规范化请求选项、Skill 定义版本、生成契约版本和源数据指纹作为缓存身份。源数据指纹 MUST 覆盖所有可见会话消息、笔记及相关 Memory 的稳定 ID、内容版本或序号、状态和内容哈希；仅 TTL 未过期不得作为返回缓存的充分条件。

#### Scenario: 输入无变化时重复触发
- **WHEN** 同一用户以等价选项重复请求同一日期回顾，且会话、笔记和相关 Memory 的完整指纹与成功缓存一致
- **THEN** 系统不执行证据正文加载后的模型生成节点，返回相同缓存结果，并记录不含正文的缓存命中事件

#### Scenario: 新增会话或笔记
- **WHEN** 缓存建立后目标日期新增一条可见的非 Skill 会话消息或每日笔记
- **THEN** 系统计算出不同源数据指纹、跳过旧缓存并基于新快照重新生成结果

#### Scenario: 笔记或 Memory 状态变化
- **WHEN** 已参与缓存指纹的笔记被修改或删除，或者 Memory 被更新、supersede、撤销、拒绝或过期
- **THEN** 系统不得返回旧缓存，并在新结果中只使用当前可见版本

#### Scenario: Skill 或生成契约升级
- **WHEN** Skill 定义版本、Prompt/生成契约版本或影响输出的规范化选项变化
- **THEN** 系统使用新的缓存身份，不复用旧版本生成的结果

### Requirement: 并发重复请求不会重复生成或泄露缓存
系统 SHALL 按缓存身份协调同一 owner 的并发等价请求，使同一源数据指纹最多有一个有效生成者；等待者 MUST 读取已提交结果或在有界等待后安全重试。缓存读取和写入 MUST 始终包含 tenant/user 范围，跨 owner 查询表现为未命中。

#### Scenario: 同一用户并发触发
- **WHEN** 同一用户同时提交两个等价 Daily Review 请求且输入指纹相同
- **THEN** 系统只允许一个请求执行模型生成，另一个复用已提交结果或进行有界重试，不产生两个冲突缓存版本

#### Scenario: 跨用户缓存访问
- **WHEN** 另一个用户构造相同日期、选项和源数据哈希尝试读取缓存
- **THEN** 系统按该用户自己的 owner scope 查询并返回未命中，不暴露原用户的回顾、证据或缓存元数据
