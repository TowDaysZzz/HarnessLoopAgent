## Context

参见 `proposal.md` 的 Why。当前 `mysqlstore.Store` 持有 `*sql.DB`，并由一个包实现 Chat、Auth/Note、KnowledgeBase、NoteDraft、Durable Workflow 和三层 Memory 仓储。现有实现不仅包含普通 CRUD，还依赖 MySQL 的生成列唯一约束、JSON、复合索引、`FOR UPDATE`、`SKIP LOCKED`、受版本与状态约束的更新、事务内事件/Outbox 和 MySQL 错误码。

数据库 schema 由 7 个嵌入式 SQL migration 文件管理，共 17 张表；生产装配通过 `mysqlstore.Open` 配置连接池，再调用 `Store.Migrate`。领域层通过 Repository 接口依赖存储，迁移不得把 GORM 类型泄漏到领域接口，也不得改变表结构、序列化格式、owner 隔离或错误契约。

## Goals / Non-Goals

**Goals:**

- 让所有运行时数据库访问通过 `*gorm.DB` 发起，并统一 context、事务、连接池、日志与错误处理。
- 用独立持久化模型承接表字段与 GORM tag，在仓储边界显式转换领域对象。
- 在迁移普通 CRUD 的同时，原样保持 Chat/Workflow/Memory 的锁、幂等、条件更新和原子提交语义。
- 保持现有 Repository 接口、schema、migration 顺序、JSON/checkpoint 兼容性和启动配置兼容。
- 让默认测试、race 测试和真实 MySQL 8.x E2E 能证明行为与并发语义没有退化。

**Non-Goals:**

- 不使用 `AutoMigrate` 生成或修复生产 schema。
- 不重命名表/列、删除历史 migration、转换历史数据或增加业务字段。
- 不修改 Memory 召回、冲突策略、Workflow 状态机、HTTP API 或 RAG/Projection 开关。
- 不为了“零 SQL 字符串”而把复杂或 MySQL 专用查询强行改写成不可审计的 ORM 链。
- 不在本变更中引入读写分离、分库分表、缓存或新的数据库可观测平台。

## Decisions

### 1. GORM 是唯一运行时数据库入口，底层 `*sql.DB` 只管理连接池生命周期

`Store` 持有 `db *gorm.DB`。`Open` 使用 `gorm.io/driver/mysql` 打开连接，并通过 `db.DB()` 获取底层连接池，仅用于设置 `MaxOpenConns`、`MaxIdleConns`、`ConnMaxLifetime`、Ping 和 Close；Repository、migration 和测试查询不直接调用 `database/sql` CRUD。

所有操作显式使用 `db.WithContext(ctx)`。GORM logger 必须使用参数化输出并禁止记录 Memory 正文、Note 内容、token、checkpoint JSON 或 edit payload；默认不启用 `PrepareStmt` 全局缓存。为匹配现有单语句原子性并避免隐式事务开销，配置 `SkipDefaultTransaction: true`，所有多语句原子流程继续显式 `Transaction`。

备选方案是同时保存 `*gorm.DB` 与 `*sql.DB` 并逐步混用。该方案迁移容易，但会长期保留双入口，使 context、事务和测试无法证明已完成迁移，因此不采用。

### 2. 持久化模型与领域模型分离

在 `internal/platform/mysqlstore` 内按聚合建立未导出的 row model，明确 `TableName`、列名、主键、nullable 字段和 JSON 原始字节。模型不嵌入 `gorm.Model`，以避免自动软删除、默认整型 ID 和非预期时间字段；不声明会触发级联保存的 GORM association。

每个仓储提供显式的 `toRow`/`fromRow` 或等价转换函数。JSON 字段继续使用现有 codec 和校验流程，在写入前 marshal、读取后 unmarshal；NULL 使用指针或受控 nullable 类型，所有时间在边界规范为 UTC。生成列只读且永不出现在 Create/Updates 字段集合中。

直接给领域结构添加 GORM tag 虽然文件更少，但会让领域层依赖表结构并容易触发默认命名/零值行为，因此不采用。

### 3. 按查询语义选择 GORM API，不追求消灭 SQL

- 单表插入、主键/owner 查询、固定条件列表和简单删除使用 `Create`、`First`、`Find`、`Delete`。
- 状态机、lease 和 row-version 更新使用 `Model(...).Where(...).Updates(map[string]any{...})`，必须检查 `RowsAffected == 1`；禁止使用可能产生 upsert 或覆盖零值的 `Save`，也禁止依赖 struct Updates 自动忽略零值。
- `FOR UPDATE` 和 `SKIP LOCKED` 使用 `clause.Locking`；一次锁定多条 Memory 时继续按 ID 排序，保持稳定锁顺序。
- 动态 Memory exact selector、复杂 Join、内层排序分页、批量 OR、migration DDL 和 `EXPLAIN FORMAT=JSON` 允许使用 `Raw`/`Exec`，但必须由当前 GORM transaction/session 执行。
- owner 条件必须与业务选择条件在同一查询内出现；不得先查 ID 再做未限定 owner 的第二次读取或更新。

备选方案是所有查询都使用 GORM 链式 API。它会使动态 OR、锁定领取和精确索引查询更难审计，并可能改变查询计划，因此不采用。

### 4. 显式保留事务、锁和幂等边界

所有现有多语句事务一对一迁移到 `db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { ... })`，事务闭包内不得回退使用根 `db`。包括但不限于：Chat run/message/event 创建，Note 与 Outbox，Draft consume，Workflow claim/commit，Memory mutation/transition/supersede/expire，以及 Projection claim。

数据库唯一约束仍是最终并发裁决者。Workflow claim、lease renew、Memory row version、状态转换和 Outbox 状态更新继续使用带完整前置条件的单条 UPDATE，并用 `RowsAffected` 映射为原有 state/lease conflict。duplicate/noop 和 idempotency replay 继续在相同事务与锁范围内处理。

GORM hooks、自动 association、批量 Save 和隐式 optimistic-lock plugin 均不启用，以免产生额外 SQL 或改变事件顺序。

### 5. 保留版本化 SQL migrations，不使用 AutoMigrate

`Store.Migrate` 继续按文件名顺序执行嵌入式 SQL，并保留 schema introspection、历史 owner 回填、精确索引补充和并发创建索引容错；实现改为 GORM `Exec`/`Raw().Scan`。这是因为生成列、函数型 guard、外键、复合唯一索引和历史回填不能由 `AutoMigrate` 可靠表达。

新的 GORM row model 只描述运行时映射，不作为 schema 来源。测试必须验证 `Migrate` 可重复执行、并发执行，并验证全新库表/索引与现有 migration 预期一致。

### 6. 统一错误映射且保持领域错误契约

开启并验证 GORM 的 duplicate error translation；同时保留对 `*mysql.MySQLError` 的回退识别，以区分 1062、锁超时/死锁和其他数据库错误。`gorm.ErrRecordNotFound` 映射为各领域既有 NotFound；`RowsAffected == 0` 结合必要的有界复查映射为 NotFound、StateConflict、ClaimConflict 或 LeaseLost。

不把 GORM 错误直接穿透到 HTTP/领域层。错误日志不得拼接含用户内容的 SQL 参数。

### 7. 分聚合迁移并始终保持主分支可测试

迁移顺序为：GORM 基础设施与 migration → KnowledgeBase/EditPayload/NoteDraft → Auth/Note → Chat → Durable Workflow → Memory/Projection → 清理旧扫描器与直接 SQL CRUD。每一阶段都保持 Repository 接口编译通过并运行对应测试，避免一次性大爆炸替换。

不增加生产双写或运行时切换开关，因为 schema 和 Repository 契约不变，双写会引入新的不一致风险。回滚通过部署上一版本二进制完成。

## Risks / Trade-offs

- [GORM 零值更新被忽略，导致状态、计数或空字符串无法写入] → 所有条件更新使用显式 `map[string]any`/`Select`，增加零值回归测试并禁止 `Save`。
- [ORM 生成 SQL 改变锁范围或索引选择] → 锁定与动态精确查询保留 Clauses/Raw，真实 MySQL 使用并发测试与 EXPLAIN 门控。
- [事务闭包误用根 DB 导致部分提交] → 事务 helper 只向闭包传递 `tx`，对失败注入验证 Memory/Workflow/Outbox 全量回滚。
- [GORM 默认 hook、association 或软删除产生额外写入] → row model 不嵌入 `gorm.Model`、不声明 association，并通过 SQL 计数/状态断言验证。
- [JSON、NULL 和 UTC 映射变化造成 checkpoint 或记忆不可读] → 保留现有 codec，增加既有行读取、round-trip 和重启恢复测试。
- [错误包装改变 `errors.Is/As` 行为] → 集中错误映射并覆盖 duplicate、not-found、state conflict、deadlock/timeout。
- [引入 ORM 增加少量延迟和依赖体积] → 跳过默认单语句事务、不启用全局 prepared statement，保留有界查询并比较关键 E2E 时延与查询计划。
- [测试直接访问原 `*sql.DB`，掩盖未迁移调用] → 测试改用 GORM Raw/Exec 或公开行为断言，并增加静态门控检查运行时文件不再直接调用 `QueryContext`/`ExecContext`/`BeginTx`。

## Migration Plan

1. 引入 GORM 依赖、Store 初始化和持久化模型，但不改变 schema。
2. 按聚合逐步迁移仓储，每完成一组即运行对应单元/集成测试。
3. 在一次性 MySQL 8.x 全新数据库运行 migration、Repository、Workflow、Memory 与 EXPLAIN 套件；随后在由现有 migrations 构建且包含兼容样本数据的数据库执行升级读取/写入回归。
4. 运行全仓测试、race 测试、`git diff --check` 和 OpenSpec validation，记录真实 MySQL 版本与命令。
5. 发布时保持现有配置和 migration 启动顺序，观察数据库错误率、冲突率、事务延迟和连接池指标。

回滚不需要数据库 DDL：停止新二进制并部署上一版本。由于表结构、字段和序列化格式不变，旧 `database/sql` 实现可直接继续读写；回滚不得删除迁移期间产生的业务数据、事件或 Outbox。
