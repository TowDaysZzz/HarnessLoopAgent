## 1. GORM 基础设施与迁移入口

- [x] 1.1 引入 `gorm.io/gorm` 与 `gorm.io/driver/mysql`，整理 MySQL driver 依赖，并以 `go mod tidy`、`go list -m all` 和 `go test ./internal/platform/mysqlstore` 验证依赖可解析且包可编译。
- [x] 1.2 将 `mysqlstore.Store` 改为持有 `*gorm.DB`，通过 `gormDB.DB()` 配置连接池、Ping 和 Close，设置 `SkipDefaultTransaction`、禁用全局 PrepareStmt 并配置参数化安全日志；用 Store Open/Close 测试验证空 DSN、连接失败、连接池参数和关闭行为。
- [x] 1.3 建立未导出的 GORM row models 与显式 TableName/列 tag，覆盖 17 张现有表且不嵌入 `gorm.Model`、不声明 association、生成列只读；用模型映射测试验证表名、JSON/NULL/UTC round-trip 和零值字段。
- [x] 1.4 将 `Store.Migrate`、schema introspection、owner 回填和精确索引补充改为 GORM `Exec`/`Raw().Scan`，保留嵌入式 SQL 与并发建索引容错；在真实 MySQL 空库验证 migration 首次、重复和并发执行均成功且表/索引不变。
- [x] 1.5 集中实现 `gorm.ErrRecordNotFound`、`gorm.ErrDuplicatedKey`、MySQL 1062/锁错误及 RowsAffected 的领域错误映射；用表驱动测试验证 errors.Is/As 和既有 NotFound/StateConflict/LeaseLost 契约。

## 2. 简单仓储迁移

- [x] 2.1 将 KnowledgeBase 读取和幂等创建迁移到 GORM，保持 tenant/user/KB 唯一约束与字段映射；运行 knowledgebase 服务测试和真实 Repository 生命周期测试验证 owner 隔离与重复创建。
- [x] 2.2 将 Memory EditPayload 创建、带锁一次性消费、过期和敏感 payload 行为迁移到 GORM transaction/locking；运行 `go test ./internal/memoryworkflow -run EditPayload` 并在真实 MySQL 验证并发消费只有一个成功。
- [x] 2.3 将 NoteDraft 创建、pending 查询和审核状态转换迁移到 GORM，显式写入空值与状态字段；运行 notedraft 测试并在真实 MySQL 验证 pending guard、错误 content hash 和重复审核。

## 3. Auth、Note 与 Outbox 迁移

- [x] 3.1 将用户会话 create/get/update/delete 迁移到 GORM，确保 token 密文不进入日志且 NotFound 行为不变；运行 auth 单元测试和真实 Repository 生命周期测试。
- [x] 3.2 将 Note create/get/list/update/delete 迁移到 GORM，保留分页排序、JSON tags、owner 条件、external ID 和 create idempotency；运行 note/http 测试并验证空 tags、空 external ID 与跨 owner 隔离。
- [x] 3.3 将 Note 与 Outbox 的原子写入、claim `SKIP LOCKED`、complete/fail/retry 迁移到 GORM transaction 与 locking clause；增加并发 worker 测试并验证失败注入不会产生 Note/Outbox 部分提交或重复领取。

## 4. Chat Runtime 迁移

- [x] 4.1 将 Chat Session 和 Message 的创建、owner 查询、倒序截取再正序返回迁移到 GORM/Raw，并运行 chat Repository/API 测试验证排序、limit 和跨 owner 隔离。
- [x] 4.2 将 CreateRun 的 session 行锁、active-run guard、消息 sequence、queued event 和 idempotency 迁移到单个 GORM transaction；用真实 MySQL 并发测试验证同 session 仅一个 active run、重复 key replay 且失败全回滚。
- [x] 4.3 将 run start/append/complete/fail/cancel 和事件序列条件更新迁移到 GORM，禁止 `Save` 并检查 RowsAffected；运行 Chat 生命周期和并发状态冲突测试。
- [x] 4.4 将 queued/running run 恢复扫描与锁定逻辑迁移到 GORM locking/Raw，验证恢复只处理目标状态且事件 sequence 连续。

## 5. Durable Workflow 迁移

- [x] 5.1 将 Workflow run/wait/node-event row 映射和 checkpoint/allowed-actions JSON codec 迁移到 GORM，运行 codec、重启恢复和既有行兼容测试验证 JSON/NULL/UTC 不变。
- [x] 5.2 将 CreateRun、GetRun、GetCurrentWait 和 idempotency duplicate 分类迁移到 GORM，保持 owner scope 与 same-start 校验；运行 durable workflow 生命周期测试。
- [x] 5.3 将 ClaimRun、ClaimWait 和 RenewClaim 迁移到 GORM 条件更新与 `FOR UPDATE`，保留 lease/state-version/actor 校验；在真实 MySQL 增加并发 claim、lease lost 和过期 lease 接管测试。
- [x] 5.4 将 CommitExecution 的 wait resolve、新 wait、连续 node events、checkpoint 更新和 claim 清理迁移到单个 GORM transaction；用失败注入和陈旧版本测试验证全部回滚、事件无缺口且状态冲突映射不变。
- [x] 5.5 更新 Workflow MySQL 集成测试，使所有测试辅助 SQL 经 GORM Raw/Exec 执行，并运行 `go test -race ./internal/workflow ./internal/platform/mysqlstore` 验证无回归。

## 6. Memory 与 Projection 迁移

- [x] 6.1 将 Memory row、structured value、lineage、supersedes/superseded_by、expiry 和 source 映射迁移到独立 GORM model；运行 Memory round-trip、BatchGet 顺序和 owner 隔离测试。
- [x] 6.2 将 FindExact 的 ref/entity/slot/content-hash/局部 scope 动态查询迁移到 GORM scopes 或 GORM Raw，保持 selector 上限、active/expiry 过滤、稳定排序和无 selector 空结果；运行 exact recall 测试并用 MySQL EXPLAIN 验证目标复合索引可用。
- [x] 6.3 将 CommitMutation、TransitionMemory 和 idempotency event 写入迁移到 GORM transaction，显式处理零值、duplicate noop 与 RowsAffected；运行 candidate/active/rejected/revoked/expired 状态测试和重复执行测试。
- [x] 6.4 将 ActivateCandidateSuperseding 迁移到 GORM，继续按稳定 ID 顺序锁定 candidate/target，并在同一事务更新旧记忆、新记忆、relation、events 和 projection outbox；运行真实 MySQL 并发 correction 测试验证仅一个 active slot、旧记录 superseded_by 正确且不存在部分提交。
- [x] 6.5 将 ExpireDue、ClaimProjections、PendingProjectionCount、CompleteProjection 和 FailProjection 迁移到 GORM locking/条件更新；用多 worker 真实 MySQL 测试验证 `SKIP LOCKED` 无重复领取、版本化 outbox 幂等及 retry/permanent fail 行为。
- [x] 6.6 更新 MySQL-only Memory Capture E2E 的数据库辅助访问为 GORM，并验证重启恢复、精确召回、duplicate noop、用户 correction、审核编辑重判、旧候选 rejected、三条 owner-scoped outbox 和 obsolete 版本不召回。

## 7. 清理与静态门控

- [x] 7.1 删除 `rowScanner`、手工 `Scan` helper 和运行时 Repository 中直接的 `QueryContext`/`ExecContext`/`BeginTx` 调用，保留仅用于连接池生命周期的 `gormDB.DB()`；用 `rg` 静态检查和 `go vet ./...` 验证无残留双入口。
- [x] 7.2 检查所有 GORM 更新均未使用 `Save`，状态/计数/空字符串更新均显式选择字段且 owner 条件完整；增加静态门控或针对性测试并运行 `go test ./internal/platform/mysqlstore ./internal/memoryworkflow`。
- [x] 7.3 验证所有领域 Repository 编译时接口断言继续成立，启动装配无需新的必填配置，并运行 `go test ./cmd/note-agent-server ./internal/platform/httpserver`。

## 8. 完整验证与交付

- [x] 8.1 在无 DSN 环境运行 `GOCACHE=/tmp/harnessloopagent-go-cache go test ./...` 和 `go test -race ./internal/memoryworkflow ./internal/platform/httpserver ./internal/workflow`，记录结果并修复所有回归。
- [x] 8.2 使用隔离的 MySQL 8.x 全新数据库按默认并行 package 模式运行 `MYSQL_INTEGRATION_DSN=... go test -count=1 -v ./internal/memoryworkflow ./internal/platform/mysqlstore`，验证 migration、Repository、Workflow、Memory、锁并发与 EXPLAIN 全部 PASS。
- [x] 8.3 在由迁移前版本 schema 和兼容样本数据构建的数据库执行 GORM 版本读取/更新/重启回归，再用旧 `database/sql` 版本读取 GORM 写入结果，证明无需 DDL 且可回滚。
- [x] 8.4 更新数据库开发文档和验证记录，说明 GORM/Raw 边界、禁止 AutoMigrate、真实 MySQL 命令、发布与回滚步骤；运行 `git diff --check` 和 OpenSpec validation 完成验收。
