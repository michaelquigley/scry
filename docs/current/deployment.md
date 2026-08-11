# Deployment

This page records the host arrangement scry expects at HQ. Scry provisions none of it: it does not write systemd units, mint tokens, reserve zrok shares, create Mattermost accounts, or edit crontabs. The daemon's whole job is to read a configuration file, bind two listeners, and run. Everything below is host work done once, by hand, around it.

Nothing on this page has been executed yet; it is the bridge between the built daemon and the first real deployment.

## The Daemon Unit

Scry runs as a normal system service reading `/etc/scry/scry.yaml`:

```ini
[Unit]
Description=scry status daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/scry --config /etc/scry/scry.yaml
EnvironmentFile=/etc/scry/scry.env
StateDirectory=scry
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

`StateDirectory=scry` gives the service `/var/lib/scry`, which is where both durable paths should point under systemd:

```yaml
state_file: "/var/lib/scry/state.json"
history_dir: "/var/lib/scry/history"
```

The defaults under `~/.local/state/scry` are a workstation convenience and should be set explicitly here. Scry treats a failed state save as fatal, and history shares that law — a ledger it cannot read whole at boot or append to at runtime stops the daemon — so either path the service cannot write is a boot failure rather than a silent divergence.

The Mattermost bot token arrives through the environment file rather than the config file:

```sh
# /etc/scry/scry.env, mode 0600
SCRY_MATTERMOST_TOKEN=...
```

The config names that variable with `token_env: "SCRY_MATTERMOST_TOKEN"`. An inline `token:` is supported as a fallback, but the environment is the production shape.

For mail, prefer the `sendmail` notifier over the direct `smtp` block on a host whose MTA is already configured (the HQ host qualifies): the MTA's own queue carries deliveries through relay outages, and no mail credential enters scry's configuration at all. Scry verifies the configured binary (default `/usr/bin/sendmail`) exists at boot, so a host missing an MTA fails loudly rather than dropping mail silently.

## The Mattermost Bot

Create a bot account in Mattermost, invite it to the channel that should receive notifications, and take its access token for the environment file. The channel's id goes in `channel_id`. Scry only posts; it opens no websocket and needs no other permissions.

## The Ingest Share

The ingest listener binds `127.0.0.1:8421` and refuses to bind anything else. Remote reachability is a reserved zrok share fronting it, living outside the process: no overlay SDK in the daemon, and an ingest surface still testable with curl on the box.

Reserve the share once by hand, then run it as a unit beside scry:

```sh
zrok reserve public --backend-mode proxy http://127.0.0.1:8421
```

```ini
[Unit]
Description=zrok reserved share fronting scry ingest
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/zrok share reserved <reserved-token> --headless
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

The share's public URL is what reporting jobs address. Confirm the exact command spellings against the installed zrok version before writing the unit.

## Per-Check Tokens

Every passive check carries its own bearer token, unique across the registry. Mint one per check and paste it into the config:

```sh
openssl rand -hex 16
```

Per-check tokens are what bound a leak to one check's ability to lie about itself. A shared token would quietly void that, so configuration rejects duplicates at boot.

## The Crontab Migration

Each reporting job replaces its mail tail with one curl on exit:

```sh
curl -fsS -m10 https://<ingest>/report/<check-id> -H "authorization: bearer <token>"
```

That is the whole migration for a job that only needs to say it ran. A job that wants to announce its own failure promptly can POST `{"status": "failed", "detail": "..."}` instead of waiting out its window; see [Passive Report Ingest](ingest.md).

## LAN Exposure

The status listener binds `0.0.0.0:8420` and is unauthenticated in v1. It names every monitored system and its current state, so it belongs on the LAN or behind a private share — never on a public address. The ingest share is the only surface intended to be reachable from outside, and it exposes report endpoints only.
