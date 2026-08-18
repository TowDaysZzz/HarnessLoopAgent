# HarnessLoopAgent

An Eino-based personal note agent. The service owns notes, conversations, long-term memory, insight workflows, and client streaming. Retrieval is provided by a separately deployed RAG service over HTTP.

## Current milestone

The repository currently contains only the Phase 1 bootstrap: an Eino `ChatModelAgent`, an ADK streaming runner, a deterministic echo tool, configuration validation, and health endpoints. It intentionally does not include a database, RAG integration, network SSE, memory, or insight workflows yet.

## Requirements

- Go 1.25+
- An OpenAI-compatible chat model that supports tool calling

## Configure and run locally

Create the local configuration file and fill in `MODEL_NAME` and `MODEL_API_KEY`:

```bash
cp config.example.yaml config.yaml
go run ./cmd/note-agent-server
```

`config.yaml` is ignored by Git and excluded from Docker builds because it can contain a secret. Set `CONFIG_FILE` to use another YAML file. Environment variables with the same names override YAML values, which is useful for containers and secret managers.

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
