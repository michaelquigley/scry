# scry

scry is a deliberately small status daemon for a hand-curated estate. It combines active TCP and HTTP probes with passive job check-ins, keeps transition state across restarts, renders one status page, and announces meaningful changes through Mattermost and SMTP.

The v1 daemon is complete: validated configuration, the transition engine and its durable state file, TCP and HTTP strategies on a jittered scheduler, the isolated passive-report listener, Mattermost and SMTP notifications with independent retry queues, and the status API with its embedded dashboard. The built behavior is described in [docs/current](docs/current/).

## Build

```sh
make test
make build
```

The binary installs to `$(go env GOPATH)/bin/scry`. Use `go build -tags no_ui ./cmd/scry` for a headless build. `make generate` regenerates the API server and the dashboard's client types from the committed contract in `internal/api/specs/scry.yml`.

## Configuration

Copy `scry.yaml.example` to `scry.yaml`, or pass an explicit file:

```sh
scry --config /path/to/scry.yaml
```

Configuration cascades from compiled defaults through `~/.config/scry/config.yaml`, `./scry.yaml`, and finally `--config`, with later sources taking precedence. Any malformed or invalid configuration stops boot.

## Running

The daemon binds two listeners with disjoint handler trees. `status_listen` (`0.0.0.0:8420` by default) serves the dashboard at `/` and the status document at `/api/status`, and belongs on the LAN. `ingest_listen` is constrained to loopback and serves passive reports only; remote reachability is an external reserved zrok share fronting it. [docs/current/deployment.md](docs/current/deployment.md) records the host arrangement around the daemon.
