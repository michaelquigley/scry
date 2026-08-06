# Dashboard

The dashboard is a single page built into the binary and served from the status listener's root. It answers one question without a click: is everything okay, and if not, what isn't. There is no router, no navigation, and no charts.

The page speaks the house visual language: light mode on cool paper, Source Serif 4 for language (title, check names) and Source Code Pro for machine strings (ids, states, timestamps, durations, detail), with soft state-colored chips. Both fonts are bundled through fontsource and served from the embedded assets — the page loads nothing remote.

A rollup banner leads. With nothing wrong it reads `all clear` with the check count; otherwise it reads `N late / N failed` and takes the color of the worse state. Below it, every check appears in one table: state chip, name with its id and kind, time in the current state, last transition, and last-result detail. Absent values render as a dash. Every timestamp carries its age — the last-transition column measured against the document's own `generated` stamp (the daemon's arithmetic, immune to browser clock skew), and the header's "as of" stamp measured against the browser's latest poll attempt, so a stale page shows its staleness growing.

Trouble sorts first — failed, then late, then ok — and within a state group alphabetically by id, so the order is stable between polls and the all-green page reads as a scannable registry. The API returns registry order and the page decides how to present it.

Time in state is measured against the document's own `generated` stamp rather than the browser's clock, so the page reports the daemon's arithmetic. Timestamps are rendered in the viewer's local zone.

## Polling and Staleness

The page polls `/api/status` every 10 seconds. A failed poll never discards the last good document: the page keeps rendering the estate it last knew and adds a banner saying the data is stale. Time in state therefore freezes at the last successful poll, which is the honest reading — the page shows the last status it actually received.

## Build

The UI is Vite, React 19, and TypeScript, built into `ui/dist` and embedded through `//go:embed all:dist`. `make build` builds the frontend first so the embedded tree always exists.

The dashboard's own client types are generated from the committed OpenAPI contract; see [Status API](status-api.md) for the generation workflow.

For frontend work, `npm --prefix ui run dev` serves the page on port 5173 and proxies `/api` to a daemon running on `127.0.0.1:8420`.

`go build -tags no_ui ./cmd/scry` produces a headless binary. Its embedded tree is empty, so the status API serves normally and every dashboard path answers 404 rather than a partially rendered page.

## Asset Routing

Only the root path and files that exist in the embedded build are served. There is deliberately no single-page fallback: with no router, an unknown path is a genuine 404 rather than an index page. That is also what keeps the status listener from answering for `/report/*`, which belongs to the ingest listener alone.
