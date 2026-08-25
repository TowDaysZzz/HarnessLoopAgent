# Go Agent 服务规范 Skill 设计

## 目标

为 HarnessLoopAgent 创建一个仓库级 Codex Skill，在生成、修改、审查或迁移 Go 代码时自动加载。Skill 同时执行两类约束：严格的通用 Go 工程规范，以及本项目 Agent 服务的分层和目录规范。

它既约束新增代码，也识别现有代码中的违规。普通开发任务直接修复当前改动涉及的违规；仓库级架构整理任务扫描全部相关包，给出依赖安全的迁移顺序并完成迁移。

## 方案

采用单一入口 Skill，并通过参考文件渐进加载细节：

```text
.agents/skills/go-agent-service-standards/
├── SKILL.md
├── agents/openai.yaml
└── references/
    ├── general-go.md
    ├── agent-layout.md
    └── project-conventions.md
```

`SKILL.md` 保持简短，负责触发、范围判断、参考文件路由、迁移边界和验证要求。详细规范放入三个独立参考文件，避免每次 Go 改动都加载全部内容。

自动调用保持默认开启。触发描述覆盖本仓库 Go 代码的生成、修改、审查、重构、包拆分和 Agent 目录迁移，不吸引非 Go 或纯文档任务。

## 规范层次

### 严格通用 Go 规范

`references/general-go.md` 规定：

- 所有代码通过 `gofmt`，导入分组稳定，不使用点导入。
- 包名短小、单数、语义明确；避免 `util`、`common`、`helpers` 等无边界包。
- 导出标识符需要有用途明确的文档；命名遵循 Go 缩写习惯。
- 接口由使用方定义并保持最小；优先返回具体类型，依赖通过构造函数显式注入。
- `context.Context` 作为可能阻塞操作的首个参数，不存入结构体，不用 `context.Background()` 绕过调用方取消。
- 错误保留因果链，使用 `%w`、`errors.Is` 和 `errors.As`；稳定领域错误使用哨兵或类型，不按错误字符串分支。
- 请求路径不 `panic`；资源、goroutine、ticker、channel 和流的所有权及关闭责任必须明确。
- 并发代码必须支持取消、避免无界 fan-out 和 goroutine 泄漏，并通过竞态测试。
- 不记录 Token、Cookie、密码、API Key、原始敏感 Prompt 或跨租户数据。
- 优先可测试的小函数和表驱动测试；Mock 仅用于真实边界，领域逻辑优先 Fake 或内存实现。

### Agent 服务目录规范

`references/agent-layout.md` 定义目标边界：

| 目录 | 职责 |
|---|---|
| `cmd/<binary>` | 进程启动、信号处理、配置加载和依赖组装 |
| `internal/agent` | 与实现无关的 Agent 消息、事件和稳定端口 |
| `internal/agent/eino` | Eino 模型、Runner、Tool 和框架适配 |
| `internal/<domain>` | 领域模型、规则、服务和领域 Repository 端口 |
| `internal/<domain>workflow` | 领域工作流节点、审核与恢复流程 |
| `internal/routing` | 确定性路由和执行分发，不承载领域写入 |
| `internal/workflow` | 通用、无领域语义的工作流运行基础设施 |
| `internal/runtime` | 运行预算、观测、指标和生命周期控制 |
| `internal/platform/httpserver` | Hertz 入站适配、DTO 解析和错误映射 |
| `internal/platform/mysqlstore` | MySQL/GORM 出站适配与事务实现 |
| `internal/ragclient` | 版本化 RAG HTTP 契约和出站适配 |

领域包不得反向依赖 HTTP、MySQL、Eino 或 `cmd`。框架类型不能穿透稳定端口。`cmd` 不承载业务规则；当组装逻辑持续增长时，提取小型 assembly 函数或组件，但不创建万能容器包。

### 项目特定约束

`references/project-conventions.md` 固化当前仓库的重要不变量：

- Agent MySQL 是笔记、会话、Memory 和 Workflow 状态的事实来源；RAG 是可重建投影。
- 用户和租户边界来自服务端认证上下文，模型和客户端不能覆盖安全过滤字段。
- 外部写入使用幂等键、事务和 Outbox；重试必须有界且只用于安全操作。
- 历史笔记回答和证据治理保持 fail-closed，不能以模型常识补齐缺失证据。
- HTTP handler 只做协议适配、认证上下文提取、输入校验和错误映射。
- Eino、Hertz、GORM 和 RAG 细节留在适配层，领域服务依赖本地端口。
- 配置从 `internal/config` 进入，秘密不得进入 Tool schema、日志或持久化事件。

## 生成与迁移流程

普通 Go 任务开始时，Skill 先检查目标包、相邻实现、调用者、测试和现有 ADR。新增代码遵循目标目录；涉及的旧代码若违反规范，在同一任务中迁移并更新所有调用者。

目录迁移必须先检查导入方向、潜在导入环、公开 API、测试包名、构造函数和运行时组装。移动应按“稳定端口 → 领域逻辑 → 适配器 → 组装”顺序进行，每一步保持可编译和可测试。

如果发现与当前任务无关的大范围违规，普通任务只记录具体路径和建议，不擅自扩展修改范围。用户明确要求目录治理、架构整理或全仓迁移时，Skill 必须扫描并执行完整迁移，不得只给建议。

## 错误处理与安全

迁移不能改变 API、持久化语义、幂等性、租户隔离、Outbox 原子性、SSE 恢复语义或 fail-closed 策略，除非任务明确要求行为变化。无法证明安全的移动应停止并说明阻塞点，而不是通过复制类型、创建反向依赖或引入全局状态绕过边界。

## 验证

验证按风险递增：

1. 对所有改动的 Go 文件运行 `gofmt` 并确认无差异。
2. 运行受影响包的测试。
3. 运行 `make check`，覆盖 `go test ./...` 和 `go vet ./...`。
4. 涉及 goroutine、channel、流、缓存、租约或生命周期时运行 `make test-race`。
5. 涉及 MySQL 或 RAG 契约时，运行对应的显式集成测试；缺少外部依赖时报告未运行项，不伪造通过结果。

Skill 自身使用 `skill-creator` 的 `quick_validate.py` 验证元数据和目录结构，并通过代表性场景检查触发范围、参考文件路由、新代码生成和目录迁移决策。

## 非目标

- 不把通用 Go 教程复制进 Skill。
- 不强制所有领域采用同一个文件名或固定文件数量。
- 不为追求目录整齐而改变业务行为。
- 不自动添加与当前项目无关的框架、代码生成器或 lint 依赖。
- 不在普通局部任务中无授权重写整个仓库。
