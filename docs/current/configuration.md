# Configuration

Scry validates the complete v1 configuration, reconciles and persists the state file, then runs active checks, the passive-report ingest listener, configured notification workers, and the status listener until SIGINT or SIGTERM.

## Cascade

Configuration is merged from lowest to highest priority:

1. compiled defaults;
2. `~/.config/scry/config.yaml` (or `$XDG_CONFIG_HOME/scry/config.yaml`);
3. `./scry.yaml`;
4. the path supplied by `--config`.

The first two file layers are optional. A path explicitly supplied with `--config` must exist. After the merge, the document is validated as one configuration; any error stops boot.

Compiled defaults:

```yaml
status_listen: "0.0.0.0:8420"
ingest_listen: "127.0.0.1:8421"
state_file: "~/.local/state/scry/state.json"
defaults:
  interval: 60s
  timeout: 10s
  fail_after: 3
  harden_after: 3
```

`state_file` follows `$XDG_STATE_HOME` when set. Duration values use Go duration strings such as `30s`, `5m`, and `24h`.

## Registry

Each check has a lowercase slug id, a display name, and exactly one strategy block:

- `passive` requires a positive `period`, positive `grace`, and a non-empty token unique among passive checks;
- `http` requires an absolute `http` or `https` URL and optional expected status codes in the range 100–599;
- `tcp` requires a host and numeric port.

Active checks inherit `interval`, `timeout`, and `fail_after`; passive checks inherit `harden_after`. Per-check fields override the global defaults. An omitted override inherits, while an explicitly authored zero or out-of-range value is invalid.

The ingest listener is constrained to a loopback address because external exposure belongs to the reserved zrok share in the deployment design. Listener ports and TCP target ports must be numeric and lie between 1 and 65535.

Passive tokens authenticate `GET|POST /report/<check-id>` on that isolated listener. Active checks have no ingest endpoint.

## Notifiers

Every notification block is optional. When present, each announced transition fans out to each configured destination:

```yaml
notifiers:
  mattermost:
    url: "https://mattermost.example.com"
    channel_id: "replace-with-channel-id"
    token_env: "SCRY_MATTERMOST_TOKEN"
  smtp:
    host: "smtp.example.com"
    port: 25
    from: "scry@example.com"
    to:
      - "operator@example.com"
  sendmail:
    from: "scry@example.com"
    to:
      - "operator@example.com"
    # path: "/usr/bin/sendmail"   # default
```

Mattermost uses a bot account to post into one channel. `url` must be an absolute HTTP or HTTPS server URL, and `channel_id` must be non-empty. The token is resolved from the environment variable named by `token_env`, falling back to an inline `token` value when that variable is unset or empty; environment wins when both are populated. The resolved token must be non-empty at boot. Supplying it through the service environment is the recommended production shape.

SMTP requires a non-empty relay host, a port from 1 through 65535, one valid sender address, and at least one valid recipient address. Display-name forms such as `"Scry <scry@example.com>"` are accepted.

SMTP is intentionally the unauthenticated house-relay shape: there are no credential fields. Scry upgrades with STARTTLS when the relay advertises it and verifies the relay certificate; otherwise it continues on the existing connection. See [Notifications](notifications.md) for delivery and retry behavior.

Sendmail delivers through the host MTA's `sendmail` binary instead of a direct relay, and is usually configured *instead of* `smtp`, not alongside it (both configured means two emails per announcement). It requires one valid sender address and at least one valid recipient; `path` is optional and defaults to `/usr/bin/sendmail`. There are no host, port, or credential fields — the MTA owns transport, queueing, and retry. At boot, scry verifies the binary exists and is executable by its own user; a host without a configured MTA fails loudly at startup. See [Notifications](notifications.md) for delivery behavior and [Deployment](deployment.md) for why HQ prefers this shape.

## Command surface

```sh
scry [--config path] [--verbose]
scry version
```

Verbose mode reinitializes df logging at debug level. Version output is supplied by `push/build`.
