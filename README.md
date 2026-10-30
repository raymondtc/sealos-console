# sealos-console

A small, dependency-free Go CLI and agent skill for Sealos application operations.

- Query workspaces, apps and managed databases.
- Create apps and databases through **console APIs** or **OpenAPI**.
- Update an image by **container name**, preserving sidecars, environment references, probes and mounts.
- Preview writes by default; use `--execute` to submit them.
- Run an **in-memory local simulator** without Docker, Kubernetes, Node or Python.
- macOS, Linux and Windows binaries for amd64 and arm64.

This is an independent client, not an official Sealos project. The initial release is a preview: request contracts and local simulated writes are tested, but real deployment writes need acceptance testing against your installed Sealos components.

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

sealos-console --profile mock app list --compare
sealos-console --profile mock app upgrade worker \
  --container sidecar --image busybox:1.37.0 --execute --wait 10
```

PowerShell login:

```powershell
'demo' | .\sealos-console.exe auth login --profile mock --url http://127.0.0.1:8787 --username demo --password-stdin --local-test --workspace ns-demo
```

The simulator seeds `web` (one container) and `worker` (two containers and `fieldRef` environment variables). Its OpenAPI list deliberately omits `worker`, reproducing a compatibility failure while the console list remains complete. It also supports app/database creation in both adapters. State is in memory; restart the server to reset it.

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

Authentication follows Desktop's password login and regional session endpoints. The client saves **only a regional token**, URL and optional selected workspace in the OS user configuration directory. It fetches the provider's workspace-scoped kubeconfig into memory and sends it to Sealos HTTP services; it never writes a kubeconfig file or connects to Kubernetes directly. This still uses kubeconfig as the provider credential. Your organization's rules for API automation continue to apply.

`SEALOS_CONSOLE_CONFIG_DIR` overrides the session directory. Files use mode 0600 on Unix; on Windows protect the directory with your user ACLs. `auth logout` removes the local profile; it does not revoke the server token.

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
sealos-console --workspace YOUR_NAMESPACE --api console app create \
  --file examples/app.json --execute --wait 120

# Or choose OpenAPI explicitly
sealos-console --workspace YOUR_NAMESPACE --api openapi app create \
  --file examples/app.json --execute --wait 120
```

Use a different app name when testing both adapters in the same workspace. Existing names are rejected, never replaced. Creation CPU/memory settings are supported; changing the CPU or memory of an existing resource is outside this release's scope.

Supported app fields: `name`, `image`, `cpu` (cores), `memoryMi`, `replicas`, `ports`, `env`, `command` / `args` (string arrays), `storage` (`name`, absolute `path`, integer `sizeGi`), and an existing `imagePullSecret` name. Unknown fields are rejected.

- Console creation generates only managed Deployment/StatefulSet and Service resources. It supports internal TCP ports, persistent storage and `fieldRef` / `configMapKeyRef` / `secretKeyRef` env references. Arbitrary YAML is not accepted.
- OpenAPI creation delegates public HTTP domains to the server (`"public": true`). An existing `imagePullSecret`, `fieldRef` or `configMapKeyRef` is not expressible by the supported OpenAPI schema, so these combinations are rejected.
- Inline registry credentials, arbitrary ingress rules, GPU settings and configuration-file mounts are not implemented in this preview.
- Preview is a local operation plan plus read-only checks, not a server-side dry run or reservation.

## Upgrade an image

```sh
sealos-console --workspace YOUR_NAMESPACE app get demo-app
sealos-console --workspace YOUR_NAMESPACE app upgrade demo-app --image nginx:1.28.0
sealos-console --workspace YOUR_NAMESPACE app upgrade demo-app --image nginx:1.28.0 --execute --wait 120

# For an app with multiple containers, choose the exact container name.
sealos-console --workspace YOUR_NAMESPACE app upgrade worker \
  --container sidecar --image busybox:1.37.0 --execute
```

The console adapter reads the raw managed workload, changes exactly one image and preserves the entire containers array (JSON Merge Patch replaces arrays). It sends the read `resourceVersion` as a concurrency precondition, re-reads before writing, and verifies the resulting workload spec afterward. No environment values are printed in plans or summaries.

The OpenAPI adapter sends only `image.imageName`; it is restricted to a compatible single-container app. That API has no documented atomic version precondition. No adapter fallback, retry or rollback is performed after a failed/uncertain write. Cross-registry updates with existing pull secrets require a separate credential workflow and are refused.

`--wait SECONDS` waits for observed generation and updated/ready replicas. Without it, a successful write verifies configuration and platform visibility, not workload health.

## Create a managed database

```sh
sealos-console db engines
sealos-console --workspace YOUR_NAMESPACE db create --file examples/database.json
sealos-console --workspace YOUR_NAMESPACE --api console db create \
  --file examples/database.json --execute --wait 180
```

Choose an exact version returned by `db engines`. The database schema uses CPU cores, `memoryMi`, `storageGi`, `replicas` and an explicit `terminationPolicy` (`Delete` or `WipeOut`). `backupEnabled` defaults to false; true requests a daily 02:00 backup with seven-day retention.

`--api openapi` uses Database API v1, whose supported resource sizes are narrower than the console's. Database edits, engine upgrades, deletion and credential retrieval are not included.

## API compatibility

| Operation | Console adapter | OpenAPI adapter |
| --- | --- | --- |
| App list | `GET /api/getApps` | `GET /api/v2alpha/apps`, cross-checked with console list |
| App creation | `POST /api/applyApp` | `POST /api/v2alpha/apps` |
| Image upgrade | `POST /api/updateApp` | `PATCH /api/v2alpha/apps/{name}` |
| Database list | `GET /api/getDBList` | `GET /api/v1/database` |
| Database creation | `POST /api/createDB` | `POST /api/v1/database` |

Desktop and providers can be upgraded independently. API versions and Next.js build IDs are not Sealos release numbers. The client targets the above endpoint family; it does not claim universal compatibility with every Sealos release or private customization. See [the compatibility notes](docs/compatibility.md).

## Agent skill

The reusable skill is in [skills/sealos-console](skills/sealos-console/SKILL.md). Install/copy that directory into your agent's skill directory and make the CLI available on PATH. A `sealos-console-skill.zip` is included with releases. It contains generic instructions and examples, not credentials or deployment-specific configuration.

## Development and release

```sh
go test -race ./...
go vet ./...
go build -trimpath -o bin/sealos-console ./cmd/sealos-console
go run ./scripts/release.go --version v0.1.0
```

Tests cover both adapters, an actual local HTTP simulator, multi-container preservation, Downward API references, workspace checks, business errors, redirects, conflict handling and timeout-after-commit behavior. The release builder produces six archives, a skill archive and SHA-256 checksums without embedding local source paths.

MIT licensed. Sealos names and trademarks belong to their respective owners.
