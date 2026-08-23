# LLM 结构化 + MySQL 精确 Memory 验证记录

验证日期：2026-08-24

## 自动化结果

以下命令已通过：

```bash
GOCACHE=/tmp/harnessloopagent-go-cache go test ./...
GOCACHE=/tmp/harnessloopagent-go-cache go test -race ./internal/memoryworkflow ./internal/platform/httpserver
openspec validate add-llm-mysql-exact-memory-runtime --strict
```

覆盖范围包括严格 Draft/Recall Plan、MySQL 精确查询、无 selector 安全行为、确定性排序与预算、Capture/Review/Resume、duplicate noop、correction 原子 supersede、编辑重新冲突、跨 owner 隔离、Prompt Injection、非法 LLM JSON、并发/陈旧 Wait、Projection disabled 和敏感指标白名单。

## 真实 MySQL 门控

真实 MySQL 测试使用一次性测试库：

```bash
MYSQL_INTEGRATION_DSN='user:password@tcp(127.0.0.1:3306)/disposable_db?parseTime=true' \
GOCACHE=/tmp/harnessloopagent-go-cache \
go test -v ./internal/memoryworkflow ./internal/platform/mysqlstore
```

本次环境未设置 `MYSQL_INTEGRATION_DSN`，因此以下测试按明确门控显示 `SKIP`，没有记为真实数据库通过：

- `TestMySQLOnlyMemoryCaptureRestartRecallCorrectionEditAndDeferredOutbox`
- `TestMemoryMigrationAndRepositoryLifecycle`
- MySQL `EXPLAIN FORMAT=JSON` 精确索引测试

普通测试、fake Repository 应用闭环、HTTP race 和 OpenSpec strict validation 均已通过。发布到测试环境前必须设置 DSN 再执行上述真实 MySQL 套件。

## 回滚与未来迁移

回滚顺序是先关闭 `MEMORY_WORKFLOW_PILOT_ENABLED`，确认不再产生新 Capture，再关闭 `MEMORY_ENABLED` 并重启。回滚不删除 MySQL Memory、Workflow checkpoint、审核事件或 pending Outbox。

当前发布保持 `MEMORY_RECALL_MODE=exact-only`、`MEMORY_RAG_ENABLED=false`、`MEMORY_PROJECTION_ENABLED=false`。未来启用 RAG 前必须先发布兼容的 `/v1/memories/search`、`/v1/memories/index` 和投影 Worker，固定 `MEMORY_PROJECTION_VERSION`，在测试环境回放历史 pending Outbox，并比较 exact-only 与 hybrid 的召回和过滤指标。
