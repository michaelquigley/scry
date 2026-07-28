# CHANGELOG

## Unreleased

FEATURE: the stage-two state core — transport-free check, result, record, and transition types; exact passive-window and active-damping rules; paired notification decisions; a single-owner engine loop with copied registry-ordered snapshots; and a strict, versioned JSON state file replaced atomically. Boot reconciliation prunes removed checks, resets kind changes, stamps complete registration baselines, persists before startup succeeds, and resumes existing failures without re-firing; reports and transitions save immediately, ordinary active-result mutations ride explicit, shutdown, and future periodic flushes, and every persistence failure stops the engine.

FEATURE: the stage-one scry skeleton — a Go 1.26 cobra binary with push-backed version reporting, df logging, a React 19/Vite/TypeScript embed seam, and a fail-fast dd configuration cascade. The registry accepts exactly one passive, HTTP, or TCP strategy per check; validates identifiers, tokens, timing thresholds, listener and target addresses, expected HTTP codes, and notifier shapes; and keeps explicit invalid zero overrides distinct from omitted values that inherit defaults.
