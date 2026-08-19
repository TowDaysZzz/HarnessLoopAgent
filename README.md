# HarnessLoopAgent

一个基于 Eino 构建的个人笔记 Agent。该服务负责管理笔记、会话、长期记忆、洞察工作流和客户端流式响应；检索能力由独立部署的 RAG 服务通过 HTTP 提供。

## 当前里程碑

仓库当前只完成了第一阶段的项目初始化，包括 Eino `ChatModelAgent`、ADK 流式 Runner、确定性的 Echo Tool、配置校验和健康检查接口。现阶段暂不包含数据库、RAG 集成、对外 SSE、长期记忆和洞察工作流。

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

检查服务状态：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

## 验证项目

```bash
make check
make docker-build
```

服务边界、后续阶段和提交顺序记录在 `docs/roadmap.md` 中。
