# MySQL Store 迁移 GORM 验证记录

验证日期：2026-08-24

## 实现边界

`internal/platform/mysqlstore` 的运行时数据库入口已统一为 `*gorm.DB`。`mysqlstore.Open` 通过 GORM MySQL driver 建立连接，并仅通过 `gormDB.DB()` 获取底层连接池，用于配置 `MaxOpenConns`、`MaxIdleConns`、`ConnMaxLifetime`、执行 Ping 和 Close。Repository 与 migration 不直接使用 `database/sql` CRUD。

普通单表 CRUD 使用 GORM `Create`、`First`、`Find`、`Delete` 和显式字段 `Updates`。以下 MySQL 专用或查询计划敏感场景仍通过当前 GORM session/transaction 的 `Raw`、`Exec` 或 Clause 执行：

- 版本化 SQL migration、schema introspection、历史 owner 回填和精确索引补充；
- `FOR UPDATE`、`SKIP LOCKED`、动态 Memory exact selector 和复杂排序；
- 必须检查 `RowsAffected` 的状态机、lease、row-version 和幂等条件更新。

所有多语句原子流程使用 `db.WithContext(ctx).Transaction`。事务内的 Chat run、Workflow commit、Memory supersede、Note/Projection Outbox 均只使用闭包传入的 `tx`。更新禁止使用 `Save`，零值和空字符串通过 `Updates(map[string]any{...})` 或显式字段选择写入。

连接配置保持生产兼容：`SkipDefaultTransaction=true`、`PrepareStmt=false`、`TranslateError=true`，日志使用参数化且默认静默，避免输出 token、Note/Memory 正文、checkpoint 或 edit payload。

## Schema 管理

现有嵌入式、版本化 SQL migration 仍是 17 张 MySQL 表的唯一结构来源。GORM row model 只负责运行时字段映射，不嵌入 `gorm.Model`，不声明 association，也不调用 `AutoMigrate`。生成列、复合唯一索引、外键和历史回填继续由 SQL migration 管理，因此本次发布不要求新的 DDL 或数据迁移。

## 自动化验证

无数据库 DSN 的完整回归命令：

```bash
GOCACHE=/tmp/harnessloopagent-go-cache go test ./...
GOCACHE=/tmp/harnessloopagent-go-cache \
go test -race ./internal/memoryworkflow ./internal/platform/httpserver ./internal/workflow ./internal/platform/mysqlstore
go vet ./...
git diff --check
openspec validate migrate-mysqlstore-to-gorm --strict
```

静态门控会扫描 `internal/platform/mysqlstore` 的非测试 Go 文件，禁止 `QueryContext`、`QueryRowContext`、`ExecContext`、`BeginTx`、`Save` 和 `AutoMigrate`。旧 `database/sql` 仅保留在兼容性测试中，用于从迁移前实现的视角读取 GORM 写入结果。

## 真实 MySQL 8.x 验证

使用一次性 MySQL 8.4 容器和全新空数据库，按默认并行 package 模式执行：

```bash
MYSQL_INTEGRATION_DSN='user:password@tcp(127.0.0.1:3306)/disposable_db?parseTime=true' \
GOCACHE=/tmp/harnessloopagent-go-cache \
go test -count=1 -v ./internal/memoryworkflow ./internal/platform/mysqlstore
```

该命令已通过，并覆盖：

- migration 首次、重复和并发执行，以及 17 张表与精确召回索引；
- Auth、KnowledgeBase、NoteDraft、EditPayload、Note/Outbox、Chat Repository 生命周期和 owner 隔离；
- Chat 单 session active-run 并发 guard、Outbox/EditPayload 并发领取；
- Durable Workflow 生命周期、状态版本、lease 续约、过期 lease 接管和事务回滚；
- Memory lifecycle、exact recall `EXPLAIN`、原子 supersede、旧版本 obsolete 过滤、Projection `SKIP LOCKED` 多 worker 无重复领取；
- MySQL-only Capture 的重启恢复、duplicate noop、用户 correction、审核编辑重判和 owner-scoped Outbox；
- GORM 写入后由旧 `database/sql` 读取，以及旧 SQL 样本写入后由 GORM 读取。

双向兼容性测试和 Memory/Workflow 重启恢复测试共同验证：GORM 版本可读取、更新既有 schema 数据，旧实现也可读取 GORM 产生的数据，序列化与表结构无需变更。

## 发布与回滚

发布时继续使用现有 DSN 和连接池配置，并保持“打开 Store → 执行 SQL migrations → 装配 Repository”的启动顺序。上线后重点观察数据库错误率、duplicate/state-conflict/lease-lost 分类、事务耗时和连接池使用情况。

若需回滚，停止 GORM 版本二进制并部署上一版本即可，不执行 DDL 回滚，也不删除迁移期间产生的业务记录、事件或 Outbox。由于表结构、字段、JSON/checkpoint 编码和 Repository 契约未改变，旧 `database/sql` 实现可继续读写同一数据库。
