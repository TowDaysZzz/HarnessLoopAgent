# HarnessLoopAgent

一个基于 Eino 构建的个人笔记 Agent。该服务负责管理笔记、会话、长期记忆、洞察工作流和客户端流式响应；检索能力由独立部署的 RAG 服务通过 HTTP 提供。

## 当前里程碑

仓库已完成 Agent 初始化、RAG 接入、HarnessRuntime 稳定性/证据治理，会话、消息、Agent Run、持久化事件、可恢复 SSE，以及第一版 BFF 认证和笔记新增/查询/删除链路。长期记忆与一次性 Reminder 均提供默认关闭的 MySQL-only Workflow 试点；Reminder 可精确固定 Memory 版本，并通过 Dispatcher + Outbox 提供 at-least-once 投递边界。

## 环境要求

- Go 1.25 或更高版本
- 支持 Tool Calling 的 OpenAI 兼容模型

## 本地配置与启动

复制配置文件，在 `MODELS` 中填写模型配置档案，并通过 `ACTIVE_MODEL` 选择服务默认使用的模型：

```bash
cp config.example.yaml config.yaml
go run ./cmd/note-agent-server
```

`config.yaml` 可能包含密钥，因此已被 Git 忽略，也不会被复制到 Docker 镜像中。可以通过 `CONFIG_FILE` 指定其他 YAML 配置文件。与 YAML 字段同名的环境变量拥有更高优先级，适合在容器和密钥管理系统中使用。

同一个 YAML 可以配置多个 OpenAI 兼容接口，例如 DeepSeek、通义千问兼容模式和 OpenAI。服务使用 `ACTIVE_MODEL` 指定的配置档案；CLI 可以使用 `--model` 临时选择其他档案。原有的扁平字段 `MODEL_NAME`、`MODEL_API_KEY` 等仍然兼容。

## 会话与可恢复 SSE

会话 API 需要 MySQL。先创建独立数据库和最小权限账号，再在私有 `config.yaml` 中启用：

```yaml
DATABASE:
  ENABLED: true
  DSN: "note_agent:replace-me@tcp(127.0.0.1:3306)/note_agent?parseTime=true&charset=utf8mb4"
  AUTO_MIGRATE: true
  MAX_OPEN_CONNS: 20
  MAX_IDLE_CONNS: 10
  CONN_MAX_LIFETIME: "5m"
```

生产环境建议通过 `DATABASE_DSN` 和 `AUTH_SESSION_SECRET` 注入凭据，并在发布流程中执行迁移；将 `AUTO_MIGRATE` 设为 `false`。启用 `AUTH` 后，浏览器只持有 HttpOnly Cookie，Agent 服务端负责保存和刷新 RAG Token。

启用认证和笔记投影：

```yaml
AUTH:
  ENABLED: true
  SESSION_SECRET: "at-least-32-random-characters-from-a-secret-manager"
  SESSION_TTL: "168h"
  COOKIE_NAME: "note_agent_session"
  COOKIE_SECURE: false

NOTE:
  ENABLED: true
  KB_ID: 5
```

认证接口：`POST /v1/auth/register`、`POST /v1/auth/login`、`POST /v1/auth/refresh`、`POST /v1/auth/logout`、`GET /v1/auth/me`。

个人知识库接口：`GET /v1/knowledge-base`、`POST /v1/knowledge-base`。首次使用时调用 `POST`；Agent 会优先绑定该用户在 RAG 中已有的个人知识库，没有时才创建一个。绑定以 `tenant_id + user_id` 唯一保存到 Agent MySQL，笔记写入和 Eino 检索均使用该绑定。重复调用不会为同一 Agent 用户重复创建知识库。

笔记接口：`POST /v1/notes`、`GET /v1/notes`、`GET /v1/notes/{note_id}`、`GET /v1/notes/{note_id}/status`、`DELETE /v1/notes/{note_id}`。创建和删除必须携带 `Idempotency-Key`。笔记原文首先写入 Agent MySQL，再通过 Outbox 投影到 RAG；RAG 任务返回 `pending` 时本地状态保持 `indexing`，不会提前标记为 `indexed`。

Reminder 的 API、功能矩阵、投递保证及灰度/回滚步骤见 [Reminder API](docs/api/agent-reminder-api.md)。

Chat 的生产主链路只负责普通多轮对话和笔记 RAG，不再分发 Note 写入、Memory、Reminder、Daily Review 或其他 Workflow/Skill。相关实现与存储暂时保留，供独立控制面和第二阶段迁移使用；在聊天中输入“回顾今天”“记住……”或“提醒我……”只会作为普通文本交给模型，不会产生业务写入副作用。历史 Daily Review 设计见 [Daily Review Skill](docs/api/daily-review-skill.md)，但它当前不再由 Chat 自然语言触发。

创建会话和 Run：

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{"title":"每日笔记"}'

curl -sS -X POST http://127.0.0.1:8080/v1/sessions/<session_id>/runs \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: request-001' \
  -d '{"message":"总结一下我们刚才讨论的内容"}'
```

订阅事件，并在断线后从最后一个事件继续：

```bash
curl -N http://127.0.0.1:8080/v1/runs/<run_id>/events
curl -N http://127.0.0.1:8080/v1/runs/<run_id>/events -H 'Last-Event-ID: 12'
```

客户端断开 SSE 不会取消后台 Run；需要显式调用 `POST /v1/runs/{run_id}/cancel`。同一会话只允许一个活跃 Run，重复 `Idempotency-Key` 返回原 Run。历史笔记问题继续采用验证后输出，未通过 Grounding 校验的模型草稿不会进入 SSE。

当前事件主干为 `run.queued -> run.started -> retrieval.decided -> run.status/tool.completed/text.delta -> run.completed`；`context.truncated` 只在上下文被裁剪时出现。`retrieval.decided` 只记录布尔结果和 allow-listed reason：明确笔记问题及其省略式追问会在当前 Run 重新检索，普通聊天不强制检索，但模型仍可自主调用只读的 `semantic_search_notes`。

当前上下文管理使用 Token 预算下的最近消息窗口，接口已允许后续替换为参考 harness9 的 Progressive Compactor。原始消息始终保存在 MySQL，不会被摘要覆盖或删除。

启用 RAG 时，在 `RAG` 节点配置独立服务地址、API Key 和允许检索的知识库：

```yaml
RAG:
  ENABLED: true
  BASE_URL: "http://127.0.0.1:8899"
  API_KEY: "rag_replace_me"
  KB_IDS: [2]
  TIMEOUT: "10s"
  TOP_K: 5
  STRATEGY_PROFILE: "default"
```

也可以使用 `RAG_ENABLED`、`RAG_BASE_URL`、`RAG_API_KEY`、`RAG_KB_IDS`、`RAG_TIMEOUT`、`RAG_TOP_K` 和 `RAG_STRATEGY_PROFILE` 覆盖 YAML。`RAG_KB_IDS` 使用逗号分隔，例如 `2,3`，只作为 CLI 或未启用用户绑定场景的兼容默认值；已登录 Web 用户始终使用个人知识库绑定。API Key、知识库 ID 和策略不会暴露为模型可填写的 Tool 参数。

## MySQL-only Memory 试点

Memory 默认关闭。当前生产基线是 `LLM 结构化 + MySQL 精确召回`：MySQL 保存完整事实、状态、版本和冲突关系；不依赖向量库，也不提供开放式语义召回。启用前需要数据库迁移和可用的结构化模型 Runner：

```yaml
MEMORY:
  ENABLED: true
  WORKFLOW_PILOT_ENABLED: true
  RECALL_MODE: "exact-only"
  RAG_ENABLED: false
  PROJECTION_ENABLED: false
```

Memory Capture 通过独立控制面启动，不再从 Chat 自然语言自动识别“记住”或“修改偏好”。普通聊天、Tool 结果和节点完成事件不会写入长期 Memory。每次长期写入仍先进入 Review，用户可批准、拒绝或编辑。控制面详见 `docs/api/agent-memory-api.md`。

exact-only 只按固定 `MemoryRef`、`EntityRef`、`namespace + slot_key`、内容哈希或显式局部 scope 召回；没有稳定 selector 时返回空结果或澄清，不执行 owner 全量扫描。因此它不能达到向量检索的同义改写、模糊描述或跨措辞召回率。

Projection 关闭时，active Memory 仍在同一 MySQL 事务写入 `pending` Outbox，作为未来回填日志；不会调用 RAG、重试或影响 readiness。回滚时先关闭 `WORKFLOW_PILOT_ENABLED`，再关闭 `MEMORY.ENABLED`，重启服务即可；MySQL Memory、审核记录和 Outbox 均保留。当前版本不要直接打开 Memory RAG/Projection；未来 RAG Memory API 和投影 Worker 发布后，再配置独立 Memory endpoint/凭证、保持 Projection Version 一致并消费历史 pending Outbox。这里的 `MEMORY.RAG_ENABLED` 与笔记/认证使用的顶层 `RAG.ENABLED` 是两个独立开关。

## HarnessRuntime 稳定性

`AGENT` 控制一次完整 Run 的时间和调用预算，`RESILIENCE` 控制模型与 RAG 的重试、并发隔离和熔断。模型只会在流尚未建立时重试；一旦流已开始，中途失败会终止本次 Run，不会重新生成并输出重复内容。RAG 检索是只读操作，只对网络错误、429 和 5xx 临时错误进行有界重试。

默认限制见 `config.example.yaml`。所有限制也支持同名环境变量，例如 `AGENT_RUN_TIMEOUT`、`MODEL_MAX_ATTEMPTS`、`RAG_MAX_ATTEMPTS`、`MODEL_MAX_CONCURRENCY` 和 `CIRCUIT_FAILURE_THRESHOLD`。

## RAG 证据治理

历史笔记问题采用验证后输出：模型生成正文时先在服务端缓冲；只有本次 Run 观察到 `semantic_search_notes`、Tool 返回 `usable=true`，并且最终回答中的文件名和 chunk ID 都属于本次检索白名单时，正文才会输出。模型跳过检索、结果低于分数阈值、citation 不完整、RAG refusal 或检索内容包含 Prompt Injection 时，Agent 程序化拒答。

当前 RAG 已启用 `enable_evidence_refusal` 和 `enable_citation_consistency`。Agent 默认要求 `evidence_gate_result=pass` 和 `citation_check.supported=true`；任一字段缺失、被禁用或未通过都会 fail-closed，历史笔记问题不会进入答案生成。

## 测试真实模型

使用默认模型进行一次流式对话：

```bash
go run ./cmd/note-agent-cli -- "你好，请用一句话介绍你自己"
```

选择其他模型配置档案，并显示 Eino 语义事件：

```bash
go run ./cmd/note-agent-cli --model qwen --events -- \
  "请调用 echo 工具原样返回：Eino tool calling works"
```

也可以使用 Makefile：

```bash
make chat PROMPT="你好"
```

CLI 会在标准错误中显示所选模型名称和接口地址，但不会输出 API Key。当前 CLI 是一次性 Smoke Test，每次执行只包含一轮对话，不持久化会话或笔记。

启用 RAG 后验证 Tool Calling 和来源引用：

```bash
go run ./cmd/note-agent-cli --events -- \
  "请查询我之前关于垃圾回收的记录，只根据检索结果回答，并给出来源"
```

事件流应包含 `tool.completed tool=semantic_search_notes`、`text.delta` 和 `run.completed`；最终回答应引用检索结果返回的文件名和 chunk ID。没有检索结果或 RAG 拒绝回答时，Agent 不应使用模型常识补全答案。

检查服务状态：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

## 验证项目

```bash
make check
make test-race
make docker-build
```

使用真实 RAG 服务运行 Client 集成测试：

```bash
RAG_INTEGRATION=1 \
RAG_BASE_URL=http://127.0.0.1:8899 \
RAG_API_KEY="${RAG_API_KEY}" \
RAG_INTEGRATION_KB_ID=2 \
make integration-rag
```

使用一次性 MySQL 测试库验证 Repository：

```bash
MYSQL_INTEGRATION_DSN='user:password@tcp(127.0.0.1:3306)/disposable_db?parseTime=true' \
make integration-mysql
```

Memory MySQL-only E2E 使用同一环境变量：

```bash
MYSQL_INTEGRATION_DSN='user:password@tcp(127.0.0.1:3306)/disposable_db?parseTime=true' \
go test -v ./internal/memoryworkflow ./internal/platform/mysqlstore
```

服务边界、后续阶段和提交顺序记录在 `docs/roadmap.md` 中；RAG HTTP 边界记录在 `docs/adr/0002-rag-http-contract.md` 中。
