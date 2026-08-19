# ADR 0001：Eino 依赖版本基线

- 状态：已采纳
- 日期：2026-08-18

## 决策

Agent 服务初始化使用 `github.com/cloudwego/eino v0.9.9`，OpenAI 兼容模型组件使用 `github.com/cloudwego/eino-ext/components/model/openai v0.1.13`。

该版本组合来自初始化时可用的稳定版本，并已经通过 `ChatModelAgent`、`Runner`、Tool 和流式 API 的编译与测试验证。项目固定使用明确的发布版本，不直接依赖开发分支。

## 影响

独立部署的 RAG 服务可以继续使用 Eino v0.7.28。两个服务通过有版本的 HTTP 契约通信，不互相导入 Eino 实现包。后续升级依赖时，需要建立独立的兼容性 PR，并使用新的 ADR 替代本决策。
