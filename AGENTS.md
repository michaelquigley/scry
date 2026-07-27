# AGENTS.md — scry

scry is a small status daemon for a hand-curated estate: active TCP/HTTP probes and passive job reports enter one transition engine, whose state is exposed on one page and announced through Mattermost and SMTP. The design deliberately stops before metrics, discovery, databases, and in-process overlay machinery.

## How to arrive

1. Read the newest entries in `docs/journal/` as prior-session context. The code and `docs/current/` win if they disagree.
2. Read `docs/current/` for behavior that has already landed.
3. Read `docs/future/scry-work-order.md` and its backing `scry-spec.md` for the current implementation stage and the settled design.

## Implementation posture

The work order lands in six stages. Implement one stage at a time, synthesize its built behavior into `docs/current/` and `CHANGELOG.md`, run Terminus to `clean` (up to three review rounds), then stop for Michael's review. Do not begin the next stage without that review.

The seam census in the spec is load-bearing:

- the model knows neither transports nor rendering;
- ingest and status use separate handler trees and separate listeners;
- `CheckStrategy` and `Notifier` are the only active-probe and notification facades;
- configuration and state failures are fatal, strategy failures are results, and notifier delivery retries without blocking the engine;
- the engine owns time through an injected clock; only `main` may call `time.Now()`.

## Build and test

```sh
make generate
make test
make build
```

`make build` creates the React frontend before installing the Go binary, so the embedded `ui/dist` tree always exists. `go build -tags no_ui ./cmd/scry` is the headless seam.

The API is contract-first once stage 6 lands: edit `internal/api/specs/scry.yml`, run `make generate`, and never hand-edit generated ogen or TypeScript schema files.

## Project memory

Durable knowledge about this project lives in `docs/journal/`, dated files `docs/journal/YYYY-MM-DD.md`. This is project memory; it does not go in harness-local storage (`.claude/` or equivalent), where it's invisible to every other harness and collaborator and dies with the host. Concretely: do not write to your harness's memory directory or memory tool for this project — even when the harness presents it as the default place for durable knowledge. That tool is the silo this convention exists to replace; the journal is the only durable home.

On arrival, read the most recent entries to pick up where the last session left off, before you start changing things. Treat them as prior-session context, not verified truth — if an entry conflicts with the code or a `docs/current/` doc, the code wins.

Write the smallest entry that carries the session's durable insight, and nothing more. The test for every line: *would a competent agent get this wrong, or waste time rediscovering it, working from the tree alone?* If it's recoverable by reading the code, the diff, `docs/current/`, or git history, leave it out.

That filter keeps four kinds of thing and discards the rest:

- **Decisions whose rationale isn't visible in the result** — why a value was chosen, what a line guards against, why something that looks like dead code or a no-op is load-bearing.
- **Deliberate non-actions** — a change you considered and chose not to make, so the next agent doesn't "fix" it. An unchanged file leaves no trace in a diff.
- **Couplings that span files** — two places that must move together, an ordering that matters, an assumption one file makes about another.
- **Live state** — what's unverified, unfinished, or waiting on something external.

Skip change inventories, restatements of the diff, and play-by-play of how you worked. There's no write-time approval gate; Michael reviews on commit. Append to the day's file if it exists, and write the few lines you'd want the next agent to read — honest and self-contained.

## Project rules

- Use `github.com/michaelquigley/df/dl` for logging and `github.com/michaelquigley/df/dd` for YAML/JSON binding.
- The maintainer owns commits and pushes unless explicitly requested otherwise.
- Run `unfurl -i` on every Markdown file you author or edit.
- Prefer lowercase user-facing output. Dynamic values appear in single quotes.
- Go files use mixed-case names such as `stateFile.go`; tests use `stateFile_test.go`.
- Go comments start lowercase unless their first word is an exported Go identifier.
- Never leave generated binaries or test artifacts in the repository.
- Never use emoji.
