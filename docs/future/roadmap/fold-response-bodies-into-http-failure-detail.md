---
title: fold response bodies into http failure detail
state: horizon
created: 2026-08-20
tags: [enhancement]
source: archive docs/journal/2026-08-20.md (health check endpoints session)
---

The HTTP strategy closes the response body unread (`internal/strategy/http.go`), so a failed probe's detail is only `http status <code>`. The estate's daemons now answer assessed health endpoints (reef server and flo service, archive repo) whose `503` bodies name the failing check as small JSON: `{"healthy": false, "checks": [{"name": "store", "ok": false}, ...]}`.

On a non-accepted response, read a capped slice of the body (a KB is plenty) and fold it into `Result.Detail` beside the status code, so the status page says which check failed instead of only the code. Keep the accepted path body-free; an empty, oversized, or non-JSON body degrades to the current status-only detail.
