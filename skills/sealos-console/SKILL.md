---
name: sealos-console
description: Operate Sealos applications and managed databases through console APIs or OpenAPI using the sealos-console CLI. Use for resource discovery, app/database creation, image upgrades and local API simulation; not cluster installation or direct kubectl administration.
---

# Sealos console operations

Use the `sealos-console` Go CLI on PATH. If absent, install a matching binary from <https://github.com/raymondtc/sealos-console/releases> or build with `go install github.com/raymondtc/sealos-console/cmd/sealos-console@latest`. Do not substitute the unrelated cluster lifecycle `sealos` CLI.

## Select the target

1. Use the profile, platform and workspace specified by the user/session. Run `auth status`, `workspace list`, and `doctor` as needed. Identify a workspace by namespace or UID; do not guess from a similar display name.
2. Login uses `auth login --url URL --username USER --password-stdin`, or a password supplied through `SEALOS_PASSWORD`. Do not put passwords in arguments, examples, output or this skill. Profiles store regional tokens; provider kubeconfig remains in memory. This does not mean the platform API is kubeconfig-independent.
3. Use `--profile NAME --workspace NAMESPACE` explicitly for writes unless the correct workspace was saved at login. Do not use downloaded kubeconfigs, kubectl, arbitrary YAML apply or raw Kubernetes endpoints as fallback paths.

## Query and diagnose

```sh
sealos-console --profile NAME app list --compare
sealos-console --profile NAME app get APP
sealos-console --profile NAME db list
sealos-console --profile NAME db engines
```

An OpenAPI list may silently omit apps whose env references cannot be parsed. `fieldRef` and `configMapKeyRef` are common examples; this does not make the underlying workload invalid. Keep the console list as the canonical inventory and report the difference. Never recreate an app merely because it is missing from OpenAPI.

## Create or upgrade

Read [references/operations.md](references/operations.md) for input shapes, API selection and limitations.

Creation and upgrades preview by default. Inspect the plan, then add `--execute` when the user's existing authorization covers that exact target and operation. A request to develop/test tooling is not a request to mutate a production resource. Do not ask again for approval that the user has already provided.

```sh
sealos-console --profile NAME --workspace NAMESPACE app create --file app.json
sealos-console --profile NAME --workspace NAMESPACE db create --file database.json
sealos-console --profile NAME --workspace NAMESPACE app upgrade APP --container CONTAINER --image IMAGE
```

For image upgrades, query the app first. Specify `--container` for multiple containers. Prefer `--api console` to preserve original arrays and unknown fields; OpenAPI is limited to compatible single-container applications. Never drop `fieldRef`, sidecars, probes or mounts to make a schema accept the workload.

After authorized execution use `--wait SECONDS`, or `app wait APP`, when readiness matters. Distinguish configuration accepted/visible from rollout ready. A failed, timed-out or unverifiable write must be followed by inspection, not automatic retry, rollback or a switch to another write adapter.

CPU/memory edits, database edits/upgrades, deletion and credential retrieval are outside this CLI's current scope. CPU/memory can be specified when creating a resource.

## Local simulation

Start `sealos-console mock serve`. Use a separate `mock` profile and synthetic `demo`/`demo` credentials:

```sh
printf '%s\n' demo | sealos-console auth login --profile mock \
  --url http://127.0.0.1:8787 --username demo --password-stdin \
  --local-test --workspace ns-demo
sealos-console --profile mock app list --compare
```

`web` is a single-container fixture; `worker` includes a sidecar and fieldRef variables. Fault modes are `--fault conflict` and `--fault timeout-after-write`. Restarting clears state. No real workloads or databases run; never describe simulated success as real Sealos acceptance testing.
