# ADR 0001: Eino dependency baseline

- Status: Accepted
- Date: 2026-08-18

## Decision

The Agent service starts with `github.com/cloudwego/eino v0.9.9` and the OpenAI-compatible model component `github.com/cloudwego/eino-ext/components/model/openai v0.1.13`.

This combination was selected from stable versions available during the bootstrap and compiled against the required `ChatModelAgent`, `Runner`, tool, and streaming APIs. Dependencies are pinned rather than taken from a branch.

## Consequences

The independently deployed RAG service can remain on Eino v0.7.28. The services communicate through versioned HTTP contracts and do not import each other's Eino implementation packages. Future dependency upgrades require a focused compatibility PR and this ADR must be superseded.
