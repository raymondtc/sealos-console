# Logs, metrics and object storage

## Observation

```sh
sealos-console app pods APP
sealos-console app events APP
sealos-console app logs APP --pod POD --container CONTAINER --tail 100 --since 1h
sealos-console app logs APP --pod POD --container CONTAINER --follow --timeout 60
sealos-console app stats APP
sealos-console app metrics APP --metric cpu --since 15m
sealos-console app metrics APP --metric memory --since 1h
sealos-console db pods DB
sealos-console db logs DB --pod POD --tail 100
sealos-console db events DB
sealos-console db metrics DB --metric cpu --since 15m
```

Pod and container arguments must match the selected resource. Explicit `--pod` is required unless exactly one Pod exists; app logs need `--container` if multiple containers exist. Database logs use the primary container selected by the server and reject `--container`. `--previous` requests the previous container instance's logs; it does not query deleted Pods.

Log snapshots return JSON; `--follow` emits one JSON object per line with `pod`, `container` and `line`. Follow ends after `--timeout` seconds (default 120, max 3600), without reconnection. The server sends raw text despite its event-stream content type; upstream errors embedded in that text cannot reliably be distinguished from application output. The server's follow mode uses a fixed initial tail of up to 1000 lines, ignoring `--tail`; snapshots accept 1–1000. `--since` accepts 1s–24h. Empty logs are `state: no-data`.

App `stats` retains every returned container and reports missing Pods explicitly. CPU/memory are Kubernetes quantity strings (for example `12000000n` and `32768Ki`). Pod status/restart fields describe only the console's primary-container summary. They are not a complete sidecar health assessment.

Historical metrics support CPU/memory, 1s–24h lookback and a fixed one-minute step. Values and granularity follow the platform response; do not assume identical units across component versions. Historical series may include replaced Pods. Missing series are `state: no-data`, not measured zero; some server versions themselves pad missing points with zeros. Database connection counts, central/deleted-Pod log search and alert management are not implemented.

Logs and event messages contain application output and may include sensitive information. Summarize only what the user needs; the CLI does not redact arbitrary application logs.

## Object storage

```sh
sealos-console storage buckets
sealos-console storage bucket BUCKET
sealos-console --workspace NAMESPACE storage connect
sealos-console --workspace NAMESPACE storage connect --execute
sealos-console --workspace NAMESPACE storage create artifacts
sealos-console --workspace NAMESPACE storage create artifacts --execute
sealos-console storage ls artifacts --prefix reports/ --limit 100
sealos-console storage ls artifacts --next-token TOKEN
sealos-console storage upload artifacts reports/result.json --file result.json
sealos-console storage upload artifacts reports/result.json --file result.json --execute
sealos-console storage download artifacts reports/result.json --output downloaded.json
```

`storage connect --execute` calls the console's `/api/user/init`: it fetches existing S3 credentials but can initialize a storage user if absent. Treat it as an explicit write-capable setup operation. Never call it implicitly for a read-only audit. It saves S3 keys scoped to the current workspace in the private profile; no key is printed. List/download/upload require these cached keys and never initialize a user automatically. Bucket inventory does not require S3 setup. Reconnect explicitly after credential rotation. Logout removes locally saved tokens and S3 keys, but does not revoke remote credentials.

Bucket names accept either the workspace-prefixed S3 name or the console resource name. Creation accepts a resource label and always requests **private** access. Existing buckets are refused. The console endpoint is an upsert, so its preflight cannot atomically prevent a concurrent creator's policy being overwritten; avoid concurrent creation of the same name. Read-back verifies the policy, not backend readiness.

File operations use the same S3-compatible endpoint as the browser, with SigV4 and path-style addressing. They do not use direct Kubernetes access. Endpoints must be HTTPS on the platform domain or its subdomains; local mock endpoints must match the exact loopback origin.

Uploads use one PUT, max 1 GiB, require a regular file and send `If-None-Match: *` to refuse overwrites. Conditional-write support is required from the S3 server and has only been tested against the simulator. Size and SHA-256 metadata are checked with HEAD after upload. No retry is automatic. Inspect an uncertain write before retrying.

Downloads require a new file path, discard incomplete downloads and commit without replacing another file. The destination filesystem must support hard links (for example NTFS on Windows). Unix mode is 0600; Windows uses the directory's ACLs. Downloaded SHA-256 is checked when matching metadata exists; otherwise the CLI reports its computed hash. S3 calls currently use the transport's 30-second request timeout. Multipart transfers, overwriting, deletion and policy changes are not implemented.
