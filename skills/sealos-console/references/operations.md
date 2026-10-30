# Operation reference

All commands emit JSON. Common flags can appear before or after commands. Resource management uses console APIs; object transfers use the browser-compatible S3 endpoint; there is no API-selection flag.

## Application creation

Example JSON:

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

Optional fields: `command` and `args` as string arrays; `storage` as objects with `name`, absolute `path`, integer `sizeGi`; `imagePullSecret` names an existing pull secret. Image tags/digests must be explicit. Names start with a lowercase letter and are DNS labels of at most 63 characters.

Console creation supports private TCP ports, persistent storage and Kubernetes fieldRef/configMapKeyRef/secretKeyRef environment references. It generates platform-managed Deployment/StatefulSet, Service and optional Ingress resources and submits them through `/api/applyApp`; it is not an arbitrary YAML channel.

Set `ports[].public=true` to create a public HTTP endpoint with a random subdomain. `app domains` lists advertised domains; optional `ports[].domain` must select one of them. The CLI reads the platform HTTPS setting and certificate mapping, then creates the Ingress through the same `/api/applyApp` submission. Preview and execution generate different prefixes; use `publicEndpoints[].url` from execution.

After creation the CLI checks network read-back through `/api/getAppByAppName`; `publicRoutingVerified` does not verify DNS, certificate validity or external reachability. A failed submission can leave partial resources. Inspect first; do not repeat creation automatically. Existing app network edits, custom domains/prefixes, public TCP/UDP forwarding, inline registry credentials, GPU settings, custom ingress rules and configuration-file mounts are not implemented.

## Database creation

```json
{
  "name": "demo-db",
  "type": "postgresql",
  "version": "postgresql-14.8.0",
  "cpu": 1,
  "memoryMi": 1024,
  "storageGi": 10,
  "replicas": 1,
  "terminationPolicy": "Delete",
  "backupEnabled": false
}
```

Use the exact version returned by `db engines`, not an assumed version from this example. `terminationPolicy` is mandatory; `WipeOut` has different future deletion semantics from `Delete`. The CLI does not expose deletion. Backup defaults off; when enabled it requests daily 02:00 backups retained for seven days.

Console uses `/api/createDB` with `isEdit=false`, millicores and MiB. Engine versions come from the console endpoint `/api/platform/getVersion`; some server builds can return a fallback catalog on backend failure.

## Image updates

```sh
sealos-console --workspace NAMESPACE app upgrade APP \
  --container CONTAINER --image registry.example.com/app:v2 --execute --wait 120
```

Console reads the raw workload and sends one image-only patch to `/api/updateApp`, preserving every container and nested field. It includes resourceVersion and verifies the spec after writing. Unknown server builds, especially StatefulSet fallback behavior, still need acceptance testing.

Same-image upgrades are no-ops. Cross-registry changes with existing pull secrets are refused. Failed writes are never retried automatically. `WRITE_UNVERIFIED` means a write may have succeeded but read-back did not establish the expected state; inspect before doing anything else.

## Version and test boundaries

The reference API family is visible in <https://github.com/labring/sealos/tree/v5.1.2-rc6>. Desktop and providers may use different builds. Do not identify the whole deployment from available database engine versions or Next.js build IDs.

The local simulator has no Kubernetes or Sealos controllers. Its synthetic ready states test the client workflow only. Real quota, image pulling, storage, ingress, RBAC and database readiness are not validated by it.
