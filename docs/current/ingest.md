# Passive Report Ingest

Scry's ingest listener is a dedicated HTTP surface bound to `ingest_listen`. Loopback-only binding is enforced both by configuration validation and again against the server's actual bound address. It serves passive reports only: status API and dashboard paths return 404 because they belong to a separate handler tree on a separate listener, and the status listener returns 404 for `/report/*` in the same way. Production HTTPS and remote reachability belong to the external reserved zrok share; the daemon itself has no overlay or TLS dependency.

Each passive check owns a unique bearer token. The authorization scheme is case-insensitive, and the token is compared in constant time against the credential registered for the path's check. Unknown and active-check ids take the same dummy-hash comparison path and return the same 401 as a bad token, so a caller with only the share URL cannot use response precedence to discover registry membership.

```sh
curl -fsS -m10 https://<ingest>/report/nas-snapshot \
  -H "authorization: bearer <token>"
```

## Report Contract

`GET /report/<check-id>` is always an `ok` report. Its body is ignored outright. A bodiless `POST` is also `ok`; a POST with detail uses one JSON object of at most 4 KiB:

```json
{"status": "failed", "detail": "snapshot exited 2"}
```

`status` defaults to `ok` and accepts only `ok` or `failed`. Unknown fields are ignored for forward compatibility. The object must be followed only by EOF, and fields must have their declared JSON types. Detail is truncated to 512 UTF-8 bytes without splitting a rune.

Responses contain no body:

| status | meaning |
| --- | --- |
| 204 | the report was applied and persisted |
| 400 | the POST body was malformed |
| 401 | the id/token pair was not a known passive check and its valid credential |
| 404 | the path was outside the `/report/<check-id>` shape |
| 405 | the method was not GET or POST |
| 413 | the POST body exceeded 4 KiB |
| 500 | the engine could not apply or persist the report |

The 204 durability boundary is deliberate: it is returned only after the engine's serialized report command has updated the record and the state file save has succeeded. A persistence failure is fatal to the engine and tears down the daemon through the shared component lifecycle.
