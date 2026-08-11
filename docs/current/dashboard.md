# Dashboard

The dashboard is a single page built into the binary and served from the status listener's root. It answers one question without a click: is everything okay, and if not, what isn't. There is no router, no navigation, and no charts.

The page speaks the house visual language: light mode on cool paper, Source Serif 4 for language (title, check names) and Source Code Pro for machine strings (ids, states, timestamps, durations, detail), with soft state-colored chips. Both fonts are bundled through fontsource and served from the embedded assets — the page loads nothing remote. Scry's identity follows the metawoo software-products pack: an Archivo Black wordmark baked to SVG paths in scry's sky-900 accent (`#0C4A6E` — reef below, flo the water, scry the sky), and a scrying-orb favicon (accent disc, paper gleam) — a sanctioned divergence from the identity pack's letter-tile spec, because a scrying glass names the tool better than a letter. The footer pairs the wordmark with the metawoo mark and links the repository, mirroring reef and flo.

The header shows the configured `estate_name` (the browser tab title follows it), so the page names what it watches rather than the tool watching it. A rollup banner leads. With nothing wrong it reads `all clear` with the check count; otherwise it reads `N late / N failed` and takes the color of the worse state. Below it, every check appears in one table: state chip, name with its id and kind, time in the current state, last transition, and last-result detail. Absent values render as a dash. Every timestamp carries its age, and every age — including the in-state column and the header's "as of" — counts live on a one-second client-side pulse. An age is the daemon's own span (timestamp to the document's `generated` stamp) plus the locally elapsed time since that document arrived, each term in its own clock frame, so browser and daemon clocks never mix and a stale page shows its staleness growing in real time.

Trouble sorts first — failed, then late, then ok — and within a state group alphabetically by id, so the order is stable between polls and the all-green page reads as a scannable registry. The API returns registry order and the page decides how to present it.

Time in state is measured against the document's own `generated` stamp rather than the browser's clock, so the page reports the daemon's arithmetic. Timestamps are rendered in the viewer's local zone.

## The State Strip

Beneath each check's row runs a thin band strip: the classic status-page strip, rendered from exact intervals rather than sampled buckets. The window is a fixed 90 days, matching the API default. Bands take the state colors from the house palette; a check's time before it existed is left as bare track, and time no daemon can testify to takes a visually distinct hatch that reads as *absence* rather than health or trouble. Where an unwatched span overlaps a not-yet-registered one, unwatched wins: a daemon that cannot testify claims neither a state nor nonexistence.

Every endpoint is exactly proportional. A short non-ok band or gap is never widened to be visible — an instrument does not round up — so anything a reader must not miss instead gets a fixed-width marker at its true position. A thirty-minute overnight failure is 0.02% of ninety days, and it stays findable without lying about how long it lasted. Every band carries its state, duration, and start on hover.

The strip renders from the history document alone. The status document contributes exactly two things: the display clock and the refetch trigger. That is what keeps two independently captured documents from disagreeing inside one strip.

The window slides with the page's one live clock, the same arithmetic the header already does: the right edge is the latest status document's `generated` plus its locally elapsed age, the left edge is 90 days before that, every interval is clipped to those bounds, and the tail band extends to the right edge. Old incidents age out the left side as the window advances, and the two clock frames never mix.

The strip inherits the page's stale honesty at both ends. It can only speak for the span its history document covers, so anything outside `[from, to]` renders unwatched — including the sliver before the document's own window opens, since the two documents are fetched in sequence and their windows do not align exactly. While status polling is failing, or while the page knows its history document is behind, bands cap at the last instant it can vouch for and the remainder to the right edge takes the unwatched treatment, growing visibly with the outage. When polling recovers, that span either vanishes (a network blip; the daemon was fine all along) or a changed `started` triggers the refetch that renders the real recorded gap.

## Polling and Staleness

The page polls `/api/status` every 10 seconds. A failed poll never discards the last good document: the page keeps rendering the estate it last knew and adds a banner saying the data is stale. Time in state therefore freezes at the last successful poll, which is the honest reading — the page shows the last status it actually received.

History rides that same cadence under one level-triggered dirty flag, so no second timer exists and a quiet estate refetches never. The flag is set when a status poll shows a transition newer than the held history document's `generated`, when it shows a `started` the document was not fetched under (a restart writes its gap into the ledger without producing any transition), when a check appears that the document does not carry, and by any failed history fetch — the missing document at first load being the same flag rather than a special case. It clears only on a fetch that lands, and every successful status poll retries while it is set.

The flag is committed before the request rather than after it, so the page never paints confident state across a span it has already decided is obsolete. Because polls are not serialized, two history fetches can be in flight at once: responses are ordered by each document's own `generated` watermark, and an arriving document is revalidated against the newest status before the flag is cleared, so a response that lost the race can neither replace a newer document nor clear a flag it does not answer for.

## Build

The UI is Vite, React 19, and TypeScript, built into `ui/dist` and embedded through `//go:embed all:dist`. `make build` builds the frontend first so the embedded tree always exists.

`npm --prefix ui run test` runs vitest over the page's pure arithmetic: the interval builder's acceptance table, strip placement and marker selection, the display-window derivation, response ordering, and the dirty flag's lifecycle. Vitest is a dev dependency only, and the strip is hand-rolled markup — the embedded page still loads nothing remote. CI runs the suite beside the frontend build.

The dashboard's own client types are generated from the committed OpenAPI contract; see [Status API](status-api.md) for the generation workflow.

For frontend work, `npm --prefix ui run dev` serves the page on port 5173 and proxies `/api` to a daemon running on `127.0.0.1:8420`.

`go build -tags no_ui ./cmd/scry` produces a headless binary. Its embedded tree is empty, so the status API serves normally and every dashboard path answers 404 rather than a partially rendered page.

## Asset Routing

Only the root path and files that exist in the embedded build are served. There is deliberately no single-page fallback: with no router, an unknown path is a genuine 404 rather than an index page. That is also what keeps the status listener from answering for `/report/*`, which belongs to the ingest listener alone.
