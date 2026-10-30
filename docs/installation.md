# Installation and harness support

## Existing projects evaluated

- [Vercel skills](https://github.com/vercel-labs/skills): general skill installation/removal, an upstream harness registry, global/project scopes, copy/link modes and JSON results. Its [supported agent table](https://github.com/vercel-labs/skills/blob/main/docs/agents.md) is the source of truth for agent IDs and paths.
- [LiveKit CLI](https://docs.livekit.io/reference/developer-tools/livekit-cli/skills/): a useful example of a Go CLI exposing skill lifecycle commands, but its Go package pins its repository and lock ownership to LiveKit, so it cannot be imported as a general installer without forking.
- [skillshare](https://github.com/runkids/skillshare): useful for centrally managing many skills and syncing configured targets, but initializing/syncing a user's whole skill library is broader than installing one product's skill.

This project invokes **skills@1.7.4** via npm/npx. It does not copy the harness directory registry or implement another skill store/lock format. The pinned manager requires Node.js >=22.20; update the pinned version deliberately after running integration tests. Standard Windows Node.js installations are supported by invoking npm's `npx-cli.js` with Node, avoiding command-shell argument interpretation. Unusual Windows Node/npm shims without a discoverable npm entrypoint report an explicit dependency error.

## Lifecycle

`skill install` and `skill update` invoke `skills add` for `https://github.com/raymondtc/sealos-console/tree/VERSION/skills/sealos-console`, with the exact skill name, explicit agents, `--copy --yes --json` and an explicit scope. `VERSION` is the running binary's release; development builds use `main`. npm and GitHub access are needed unless the package/source are already available to upstream. This is not an offline embedded skill installer.

The refresh operation deliberately re-adds only this one versioned source rather than invoking upstream's bulk update command. A newer skill becomes available by upgrading the CLI first. Upstream installation may replace manual edits to this skill; preserve customizations before updating. Scripts and lifecycle commands leave Sealos profiles, passwords and S3 credentials alone.

`skill status` delegates to `skills list --json`, then returns only entries named `sealos-console`. Install/update validate upstream JSON success for every requested agent and query the installed state afterward. A failure or partial result does not trigger retries or rollback. Manager diagnostics go to stderr; structured results go to stdout. Telemetry is disabled for child processes with `DISABLE_TELEMETRY=1` and `DO_NOT_TRACK=1`.

`skill uninstall` delegates to `skills remove sealos-console --yes` for the selected scope. It intentionally accepts no `--agent`: Codex, Cursor, Gemini and other harnesses may share `.agents/skills`, so one harness cannot always lose access independently. Upstream may retain a shared copy when a per-agent removal leaves another detected consumer. Whole-scope removal gives a verifiable result without adding custom path deletion logic. Other skill names are never selected. Agent IDs must be explicit for install/update; wildcards and implicit install-to-all are not accepted.

Global scope is the default; project scope is the current working directory. A shared project directory is readable by any harness that supports it even when only one was selected. These commands do not configure MCP, plugins, harness permissions or model settings.

## Existing manual installations

Use one manager for this skill. A manually copied skill or developer symlink may be outside the upstream manager's currently supported directories (for example an older Codex-specific path). This CLI does not scan arbitrary historical directories or remove those copies. Inspect `skill status` and the upstream path table, then move a duplicate/manual copy aside if migrating. Do not delete a symlink's source repository. Existing installations managed by another tool should be removed or migrated with that tool.

## Binary installers

`install.sh` uses curl, tar and sha256sum/shasum; `install.ps1` uses PowerShell's built-in download, ZIP and SHA-256 facilities. Both select macOS/Linux/Windows and amd64/arm64, fetch the named release and checksums, verify before replacement, check the executable version, and preserve the previous binary on verification failures. User-level installation needs no administrator privileges. PATH changes are printed, not persisted automatically.

The scripts and `checksums.txt` come from the same GitHub release; checksum matching detects corrupt/mismatched downloads, not an independent cryptographic signature of the publisher. macOS/Linux default to `~/.local/bin`; Windows defaults to `%LOCALAPPDATA%\sealos-console\bin`. Windows refuses a reparse-point destination; close a running Windows CLI before replacing it. UNIX installation replaces an existing executable symlink without overwriting the linked source. No login state is migrated or cleared by an installation.

Tests run the actual scripts against synthetic release archives on all three operating systems, including failed checksums and replacement. Upstream manager integration tests install/update/uninstall a synthetic skill for Codex, Claude Code, Cursor, GitHub Copilot, Gemini CLI, OpenCode and Windsurf in a temporary project, while preserving an unrelated skill.
