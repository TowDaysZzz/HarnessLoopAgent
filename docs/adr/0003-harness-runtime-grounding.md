# ADR 0003：在 Eino 外层增加 HarnessRuntime 和证据门禁

- 状态：已采纳
- 日期：2026-08-19

## 背景

Eino 负责模型协议、Tool Schema 和 Agent Tool Calling，但仅配置单次模型 HTTP 超时不足以约束完整 Run；仅用 Prompt 要求“只根据检索结果回答”也不能阻止模型跳过 Tool、使用低质量结果或虚构 citation。

项目借鉴 harness9 的有界循环、分层重试、Tool 超时、并发限制、Observer、结构化错误 Observation 和确定性 Eval Harness。项目不复制 harness9 的 Provider、Registry 和消息模型，避免与 Eino 重复。

## 决策

在 Eino 外增加以下可组合层：

```text
HarnessRuntime Budget
  -> Resilient ChatModel
  -> Eino ADK Tool Calling
  -> Bounded Tool
  -> Resilient RAG Retriever
  -> Evidence Policy
  -> Buffered Draft
  -> Citation Allowlist Validator
  -> Output or Refusal
```

模型只在流建立前对明确的临时错误重试，不对 400、401、403、证书错误、Context 取消和流中错误重试。RAG 可对网络错误、429、502、503 和 504 重试，并尊重 `Retry-After`。

## 证据规则

- 历史笔记问题必须观察到 `semantic_search_notes`。
- Tool 结果必须满足最小数量、Top score、单条 score 和 citation 完整性。
- RAG refusal 和失败的 citation check 直接产生不可用 Observation。
- 生产环境可要求 `evidence_gate_result=pass`；`disabled` 按 fail-closed 处理。
- 检索内容是非可信数据，不得作为指令执行；命中 Prompt Injection 标记的 chunk 被丢弃。
- 最终回答中的文件名和 chunk ID 必须属于本次检索 allowlist。
- 校验失败最多修复一次，仍失败则返回固定拒答文案。

## 流式语义

普通聊天保持模型增量流式输出。历史笔记问答先发送 `retrieving`、`validating` 状态，正文在验证通过后分块输出。该选择牺牲部分首 Token 延迟，以换取“未经验证内容永不发送”的确定性。

## 已知边界

当前问题路由使用确定性关键词规则，后续可替换成经过评测的分类器。当前 citation validator 验证来源归属，不对每个自然语言 claim 做语义蕴含判断；语义 Grounding Judge 应作为后续补充，不能替代确定性门禁。
