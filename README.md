# HarnessLoopAgent

An Eino-based personal note agent. The service owns notes, conversations, long-term memory, insight workflows, and client streaming. Retrieval is provided by a separately deployed RAG service over HTTP.

## Current milestone

The repository currently contains only the Phase 1 bootstrap: an Eino `ChatModelAgent`, an ADK streaming runner, a deterministic echo tool, configuration validation, and health endpoints. It intentionally does not include a database, RAG integration, network SSE, memory, or insight workflows yet.

## Requirements

- Go 1.25+
- An OpenAI-compatible chat model that supports tool calling

## Run locally

Set the values shown in `.env.example`, then run:

```bash
export MODEL_NAME=your-model
export MODEL_API_KEY=your-key
export MODEL_BASE_URL=https://api.openai.com/v1
go run ./cmd/note-agent-server
```

Check the process:

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

## Verify

```bash
make check
make docker-build
```

The planned service boundaries and subsequent milestones are recorded in `docs/roadmap.md`.
