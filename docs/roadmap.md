# Note Agent implementation roadmap

## Architecture decisions

- The Note Agent owns original notes, revisions, conversations, agent runs, SSE events, long-term memories, and insights.
- RAG RetrievalOps owns chunking, embeddings, Milvus indexing, retrieval, reranking, citations, and retrieval evaluation.
- The Note Agent calls RAG through versioned HTTP APIs. It does not use MCP or access Milvus directly.
- Agent MySQL is the source of truth. RAG data is a rebuildable search projection.
- Explicit note writes use a deterministic note service; date and tag queries use SQL; semantic questions use RAG; insight features use Eino Compose; open-ended tasks use an Eino Agent.

## Delivery phases

1. Bootstrap the empty repository with a minimal Eino model, tool, streaming runner, configuration, health server, tests, and container image.
2. Add a typed RAG HTTP client and the `semantic_search_notes` Eino tool; first use a fake RAG server, then connect to `POST /v1/retrieve`.
3. Add conversations, agent runs, durable run events, and resumable SSE using monotonically increasing event sequence numbers.
4. Add note CRUD, revisions, transactional outbox, and structured RAG indexing with version-aware upsert and delete.
5. Add deterministic intent routing, bounded context compaction, tool policies, and auditable agent runs.
6. Add candidate/active/rejected/expired long-term memory with evidence, confirmation, expiry, and revocation.
7. Add separate Eino Compose workflows for yesterday review, yesterday dream, on this day, daily novelty, happiness pill, and behavior insight.
8. Add service JWT identity propagation, tenant/subject isolation, tracing, scripted evaluations, load tests, and staged rollout.

## Required RAG contracts

The RAG service should eventually expose `PUT /v1/index/documents/{external_id}`, `GET /v1/index/jobs/{job_id}`, `DELETE /v1/index/documents/{external_id}`, and the existing `POST /v1/retrieve`. Structured note writes include `external_id`, monotonic `external_version`, `document_type=note`, occurrence time, tags, and source metadata. Stale versions must be rejected.

User-triggered retrieval must carry server-issued tenant and subject identity. RAG must build tenant, subject, KB, and deleted filters itself instead of trusting model-generated request fields.

## Commit order

`agent/bootstrap-eino` -> `agent/rag-client-tool` -> `agent/chat-sse` -> `agent/note-domain` -> `rag/structured-index-api` -> `agent/outbox-indexing` -> `agent/memory` -> `agent/insight-workflows` -> `security-evals-rollout`.

Database, Redis, public chat/SSE endpoints, memory, insight workflows, multi-agent orchestration, MCP, and UI are intentionally deferred from the bootstrap milestone.
