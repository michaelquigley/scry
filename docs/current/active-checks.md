# Active Checks

Scry runs each configured HTTP or TCP check from its own scheduler goroutine. Every check receives one random initial offset in the half-open range `[0, interval)`, then probes on a fixed ticker at its configured interval. The spread prevents a large registry from firing as one startup volley.

Each probe runs beneath a per-check context deadline. The strategy returns a `Result` rather than an operational error: inability to connect, request, or complete by the deadline is a failed health result. If daemon cancellation interrupts an in-flight probe, the scheduler discards that result because shutdown says nothing about the target's health. A probe deadline reached while the daemon remains live is delivered as a failed result.

All active workers deliver `(check id, result)` through one scheduler result channel. The scheduler submits those results to the engine's serialized command loop. It also sends a bare passive-sweep command every 15 seconds and flushes dirty, non-transitioning active state every 60 seconds. Passive window state is derived atomically inside the engine rather than in the scheduler.

## TCP

A TCP check performs a context-bound dial to the configured host and numeric port. An accepted connection is immediately closed and produces `ok`; a dial failure produces `failed` with the dial error as detail.

```yaml
checks:
  - id: ssh
    name: studio host ssh
    tcp:
      address: "192.0.2.10:22"
    interval: 30s
    timeout: 3s
```

## HTTP

An HTTP check performs a GET and judges the first response without following redirects. With no `expect` list, every 2xx response is `ok`. When `expect` is present, only the listed status codes are `ok`; this can deliberately accept a redirect or error status. Any other response produces `failed` with its status code in the detail.

By default the probe dials the URL's host and port. An optional `address` overrides only the dial target with a `host:port` value: the connection lands on that address while the URL's host still identifies the listener — it is the request's `Host` header and, over TLS, the server name the certificate is checked against. An explicit `address` bypasses proxy environment variables, because a proxy in between would defeat the override. That is the shape for a name that resolves differently inside the monitoring network: the URL carries the name and `address` carries the external endpoint, so a probe run from inside the network speaks to the listener an outside user reaches.

TLS certificates are verified by default. `insecure: true` is an explicit per-check escape hatch for self-signed estate endpoints.

```yaml
checks:
  - id: homepage
    name: homepage
    http:
      url: "https://example.test/health"
      expect: [200, 204]
      insecure: false
    interval: 60s
    timeout: 10s
  - id: chat-external
    name: chat external listener
    http:
      url: "https://chat.quigley.com/health"
      address: "203.0.113.40:443"
    interval: 60s
    timeout: 10s
```
