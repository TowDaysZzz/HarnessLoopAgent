# Agent 认证 API

浏览器只访问 Agent。成功登录或刷新后，Agent 通过 HttpOnly Cookie 建立会话；响应体不返回 RAG Token。

## 接口

```text
POST /v1/auth/register
POST /v1/auth/login
POST /v1/auth/refresh
POST /v1/auth/logout
GET  /v1/auth/me
```

注册和登录请求沿用 RAG 当前账号字段，Agent 只透传允许字段。`/me` 返回经过裁剪的用户、租户和角色信息。

所有错误采用统一格式：

```json
{"error":{"code":"unauthorized","message":"登录状态已失效"}}
```

生产环境 Cookie 必须启用 `Secure`，写接口必须校验 Origin/CSRF，认证日志不得记录密码、Token 或完整 Cookie。
