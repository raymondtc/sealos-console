---
name: sealos-console
description: Operate Sealos applications and managed databases through console APIs using the sealos-console CLI. Use for resource discovery, app/database creation, image upgrades, logs/monitoring, object storage and local API simulation; not cluster installation or direct kubectl administration.
---

# Sealos console operations

Use `sealos-console` on PATH. If absent, prefer the project's prebuilt installer: download <https://raw.githubusercontent.com/raymondtc/sealos-console/main/install.sh> for macOS/Linux or <https://raw.githubusercontent.com/raymondtc/sealos-console/main/install.ps1> for Windows, then run it. The installer verifies the release checksum and does not require Go. Manual release archives and Go source builds are fallbacks. Do not substitute the unrelated cluster lifecycle `sealos` CLI.

The CLI provides `skill install|update --agent AGENT`, `skill status`, and `skill uninstall`. These delegate to Vercel `skills` and require Node.js >=22.20 with npm; normal platform operations do not. Scope defaults to global; use `--scope project` when the user requests a project-local installation. Install/update require explicit harness IDs, for example `codex`, `claude-code` or `cursor`. Updates install the running CLI release's skill, so upgrade the CLI first. Uninstall removes only this skill across harnesses within the chosen scope; shared directories prevent per-harness uninstall. `--dry-run` previews the exact command. Do not reinstall or manage other skills merely to perform a platform operation.

## Select the target

1. Use the profile, platform and workspace specified by the user/session. Run `auth status`, `workspace list`, and `doctor` as needed. Identify a workspace by namespace or UID; do not guess from a similar display name.
2. Login uses `auth login --url URL --username USER --password-stdin`, or a password supplied through `SEALOS_PASSWORD`. Do not put passwords in arguments, examples, output or this skill. Profiles store regional tokens and, after explicit storage setup, workspace S3 keys; provider kubeconfig remains in memory. This does not mean the platform API is kubeconfig-independent.
3. Use `--profile NAME --workspace NAMESPACE` explicitly for writes unless the correct workspace was saved at login. Do not use downloaded kubeconfigs, kubectl, arbitrary YAML apply or raw Kubernetes endpoints as fallback paths.

## Query and diagnose

```sh
sealos-console --profile NAME app list
sealos-console --profile NAME app get APP
sealos-console --profile NAME app domains
sealos-console --profile NAME db list
sealos-console --profile NAME db engines
```

Queries use the console inventory and retain applications with multiple containers or environment references. Summaries report reference kinds without exposing environment values.

For observation and object storage, read [references/observe-storage.md](references/observe-storage.md). It covers log/container selection, missing metrics, S3 setup, bucket creation and file transfers. `storage connect --execute` can initialize a user; do not run it implicitly during read-only work. Preserve the user-selected scope for creation/uploads and avoid automatic retries after uncertain writes.

## Create or upgrade

Read [references/operations.md](references/operations.md) for input shapes and limitations.

Creation and upgrades preview by default. Inspect the plan, then add `--execute` when the user's existing authorization covers that exact target and operation. A request to develop/test tooling is not a request to mutate a production resource. Do not ask again for approval that the user has already provided.

```sh
sealos-console --profile NAME --workspace NAMESPACE app create --file app.json
sealos-console --profile NAME --workspace NAMESPACE db create --file database.json
sealos-console --profile NAME --workspace NAMESPACE app upgrade APP --container CONTAINER --image IMAGE
```

For a new public HTTP endpoint, set `ports[].public=true`. Use `app domains` for available platform domains; `ports[].domain` optionally selects one. Preview URLs are not reserved and change when execution generates fresh prefixes. Report URLs from the executed result; routing read-back and workload readiness do not establish external reachability. Custom domains and public TCP/UDP forwarding are not implemented.

For image upgrades, query the app first. Specify `--container` for multiple containers. Image upgrades preserve original arrays and unknown fields. Never drop `fieldRef`, sidecars, probes or mounts to make a schema accept the workload.

After authorized execution use `--wait SECONDS`, or `app wait APP`, when readiness matters. Distinguish configuration accepted/visible from rollout ready. A failed, timed-out or unverifiable write must be followed by inspection, not automatic retry or rollback.

Existing app network changes, CPU/memory edits, database edits/upgrades, deletion and database credential retrieval are outside this CLI's current scope. CPU/memory can be specified when creating a resource.

## Local simulation

Start `sealos-console mock serve`. Use a separate `mock` profile and synthetic `demo`/`demo` credentials:

```sh
printf '%s\n' demo | sealos-console auth login --profile mock \
  --url http://127.0.0.1:8787 --username demo --password-stdin \
  --local-test --workspace ns-demo
sealos-console --profile mock app list
```

`web` is a single-container fixture; `worker` includes a sidecar and fieldRef variables. Fault modes are `--fault conflict` and `--fault timeout-after-write`. Restarting clears state. No real workloads or databases run; never describe simulated success as real Sealos acceptance testing.
