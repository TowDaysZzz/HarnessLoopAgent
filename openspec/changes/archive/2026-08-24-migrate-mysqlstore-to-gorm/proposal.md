## Why

当前 `internal/platform/mysqlstore` 直接使用 `database/sql` 维护约 45 处查询、64 处写入和 22 个显式事务，连接、扫描、错误映射及事务样板分散在多个仓储中。项目需要以 GORM 统一数据库访问和模型映射，同时保持既有 MySQL 并发、幂等、租约和 Memory 冲突更新语义不变，并通过真实 MySQL 回归证明迁移没有行为退化。

## What Changes

- 引入 `gorm.io/gorm` 与 `gorm.io/driver/mysql`，将 `mysqlstore` 的运行时数据库入口统一为 `*gorm.DB`。
- 为 Chat、Auth/Note、KnowledgeBase、NoteDraft、Durable Workflow、Memory 与 Projection Outbox 建立独立持久化模型和领域转换边界。
- 将普通 CRUD、条件更新和事务迁移到 GORM API；行锁、`SKIP LOCKED`、动态精确召回和复杂 Join 使用 GORM Clauses 或 GORM Raw 保留原有 SQL 语义。
- 保留嵌入式、版本化 SQL migration 作为数据库结构来源，不使用 GORM `AutoMigrate` 管理生成列、复合唯一索引、外键或历史回填。
- 统一 GORM 的 context 传递、连接池配置、关闭、not-found/duplicate/state-conflict 错误映射和日志安全策略。
- 更新单元测试、Repository 集成测试、并发锁测试、Memory/Workflow 真实 MySQL E2E 与验证文档。
- 不修改现有 HTTP/API、Repository 接口、表结构、Memory 状态机、Workflow checkpoint 格式或运行时默认配置。

## Capabilities

### New Capabilities

无。本变更是数据库访问实现重构，不引入新的外部能力，change 元数据声明 `skip_specs: true`。

### Modified Capabilities

无。现有 Chat、Workflow 与 Memory 的规格行为均作为迁移不变量保留。

## Impact

- 主要影响 `internal/platform/mysqlstore`、`cmd/note-agent-server` 的数据库装配以及相关集成测试和验证文档。
- `go.mod`/`go.sum` 将增加 GORM core 与 MySQL driver；底层 `go-sql-driver/mysql` 由 GORM MySQL driver 使用。
- 生产数据库表和既有数据不做破坏性迁移；部署仍运行现有 SQL migrations。
- 迁移风险集中在事务边界、行锁、条件更新、JSON/NULL 映射、MySQL 错误分类和查询计划，必须通过 MySQL 8.x 全新库及既有 schema 回归验证。
