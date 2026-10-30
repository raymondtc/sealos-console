# sealos-console

A small, dependency-free Go CLI and agent skill for Sealos application operations.

- Query workspaces, apps and managed databases.
- Create apps and databases through **console APIs**, including platform-managed public HTTP/HTTPS endpoints.
- Update an image by **container name**, preserving sidecars, environment references, probes and mounts.
- Inspect app/database Pods, logs, events and CPU/memory history; inspect all app container usage.
- List/create object-storage buckets and list/upload/download objects through console-managed S3 access.
- Preview writes by default; use `--execute` to submit them.
- Run an **in-memory local simulator** without Docker, Kubernetes, Node or Python.
- macOS, Linux and Windows binaries for amd64 and arm64.

This is an independent client, not an official Sealos project. This release is a preview: request contracts and local simulated writes are tested, but real deployment writes need acceptance testing against your installed Sealos components.

## Install

Download an archive from [Releases](https://github.com/raymondtc/sealos-console/releases), verify it against `checksums.txt`, and put the binary on your PATH. Windows packages contain `sealos-console.exe`.

Or use Go 1.23+:

```sh
go install github.com/raymondtc/sealos-console/cmd/sealos-console@latest
```

## Try it locally

Start the simulator in one terminal:

```sh
sealos-console mock serve
```

In another terminal, log in with the **synthetic** demo credentials:

```sh
printf '%s\n' demo | sealos-console auth login \
  --profile mock --url http://127.0.0.1:8787 \
  --username demo --password-stdin --local-test --workspace ns-demo

sealos-console --profile mock app list
sealos-console --profile mock app upgrade worker \
  --container sidecar --image busybox:1.37.0 --execute --wait 10
```

PowerShell login:

```powershell
'demo' | .\sealos-console.exe auth login --profile mock --url http://127.0.0.1:8787 --username demo --password-stdin --local-test --workspace ns-demo
```

The simulator seeds `web` (one container) and `worker` (two containers and `fieldRef` environment variables). It supports app/database creation, image-only updates and readiness checks through the console API workflow. It also simulates Pods, logs/events/metrics, private bucket creation, signed S3 listing and file transfers. The `archive` bucket starts with `hello.txt`. State is in memory; restart the server to reset it.

Fault injection:

```sh
sealos-console mock serve --listen 127.0.0.1:8788 --fault conflict
sealos-console mock serve --listen 127.0.0.1:8789 --fault timeout-after-write
```

Log in with a separate mock profile for each port. `conflict` rejects image updates with HTTP 409. `timeout-after-write` commits a simulated mutation, then delays its response beyond the client's timeout. Inspect the resource after the timeout; the client does not blindly retry.

**This is an API simulator, not a lightweight Sealos distribution.** It starts no containers or database engines. It cannot verify upstream admission policies, image pulling, storage, networking, billing, controllers or real rollouts. Local-test profiles accept only `http://127.0.0.1:PORT`, and mock providers must use that exact origin. Production profiles require HTTPS.

## Connect to Sealos

Use your own platform URL and account:

```sh
sealos-console auth login --url https://cloud.example.com \
  --username YOUR_USER --password-stdin
sealos-console workspace list
sealos-console --workspace YOUR_NAMESPACE doctor
```

Provide the password on stdin, or through `SEALOS_PASSWORD`. Password arguments are intentionally unsupported. `SEALOS_USERNAME` is also accepted.

Authentication follows Desktop's password login and regional session endpoints. Login saves a regional token, URL and optional selected workspace in the OS user configuration directory. It fetches the provider's workspace-scoped kubeconfig into memory and sends it to Sealos HTTP services; it never writes a kubeconfig file or connects to Kubernetes directly. This still uses kubeconfig as the provider credential. Your organization's rules for API automation continue to apply.

`SEALOS_CONSOLE_CONFIG_DIR` overrides the session directory. Files use mode 0600 on Unix; on Windows protect the directory with your user ACLs. An explicit `storage connect --execute` additionally stores S3 keys per workspace in that private profile. `auth logout` removes all local profile credentials; it does not revoke the server token or S3 keys.

## Create an app

[examples/app.json](examples/app.json):

```json
{
  "name": "demo-app",
  "image": "nginx:1.27.5",
  "cpu": 0.5,
  "memoryMi": 512,
  "replicas": 1,
  "ports": [{"number": 80, "public": false}],
  "env": [{"name": "MODE", "value": "demo"}]
}
```

```sh
# Preview only
sealos-console --workspace YOUR_NAMESPACE app create --file examples/app.json

# Submit through the same API used by the console
sealos-console --workspace YOUR_NAMESPACE app create \
  --file examples/app.json --execute --wait 120

```

Existing names are rejected, never replaced. Creation CPU/memory settings are supported; changing the CPU or memory of an existing resource is outside this release's scope.

Supported app fields: `name`, `image`, `cpu` (cores), `memoryMi`, `replicas`, `ports`, `env`, `command` / `args` (string arrays), `storage` (`name`, absolute `path`, integer `sizeGi`), and an existing `imagePullSecret` name. Unknown fields are rejected.

- Console creation generates managed Deployment/StatefulSet, Service and optional Ingress resources. It supports internal TCP ports, persistent storage and `fieldRef` / `configMapKeyRef` / `secretKeyRef` env references. Arbitrary YAML is not accepted.
- Public HTTP endpoints (`"public": true`) use the platform's advertised domain and HTTPS setting. `domain` optionally selects another domain returned by `app domains`. Custom domains and prefixes are not accepted.
- Inline registry credentials, arbitrary ingress rules, GPU settings and configuration-file mounts are not implemented in this preview.
- Preview is a local operation plan plus read-only checks, not a server-side dry run or reservation.

## Create an app with public access

```sh
sealos-console --workspace YOUR_NAMESPACE app domains
sealos-console --workspace YOUR_NAMESPACE app create --file examples/app-public.json
sealos-console --workspace YOUR_NAMESPACE app create --file examples/app-public.json --execute --wait 120
sealos-console --workspace YOUR_NAMESPACE app get demo-public
```

Set `"ports": [{"number": 80, "public": true}]` in the application JSON. The port must serve HTTP. The client reads `/api/platform/getInitData`, generates a random subdomain per public port, and submits the managed Ingress with the workload and Service through `/api/applyApp`. HTTPS uses the domain's configured certificate reference, falling back to the console's `wildcard-cert` convention. HTTP-only platforms omit TLS. No certificate or DNS records are created by the CLI.

To choose another advertised domain, add `"domain": "apps.example.com"` to that public port, using an actual value from `app domains`. Leaving it out selects the platform default. Mixed public and private ports are supported.

Plans and creation results include `publicEndpoints` with URLs. **A preview does not reserve a domain.** Each invocation generates fresh prefixes; use the URLs returned by the executed command. The client reads back `/api/getAppByAppName` and checks each public port's domain, Service and route. Missing or mismatched routing returns `WRITE_UNVERIFIED`; resources may already exist, so inspect them instead of blindly rerunning creation. `app get` returns network information without exposing the detail endpoint's environment values.

`publicRoutingVerified` means the console reports the expected routing configuration. It does not prove DNS, certificate delivery, external HTTP reachability or controller readiness; `--wait` checks only workload readiness. Existing app network edits, custom domains/prefixes, TCP/UDP public forwarding, and custom ingress routes are outside this release.

## Upgrade an image

```sh
sealos-console --workspace YOUR_NAMESPACE app get demo-app
sealos-console --workspace YOUR_NAMESPACE app upgrade demo-app --image nginx:1.28.0
sealos-console --workspace YOUR_NAMESPACE app upgrade demo-app --image nginx:1.28.0 --execute --wait 120

# For an app with multiple containers, choose the exact container name.
sealos-console --workspace YOUR_NAMESPACE app upgrade worker \
  --container sidecar --image busybox:1.37.0 --execute
```

The client reads the raw managed workload, changes exactly one image and preserves the entire containers array (JSON Merge Patch replaces arrays). It sends the read `resourceVersion` as a concurrency precondition, re-reads before writing, and verifies the resulting workload spec afterward. No environment values are printed in plans or summaries.

No automatic retry or rollback is performed after a failed/uncertain write. Cross-registry updates with existing pull secrets require a separate credential workflow and are refused.

`--wait SECONDS` waits for observed generation and updated/ready replicas. Without it, a successful write verifies configuration and platform visibility, not workload health.

## Create a managed database

```sh
sealos-console db engines
sealos-console --workspace YOUR_NAMESPACE db create --file examples/database.json
sealos-console --workspace YOUR_NAMESPACE db create \
  --file examples/database.json --execute --wait 180
```

Choose an exact version returned by `db engines`. The database schema uses CPU cores, `memoryMi`, `storageGi`, `replicas` and an explicit `terminationPolicy` (`Delete` or `WipeOut`). `backupEnabled` defaults to false; true requests a daily 02:00 backup with seven-day retention.

Database edits, engine upgrades, deletion and credential retrieval are not included.

## Logs and monitoring

```sh
sealos-console app pods APP
sealos-console app logs APP --pod POD --container CONTAINER --tail 100 --since 1h
sealos-console app logs APP --pod POD --container CONTAINER --follow --timeout 60
sealos-console app events APP
sealos-console app stats APP
sealos-console app metrics APP --metric cpu --since 15m
sealos-console db pods DB
sealos-console db logs DB --pod POD
sealos-console db events DB
sealos-console db metrics DB --metric memory --since 1h
```

Logs require an explicit Pod unless there is exactly one; multi-container app logs also require a container name. Snapshots are JSON; followed logs emit JSON lines and stop after the timeout. Empty logs/series report `no-data`. App stats preserve all returned containers, report missing Pods and retain Kubernetes resource quantity units. Pod status/restarts are the console's primary-container summary. Historical metric units follow the platform; some server versions pad missing points with zeros.

`--previous` reads the previous container instance. Follow mode uses the server's initial tail of up to 1000 lines regardless of `--tail`. Logs/event messages may contain application secrets and are not automatically redacted. Database connection metrics, deleted-Pod log search and alert management are outside this release. See [detailed observation behavior](skills/sealos-console/references/observe-storage.md).

## Object storage

```sh
sealos-console storage buckets
sealos-console --workspace YOUR_NAMESPACE storage connect
sealos-console --workspace YOUR_NAMESPACE storage connect --execute
sealos-console storage create artifacts
sealos-console storage create artifacts --execute
sealos-console storage ls artifacts --prefix reports/ --limit 100
sealos-console storage upload artifacts reports/result.json --file result.json
sealos-console storage upload artifacts reports/result.json --file result.json --execute
sealos-console storage download artifacts reports/result.json --output downloaded.json
```

`storage connect --execute` fetches S3 credentials and **may initialize a storage user**. It stores keys in the private profile for the selected workspace, without printing them. Ordinary object reads require cached credentials and never call this initializer. Bucket listings need no S3 setup. Logout removes the local keys; reconnect explicitly after rotation.

Buckets are created through the console with private access; existing names are refused. The platform creation endpoint is an upsert, so concurrent creation of the same name can still change policy between preflight and submission. File operations use the browser's S3-compatible endpoint and SigV4, with no direct Kubernetes access. Only buckets managed by the selected workspace are accepted.

Uploads are a single PUT (max 1 GiB), preview by default, and refuse overwrites using `If-None-Match: *`; your S3 server must support that condition. A HEAD checks size/checksum metadata afterward. Downloads require a new path and a filesystem supporting hard links; partial files are discarded. File requests have a 30-second timeout. There is no automatic retry, multipart upload, deletion or permission editing. See [storage details and limits](skills/sealos-console/references/observe-storage.md).

## API compatibility

| Operation | Console endpoint |
| --- | --- |
| App list / workload / readiness | `GET /api/getApps` |
| App network details | `GET /api/getAppByAppName?appName=NAME` |
| Public domains / creation metadata | `GET /api/platform/getInitData` |
| App creation | `POST /api/applyApp` |
| Image upgrade | `POST /api/updateApp` |
| Database list / get | `GET /api/getDBList` |
| Database engine versions | `GET /api/platform/getVersion` |
| Database creation | `POST /api/createDB` |
| App Pods / events | `GET /api/getAppPodsByAppName`, `GET /api/getPodEvents` |
| App logs / instantaneous usage | `POST /api/getPodLogs`, `POST /api/getPodsMetrics` |
| App / database CPU and memory history | `GET /api/monitor/getMonitorData` on the respective provider |
| Database Pods / logs / events | `/api/pod/getPodsByDBName`, `/api/pod/getPodLogs`, `/api/pod/getPodEvents` |
| Bucket list / create | `POST /api/bucket/list`, `POST /api/bucket/create` |
| Explicit S3 setup | `GET /api/user/init` (can initialize a user) |
| Object list / upload / download | S3 ListObjectsV2 / conditional PUT + HEAD / GET |

Starting with v0.2.0, resource management uses console APIs. Version 0.4.0 adds browser-compatible S3 file operations. The `--api` and `--compare` flags were removed and are rejected. Update scripts that used them. Public endpoint creation was restored through console APIs in v0.3.0.

Desktop and providers can be upgraded independently. API versions and Next.js build IDs are not Sealos release numbers. The client targets the above endpoint family; it does not claim universal compatibility with every Sealos release or private customization. See [the compatibility notes](docs/compatibility.md).

## Agent skill

The reusable skill is in [skills/sealos-console](skills/sealos-console/SKILL.md). Install/copy that directory into your agent's skill directory and make the CLI available on PATH. A `sealos-console-skill.zip` is included with releases. It contains generic instructions and examples, not credentials or deployment-specific configuration.

## Development and release

```sh
go test -race ./...
go vet ./...
go build -trimpath -o bin/sealos-console ./cmd/sealos-console
go run ./scripts/release.go --version v0.4.0
```

Tests cover console operations, an actual local HTTP simulator, multi-container preservation, Downward API references, workspace checks, business errors, redirects, conflict handling and timeout-after-commit behavior. The release builder produces six archives, a skill archive and SHA-256 checksums without embedding local source paths.

MIT licensed. Sealos names and trademarks belong to their respective owners.

## Optional live read-only verification

After logging in to the desired workspace, run:

```sh
SEALOS_CONSOLE_READONLY_PROFILE=YOUR_PROFILE go test ./internal/console -run '^TestLiveReadOnlyConsoleEndpoints$' -v -count=1
```

This test is skipped unless the profile environment variable is set. It uses the saved session and rejects all requests except an explicit GET allowlist, including rejecting workspace switches. Use a profile bound to its current workspace. It reads inventories and each app/database summary, checks doctor, public domains, existing network details and public creation previews, and reports only counts and endpoint paths. It does not submit creates, upgrades or deletes. A database version response reflects the console's catalog; some server versions may return a fallback catalog on backend failure.
