---
title: dns serial canary
state: researching
created: 2026-10-10
tags: [feature, spike]
milestone: v0.1.x
source: grimoire recipes/infrastructure/linode-slave-zone-stale-diagnosis.md; recipes/infrastructure/frame-subscription-capture-deployment.md (monitoring section)
---

Watch each zone the practice serves from its hidden master (two.quigley.com, BIND) for the slaves falling silently behind: compare the SOA serial on the master against every public nameserver in the zone's NS set, and raise a transition when the master stays ahead past a short grace period. Decide where the compare belongs — a third active strategy beside `tcp` and `http` (scry has no DNS dialer today), or a cron job on the master posting passive reports through ingest — and whether one check covers a zone or one per zone per nameserver. Pin the NS lookup to an external resolver; a canary that cannot see the NS set must report failure, not nothing.

## why

Linode's secondary DNS is two-stage (ingest fleet that AXFRs, then an internal push to the Akamai anycast edge) and the edge does not honor SOA expire. A wedged slave entry therefore serves a frozen copy indefinitely with no visible failure: metawoo.com was stale for seven months in 2026 before a DNS change exposed it. A rarely-changing zone gives no other signal when transfers die. The serial compare is the one command that localized that incident; standing it up as a monitor was planned into the subscription capture deployment alongside the healthz probe, which scry now runs. This card is the half still unbuilt.

## background

Legitimate changes converge unevenly across the anycast nodes for a few minutes, so a single divergent reading is not an incident; the check should tolerate one and alert on the next. Master ahead of a slave for longer than that is a transfer problem, never propagation lag, and waiting does not converge it. The repair recorded in the playbook (delete and re-create the slave entry in Linode's Domains manager) carries a real outage window, so the notification should carry the zone and the lagging nameserver, not just a state flip.
