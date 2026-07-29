---
title: mcp status reader
state: inbox
created: 2026-07-29
tags: [feature]
source: docs/future/scry-spec.md (retired at v1 close-out)
---

An MCP tool for the gang that reads scry's status: a thin consumer of `GET /api/status`, surfacing the rollup and per-check states so agents can answer "is the estate okay, and if not, what isn't" without scraping the dashboard. The committed OpenAPI document (`internal/api/specs/scry.yml`) is the contract; evolution stays additive.

## why

Deferred until the JSON API has stabilized under real use. The API it consumes shipped in v1 and is deliberately the single walk of the model — this card is a reader, never a second render path.
