# ADR 0007：意图路由与 Executor 扩展边界

## 状态

已接受。

## 决策

当前采用单主 Agent、确定性意图路由和 Eino Tool/Workflow。笔记页面的显式新增与删除直接调用 NoteService，不经过 LLM；自然语言中的“记住/记一笔”由意图路由识别后调用写笔记 Tool。

路由输出统一为 `RouteDecision`，执行入口统一为 `Executor`，请求上下文统一为 `RunContext`。`RunContext` 携带可信主体、Trace、预算、会话和取消信号，但不向模型暴露凭证。

简单意图优先走确定性短链路；复杂或不明确意图进入主 Agent。系统自动推断出的长期偏好只创建 `candidate`，必须经用户确认才转为长期记忆。

预留 `ENABLE_MULTI_AGENT` 开关和父子 Run 标识，但当前不创建子 Agent。未来增加多 Agent 时，通过新增 Executor 和编排策略扩展，不改变 HTTP 契约、权限模型和 Observer 事件模型。

## 后果

- 当前链路容易测试且延迟可控。
- 后续多 Agent 演进无需重写领域服务。
- HarnessRuntime 继续位于 Eino 外层，统一负责预算、重试、超时、熔断、Grounding、观察与恢复。
