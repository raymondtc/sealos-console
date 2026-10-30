# Compatibility and testing boundaries

The client implements the Desktop password/regional-token workflow and the App Launchpad/DB Provider API family found in the Sealos v5.1 source tree. Deployments may mix component builds. Confirm your installed images and API behavior rather than inferring compatibility from one platform version string.

Reference implementation used to understand the API contracts: [labring/sealos v5.1.2-rc6](https://github.com/labring/sealos/tree/v5.1.2-rc6). This reference is not a claim that every endpoint or deployment matches that tag. App Launchpad and DB Provider source directories were subsequently extracted from that repository.

## Console-only operations

Version 0.2.0 removes the OpenAPI adapter and the `--api` / `--compare` flags. Remove those flags from existing scripts. Version 0.3.0 adds platform-managed public HTTP/HTTPS endpoint creation through console APIs.

App summaries are read from the console workload list, including multi-container apps and environment references such as `fieldRef` and `configMapKeyRef`. The reduced schemas of some historical OpenAPI versions could not represent these workloads and silently omitted apps from list responses. The CLI no longer calls those endpoints.

Database engine versions use `/api/platform/getVersion`, the same catalog endpoint used by the console. Some server versions return a static fallback catalog if the backend query fails, so a successful catalog read alone does not establish that an engine can be provisioned.

## Public endpoint creation

The console's [ingress generator](https://github.com/labring/sealos/blob/v5.1.2-rc6/frontend/providers/applaunchpad/src/utils/deployYaml2Json.ts) builds an Ingress for each public HTTP port. The CLI follows its manager/domain/port labels, nginx annotations, root Prefix route and platform certificate mapping. Configuration comes from `/api/platform/getInitData`; resources are submitted together through `/api/applyApp`. This client supports HTTP backends and platform-managed domains, not custom domains, prefixes or arbitrary ingress resources.

Each invocation generates fresh random domain prefixes, including previews. Nothing is reserved before execution. The creation response exposes the generated URLs, and network read-back checks their domain, backend Service and port through `/api/getAppByAppName`. This adapted detail response does not establish the actual TLS resource, certificate validity, DNS or external reachability. App inventory and image preservation continue to use raw console workloads. Detail responses are projected to network fields so environment values are not exposed.

Server creation can partially succeed before a dependent resource fails. The CLI does not retry, recreate, or roll back on failure. Inspect existing resources before attempting recovery. Server versions determine ingress admission, owner references and domain collision behavior; an isolated real write acceptance test remains necessary for a new build.

## Image preservation

Console upgrades send a JSON Merge Patch through the platform's `/api/updateApp`. Since arrays are replaced, the client preserves all original containers and all nested fields, modifies one named image, and includes `metadata.resourceVersion`. It does not regenerate an existing workload from the create schema. Workload spec read-back must equal the previous spec plus the intended image change.

The supported client request is narrow, but the actual server implementation still determines side effects. In particular, inspect your version's StatefulSet update behavior before real use; older server implementations may have different fallback paths. Test against an isolated workspace before relying on a new component build.

## Local simulator

`mock serve` uses only memory and a loopback HTTP listener. It independently validates the expected request envelopes and simulates:

- Desktop login/workspace/provider discovery;
- console app/database creation, including public Ingress/Service association and TLS settings;
- a single-container app and a multi-container app with `fieldRef`;
- resourceVersion conflicts and image-only merge semantics;
- an accepted mutation followed by a client timeout.

It does not execute upstream Sealos code, start Pods or databases, enforce Kubernetes admission/RBAC/quota/billing, allocate volumes or networks, generate real credentials, validate database engines, or prove real rollout success. Public URLs, storage and resource requests are input-contract tests, not infrastructure tests.

The simulated `Running`/ready states are synthetic. Simulated write results are marked `simulation: true`. Restarting the server clears state. Never use a real account password with the mock service; its fixed demo credentials are `demo` / `demo`.

## Errors

Legacy endpoints can return HTTP 200 with a non-2xx business `code`; both levels are checked. Server error payloads and raw environment values are omitted from CLI output. A timeout can happen after the server has committed a write. Inspect resource state before manually retrying; the CLI never retries writes or performs automatic rollback.

## Observation and object storage (v0.4.0)

App log snapshots/streams, events, Pod inventory, instantaneous container metrics and CPU/memory history have been verified with a real account using read-only operations. Database Pods/logs/events and CPU/memory history also returned usable responses. Empty log responses can omit `data`; they are reported as `no-data`. App history timestamps are sent as milliseconds, database history as seconds. Their payload formats and metric units differ. Follow responses are raw text under an event-stream content type, not SSE `data:` frames. A stream opening successfully does not prove a healthy workload or distinguish server errors embedded in raw log text.

Bucket inventory, S3 object listing and downloading an existing small object have been verified on a real deployment. Real create/upload operations were not performed; their contracts and safeguards are tested locally. Object storage follows the historical `system-objectstorage` provider, which was removed from the main upstream tree in [#7186](https://github.com/labring/sealos/pull/7186); this does not establish support on newer component builds. Missing providers fail explicitly.

`GET /api/user/init` can create a storage user, so it is exposed only by explicit `storage connect --execute`. S3 file operations thereafter use cached workspace credentials, HTTPS, path-style SigV4 in `us-east-1`, and no redirects or automatic retries. No bucket is created directly through S3. The console bucket-create endpoint is an upsert: duplicate preflight is not an atomic concurrency guard. Uploads rely on server enforcement of `If-None-Match: *`; verify that behavior in an isolated workspace before production writes. Downloads do not overwrite local paths and require hard-link support.

The simulator seeds synthetic metrics/logs/events and a bucket/object, validates signed S3 requests and models conditional uploads. It does not validate the real storage controller, object-store conditional-write support, capacity, retention or permissions. No deployment addresses, credentials, object contents or raw application logs are included in the published project.
