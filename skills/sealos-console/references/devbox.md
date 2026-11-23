# DevBox

Select the workspace explicitly for writes. All commands use the DevBox web console provider. Official template catalogs and existing DevBox inventory work with the Desktop session. Template versions, public and private markets, and template conversion require a separate DevBox session.

```sh
sealos-console --workspace WORKSPACE devbox connect
sealos-console --workspace WORKSPACE devbox connect --execute
sealos-console --workspace WORKSPACE devbox template list --scope official --search Python
sealos-console --workspace WORKSPACE devbox template list --scope private --page 1
sealos-console --workspace WORKSPACE devbox template versions REPOSITORY_UID
```

`connect --execute` may initialize a DevBox user and organization. It exchanges the Desktop application token for a DevBox token stored separately per workspace in the private CLI profile. Refresh with the same command after expiration. Catalog reads never initialize a session implicitly.

## Create, inspect and run a command

```sh
sealos-console --workspace WORKSPACE devbox create demo --template REPOSITORY_UID --template-version 3.12 --cpu-milli 2000 --memory-mi 4096 --storage-gi 10 --port 8080
sealos-console --workspace WORKSPACE devbox create demo --template REPOSITORY_UID --template-version 3.12 --port 8080 --execute --wait 180
sealos-console --workspace WORKSPACE devbox get demo
sealos-console --workspace WORKSPACE devbox exec demo --execute --timeout 120 -- python3 --version
```

Specify a version name or UID. A port defaults to internal HTTP. Add `--public` with that port to request a generated preview hostname. Actual readiness and quota depend on the provider. If a write times out or read-back fails, inspect the named DevBox before attempting another write.

`exec` is non-interactive and starts in `/home/devbox/project`. Pass the remote command and arguments after `--`, or explicitly invoke `sh -c` for pipelines and compound commands. It streams merged output by line and returns the verified remote exit code. Use `--json` for line events and a final exit-code event. An interrupted stream or missing exit marker produces an error and leaves the command outcome uncertain. Use the platform's IDE connection for an interactive session.

## Public preview

```sh
sealos-console --workspace WORKSPACE devbox ports demo
sealos-console --workspace WORKSPACE devbox public on demo --port 8080
sealos-console --workspace WORKSPACE devbox public on demo --port 8080 --execute
sealos-console --workspace WORKSPACE devbox public off demo --port 8080 --execute
```

`devbox ports` shows configured ports and their active public URLs. The toggle accepts an existing HTTP port and changes its platform-managed public route. It previews by default and reads back the port state after a write. Start the application on the selected port and bind it to `0.0.0.0` to serve traffic. Closing the route preserves the internal port and the running DevBox.

The hostname assigned by `public on` is stable for the lifetime of that DevBox and port under the same ingress domain. Reopening a port that previously had a Console-assigned hostname can yield a different URL. Manage WebIDE ports and custom-domain routes in the DevBox Console.

## Release and reuse

```sh
sealos-console --workspace WORKSPACE devbox release create demo --tag v1.0.0 --execute --wait 300
sealos-console --workspace WORKSPACE devbox release list demo
sealos-console --workspace WORKSPACE devbox release wait demo --tag v1.0.0 --timeout 300
sealos-console --workspace WORKSPACE devbox template create demo-python --from demo --tag v1.0.0 --template-version v1.0.0 --execute
```

Before release, build the application and verify its `entrypoint.sh`. The preview indicates whether the CLI will stop a running DevBox to snapshot its filesystem. The CLI keeps its port assignment and leaves the DevBox stopped after the release. Use `devbox start NAME` to resume it when needed. The release produces an image. An App Launchpad deployment is a separate operation. On providers that omit the image reference, a successful wait can report an image based on the Console's registry convention. Pullability in App Launchpad is a separate check. Private template creation uses the selected release image and does not supply extra environment variables or ConfigMap defaults. Remove credentials and other private data from the source project before taking a release snapshot.

`devbox start NAME` and `devbox stop NAME --mode retain-port|release-port` preview by default. Append `--execute --wait 120` to apply the action and poll. Use `retain-port` to preserve the existing port assignment. Use `release-port` when the port can be assigned again on a later start.
