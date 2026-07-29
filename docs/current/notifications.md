# Notifications

Scry sends every announced state transition to every configured notifier. The model decides whether a transition is announced; the notifier layer only formats and delivers it. Active checks therefore pass through `late` silently, while passive lateness, every transition to `failed`, and the paired recoveries are delivered.

Both transports share the same content:

```text
[scry] nas-snapshot: late → failed

- name: NAS nightly snapshot
- id: nas-snapshot
- state: late → failed
- time in previous state: 6h0m0s
- detail: snapshot exited 2
- timestamp: 2026-07-28T09:30:00-04:00
```

The first line is also the SMTP subject. A transition without last-result detail renders `(none)`.

## Delivery

The engine saves a transition before enqueueing its notification. Enqueue copies the transition into one unbounded in-memory FIFO for each configured destination and returns without doing network work. One worker per notifier preserves that destination's order, while a stalled destination cannot hold up another destination or the engine.

Each delivery attempt has a 30-second deadline. A failed message is attempted five times, with waits of 15 seconds, 1 minute, 3 minutes, and 6 minutes between attempts. After the fifth failure, Scry logs the drop and advances to the next queued transition. Queues are intentionally memory-only; undelivered messages do not survive a daemon restart, while the persisted state file and eventual status page remain authoritative.

## Mattermost

Mattermost delivery uses a bot account through the shared `theharnessbody/mattermost` client. Scry constructs the client once and posts the shared message to the configured channel through `/api/v4/posts`; it never starts the client's command listener, resolves bot identity, or opens a websocket.

The bot token is read environment-first through `token_env`, with inline `token` as a fallback. Configuration validation requires the resolved token before the daemon starts.

The shared client's posting call has a fixed 10-second HTTP timeout, tighter than the dispatcher's 30-second attempt deadline. A canceled attempt is rejected before posting begins; once a post is in flight, the shared client's timeout supplies the finite bound. A non-2xx response is a delivery error and enters the normal ordered retry path.

## SMTP

SMTP delivery uses the standard-library client with the configured envelope sender and recipients. It is designed for an unauthenticated house relay. When the relay advertises STARTTLS, Scry upgrades and verifies the certificate using the configured relay host; when STARTTLS is absent, it sends over the existing relay connection. No SMTP credentials or insecure-TLS escape hatch are part of the v1 configuration.
