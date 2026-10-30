# Compatibility and testing boundaries

The client implements the Desktop password/regional-token workflow and the App Launchpad/DB Provider API family found in the Sealos v5.1 source tree. Deployments may mix component builds. Confirm your installed images and API behavior rather than inferring compatibility from one platform version string.

Reference implementation used to understand the API contracts: [labring/sealos v5.1.2-rc6](https://github.com/labring/sealos/tree/v5.1.2-rc6). This reference is not a claim that every endpoint or deployment matches that tag. App Launchpad and DB Provider source directories were subsequently extracted from that repository.

## A workload can be valid Kubernetes and still fail OpenAPI parsing

For example, an environment variable may use:

```json
{"name":"POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}}
```

Some App Launchpad v2alpha schemas only model `valueFrom.secretKeyRef`. They cannot represent the example above. In the reference implementation, a list operation catches individual conversion errors and skips those apps, so HTTP 200 does not guarantee a complete list. This client keeps the console list and reports `missingFromOpenapi` when comparing adapters.

The relevant upstream implementation is [appService.ts](https://github.com/labring/sealos/blob/v5.1.2-rc6/frontend/providers/applaunchpad/src/services/backend/appService.ts). The console's raw list is useful for preserving fields that a reduced API model cannot express.

## Image preservation

Console upgrades send a JSON Merge Patch through the platform's `/api/updateApp`. Since arrays are replaced, the client preserves all original containers and all nested fields, modifies one named image, and includes `metadata.resourceVersion`. It does not regenerate an existing workload from the create schema. Workload spec read-back must equal the previous spec plus the intended image change.

OpenAPI upgrades use the server's image operation. They cannot select arbitrary sidecars and do not expose a documented atomic resource-version precondition. The client refuses incompatible workloads and never silently switches write adapters after a failure.

The supported client request is narrow, but the actual server implementation still determines side effects. In particular, inspect your version's StatefulSet update behavior before real use; older server implementations may have different fallback paths. Test against an isolated workspace before relying on a new component build.

## Local simulator

`mock serve` uses only memory and a loopback HTTP listener. It independently validates the expected request envelopes and simulates:

- Desktop login/workspace/provider discovery;
- console and OpenAPI app/database creation;
- a single-container app and a multi-container app with `fieldRef`;
- the OpenAPI omission and detail failure for unsupported env references;
- resourceVersion conflicts and image-only merge semantics;
- an accepted mutation followed by a client timeout.

It does not execute upstream Sealos code, start Pods or databases, enforce Kubernetes admission/RBAC/quota/billing, allocate volumes or networks, generate real credentials, validate database engines, or prove real rollout success. Public URLs, storage and resource requests are input-contract tests, not infrastructure tests.

The simulated `Running`/ready states are synthetic. Simulated write results are marked `simulation: true`. Restarting the server clears state. Never use a real account password with the mock service; its fixed demo credentials are `demo` / `demo`.

## Errors

Legacy endpoints can return HTTP 200 with a non-2xx business `code`; both levels are checked. Server error payloads and raw environment values are omitted from CLI output. A timeout can happen after the server has committed a write. Inspect resource state before manually retrying; the CLI never retries writes or performs automatic rollback.
