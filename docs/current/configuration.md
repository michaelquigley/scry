# Configuration

Stage 1 establishes scry's executable and configuration boundary. The binary currently validates the complete v1 configuration and exits; the engine and long-running daemon behavior land in the later stages of the active work order.

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

## Command surface

```sh
scry [--config path] [--verbose]
scry version
```

Verbose mode reinitializes df logging at debug level. Version output is supplied by `push/build`.
