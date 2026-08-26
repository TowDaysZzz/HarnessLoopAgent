# Go Agent Service Standards Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an automatically discoverable repository Skill that enforces strict Go engineering rules and HarnessLoopAgent Agent-service package boundaries for generation, review, and migration work.

**Architecture:** Keep a concise `SKILL.md` as the trigger and router. Put general Go rules, Agent package boundaries, and repository invariants in three focused references; retain default implicit invocation through `agents/openai.yaml` without an explicit-only policy.

**Tech Stack:** Codex Agent Skills, Markdown, YAML, Python validation helpers bundled with `skill-creator`, Go 1.25 project conventions.

---

### Task 1: Establish failing structural checks and initialize the Skill

**Files:**
- Create: `.agents/skills/go-agent-service-standards/SKILL.md`
- Create: `.agents/skills/go-agent-service-standards/agents/openai.yaml`
- Create: `.agents/skills/go-agent-service-standards/references/general-go.md`
- Create: `.agents/skills/go-agent-service-standards/references/agent-layout.md`
- Create: `.agents/skills/go-agent-service-standards/references/project-conventions.md`

- [ ] **Step 1: Run the structural checks before creation**

Run:

```bash
test -f .agents/skills/go-agent-service-standards/SKILL.md \
  && test -f .agents/skills/go-agent-service-standards/references/general-go.md \
  && test -f .agents/skills/go-agent-service-standards/references/agent-layout.md \
  && test -f .agents/skills/go-agent-service-standards/references/project-conventions.md
```

Expected: FAIL because the new Skill does not exist yet.

- [ ] **Step 2: Read the UI metadata format before generating it**

Run:

```bash
sed -n '1,260p' /Users/xuanzai/.codex/skills/.system/skill-creator/references/openai_yaml.md
```

Expected: metadata requirements for `display_name`, `short_description`, and `default_prompt` are available.

- [ ] **Step 3: Initialize the Skill with only the required reference resource directory**

Run:

```bash
python3 /Users/xuanzai/.codex/skills/.system/skill-creator/scripts/init_skill.py \
  go-agent-service-standards \
  --path .agents/skills \
  --resources references \
  --interface 'display_name=Go Agent Service Standards' \
  --interface 'short_description=Enforce Go and Agent service conventions' \
  --interface 'default_prompt=Apply the repository Go and Agent service standards to this task.'
```

Expected: the Skill directory, starter `SKILL.md`, `agents/openai.yaml`, and `references/` are created. No `policy.allow_implicit_invocation: false` entry is present.

### Task 2: Write the trigger and routing entrypoint

**Files:**
- Modify: `.agents/skills/go-agent-service-standards/SKILL.md`
- Verify: `.agents/skills/go-agent-service-standards/agents/openai.yaml`

- [ ] **Step 1: Replace the starter with a concise automatic trigger**

The frontmatter must be:

```yaml
---
name: go-agent-service-standards
description: Use when generating, modifying, reviewing, or reorganizing Go code in HarnessLoopAgent, especially Agent ports, Eino adapters, domain services, workflows, routing, HTTP, persistence, concurrency, and tests.
---
```

The body must require:

1. Inspect the target package, callers, tests, relevant ADRs, and neighboring patterns before editing.
2. Always read `references/general-go.md` and `references/project-conventions.md` for Go code work.
3. Also read `references/agent-layout.md` for package placement, dependency direction, `cmd`, Agent, Eino, workflow, routing, runtime, HTTP, MySQL, or RAG work.
4. Conform all new code and touched legacy code. For an explicit repository architecture task, scan and migrate all violations; for unrelated violations, report exact paths without silently expanding scope.
5. Preserve APIs and repository safety invariants unless the task explicitly changes behavior.
6. Format and run targeted tests, `make check`, and risk-based race/integration tests before completion.

- [ ] **Step 2: Verify automatic invocation remains enabled**

Run:

```bash
rg -n 'allow_implicit_invocation|display_name|short_description|default_prompt' \
  .agents/skills/go-agent-service-standards/agents/openai.yaml
```

Expected: interface fields are present and `allow_implicit_invocation: false` is absent.

### Task 3: Write the strict general Go reference

**Files:**
- Create: `.agents/skills/go-agent-service-standards/references/general-go.md`

- [ ] **Step 1: Document decision-changing Go rules**

Write focused sections covering:

- formatting, imports, package names, exported APIs, initialisms, and receiver names;
- small consumer-owned interfaces, concrete return types, constructors, and dependency injection;
- `context.Context` propagation, deadlines, cancellation, and prohibition on storing contexts;
- `%w`, `errors.Is`, `errors.As`, typed/sentinel domain errors, actionable error context, and no string branching;
- explicit ownership for goroutines, channels, streams, tickers, timers, response bodies, and closers;
- bounded concurrency, cancellation-aware sends, no goroutine leaks, and race-test requirements;
- zero-value and nil semantics, copying of mutable slices/maps at trust boundaries, and immutable request inputs where practical;
- structured logging without secrets or cross-tenant payloads;
- table-driven behavior tests, real boundaries, limited mocks, deterministic time/randomness, and explicit integration-test gates.

Include a quick-reference table mapping each concern to the required decision and a common-mistakes section. Do not reproduce a generic Go tutorial or prescribe arbitrary file-size limits.

### Task 4: Write the Agent package layout reference

**Files:**
- Create: `.agents/skills/go-agent-service-standards/references/agent-layout.md`

- [ ] **Step 1: Define package ownership and dependency direction**

Document this target map:

```text
cmd/<binary>                  process bootstrap and dependency assembly
internal/agent               framework-neutral messages, events, and stable ports
internal/agent/eino          Eino model, runner, tool, and framework adapters
internal/<domain>            domain model, rules, services, repository ports
internal/<domain>workflow    domain workflow nodes, review, resume, orchestration
internal/routing             deterministic classification and dispatch
internal/workflow            domain-neutral workflow runtime primitives
internal/runtime             budgets, lifecycle, observation, metrics
internal/platform/httpserver Hertz inbound protocol adapter
internal/platform/mysqlstore MySQL/GORM outbound adapter and transactions
internal/ragclient           versioned RAG HTTP outbound contract
```

Require domain packages to avoid dependencies on Hertz, GORM, Eino, `cmd`, and concrete stores. Require framework types to stop at adapter boundaries. Keep `cmd` free of domain rules and extract focused assembly helpers as bootstrap grows.

- [ ] **Step 2: Define safe migration behavior**

Require inspection of imports, cycles, exported APIs, tests, constructors, and bootstrap wiring before moving files. Migrate in the order stable ports → domain logic → adapters → assembly, keeping each step compiling. Explicit architecture tasks must execute the complete migration; ordinary tasks migrate touched violations and report unrelated ones.

### Task 5: Write HarnessLoopAgent-specific invariants

**Files:**
- Create: `.agents/skills/go-agent-service-standards/references/project-conventions.md`

- [ ] **Step 1: Capture repository decisions that generated code must preserve**

Document:

- Agent MySQL is the source of truth; RAG data is a rebuildable projection.
- Tenant, user, knowledge-base, and authorization scope come from trusted server context, never model-controlled fields.
- External mutations use idempotency, transactions, Outbox boundaries, and bounded retry only when safe.
- Historical-note grounding is fail-closed; missing or invalid evidence cannot be filled from model knowledge.
- Hertz handlers perform protocol adaptation, authentication extraction, validation, and error mapping only.
- Eino, Hertz, GORM, and RAG implementation details remain in adapters; domain services depend on local ports.
- Configuration flows from `internal/config`; secrets never enter Tool schemas, logs, or persistent events.
- Preserve SSE resume semantics, monotonic event ordering, cancellation behavior, and one-active-run rules.

Link to the relevant existing ADR paths rather than duplicating their full text.

### Task 6: Validate structure, content, and repository cleanliness

**Files:**
- Validate: `.agents/skills/go-agent-service-standards/**`
- Preserve: all unrelated working-tree changes

- [ ] **Step 1: Run the official Skill validator**

Run:

```bash
python3 /Users/xuanzai/.codex/skills/.system/skill-creator/scripts/quick_validate.py \
  .agents/skills/go-agent-service-standards
```

Expected: validation succeeds with no frontmatter, naming, or placeholder errors.

- [ ] **Step 2: Check required routing and invariants**

Run:

```bash
rg -n 'references/(general-go|agent-layout|project-conventions)\.md|make check|make test-race' \
  .agents/skills/go-agent-service-standards/SKILL.md
rg -n 'context\.Context|errors\.Is|errors\.As|goroutine|gofmt|go vet|race' \
  .agents/skills/go-agent-service-standards/references/general-go.md
rg -n 'internal/agent/eino|internal/platform/httpserver|internal/platform/mysqlstore|import cycle' \
  .agents/skills/go-agent-service-standards/references/agent-layout.md
rg -n 'source of truth|fail-closed|Outbox|tenant|SSE' \
  .agents/skills/go-agent-service-standards/references/project-conventions.md
```

Expected: every command finds the required decision-changing content.

- [ ] **Step 3: Scan for unfinished scaffold text and formatting errors**

Run:

```bash
rg -n 'TBD|TODO|FIXME|Replace this|Example resource' \
  .agents/skills/go-agent-service-standards && exit 1 || true
git diff --check -- .agents/skills/go-agent-service-standards
```

Expected: no unfinished scaffold matches and no whitespace errors.

- [ ] **Step 4: Review only the intended changes**

Run:

```bash
git status --short -- .agents/skills/go-agent-service-standards \
  docs/superpowers/specs/2026-08-25-go-agent-service-standards-design.md \
  docs/superpowers/plans/2026-08-25-go-agent-service-standards.md
```

Expected: the new Skill and plan are visible; unrelated worktree changes remain untouched.
