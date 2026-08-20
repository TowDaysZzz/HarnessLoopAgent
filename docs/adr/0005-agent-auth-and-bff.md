# ADR 0005：Agent 认证与 BFF 会话

## 状态

已接受。

## 决策

RAG 的登录体系是用户身份唯一来源。浏览器只调用 Agent API；Agent 作为 BFF 调用 RAG 的注册、登录、刷新和用户信息接口。

Agent 使用 HttpOnly、Secure（生产环境）和 SameSite Cookie 保存短期会话标识。RAG Access Token 与 Refresh Token 仅保存在 Agent 服务端会话存储，不写入 LocalStorage，也不作为模型输入或 Tool 参数。

所有用户请求由认证中间件解析服务端会话，并生成可信的 `Principal`（`user_id`、`tenant_id`、角色和 RAG Access Token）。业务 Handler 不接受客户端声明的用户或租户身份。

RAG 返回 401 时，Agent 最多执行一次受控 Token 刷新并重放请求；刷新失败时清理 Agent 会话并返回 401。日志、Trace 和错误响应不得包含 Token、密码或 Cookie。

## 后果

- Web 前端无需接触 RAG Token。
- Agent 可以统一实施 CSRF、权限、审计和 Token 轮换策略。
- 多实例部署时，服务端认证会话必须使用共享存储。
