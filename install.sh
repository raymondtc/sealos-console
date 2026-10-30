#!/bin/sh
# Install the prebuilt CLI. No Go, Node.js or administrator privileges required.
set -eu
version=${SEALOS_CONSOLE_VERSION:-v0.5.0}
install_dir=${SEALOS_CONSOLE_INSTALL_DIR:-"$HOME/.local/bin"}
usage() {
  echo 'Usage: sh install.sh [--version vX.Y.Z] [--install-dir DIR]'
  echo 'Defaults: the release bundled with this script; ~/.local/bin'
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--install-dir)
      [ "$#" -ge 2 ] || { usage >&2; exit 1; }
      case "$1" in --version) version=$2 ;; --install-dir) install_dir=$2 ;; esac
      shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 1 ;;
  esac
done
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$' || { echo 'Invalid version.' >&2; exit 1; }
[ -n "$install_dir" ] || { echo 'Install directory is empty.' >&2; exit 1; }
case "$install_dir" in /*) ;; *) install_dir="$PWD/$install_dir" ;; esac
case "$(uname -s)" in Darwin) platform=darwin ;; Linux) platform=linux ;; *) echo 'Use install.ps1 for Windows; unsupported OS.' >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo 'Supported architectures: amd64 and arm64.' >&2; exit 1 ;; esac
for tool in curl tar awk mktemp; do command -v "$tool" >/dev/null 2>&1 || { echo "Required tool missing: $tool" >&2; exit 1; }; done
if command -v sha256sum >/dev/null 2>&1; then checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum_tool=shasum
else echo 'Install sha256sum or shasum to verify the download.' >&2; exit 1; fi
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/sealos-console-install.XXXXXXXX")
staged=''
cleanup() { rm -rf "$work_dir"; if [ -n "$staged" ]; then rm -f "$staged"; fi; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
asset="sealos-console_${version}_${platform}_${arch}.tar.gz"
base="https://github.com/raymondtc/sealos-console/releases/download/$version"
fetch() { curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error --retry 2 --connect-timeout 15 --max-time 300 "$1" -o "$2"; }
fetch "$base/checksums.txt" "$work_dir/checksums.txt"
fetch "$base/$asset" "$work_dir/$asset"
expected=$(awk -v name="$asset" '$2 == name { print $1; count++ } END { if (count != 1) exit 1 }' "$work_dir/checksums.txt")
if [ "$checksum_tool" = sha256sum ]; then actual=$(sha256sum "$work_dir/$asset" | awk '{print $1}')
else actual=$(shasum -a 256 "$work_dir/$asset" | awk '{print $1}'); fi
[ "$actual" = "$expected" ] || { echo 'Checksum mismatch; existing installation was not changed.' >&2; exit 1; }
# Extract only the known executable, not arbitrary archive paths.
tar -xzf "$work_dir/$asset" -C "$work_dir" sealos-console
[ -f "$work_dir/sealos-console" ] && [ ! -L "$work_dir/sealos-console" ] || { echo 'Release executable is invalid.' >&2; exit 1; }
chmod 755 "$work_dir/sealos-console"
[ "$("$work_dir/sealos-console" --version)" = "$version" ] || { echo 'Release version mismatch.' >&2; exit 1; }
mkdir -p "$install_dir"
[ ! -d "$install_dir/sealos-console" ] || { echo 'Destination is a directory.' >&2; exit 1; }
staged=$(mktemp "$install_dir/.sealos-console.XXXXXXXX")
cp "$work_dir/sealos-console" "$staged"
chmod 755 "$staged"
# Rename replaces a prior symlink without writing into its target.
mv -f "$staged" "$install_dir/sealos-console"
staged=''
printf 'Installed sealos-console %s to %s\n' "$version" "$install_dir/sealos-console"
case ":$PATH:" in *":$install_dir:"*) ;; *) printf 'Add this directory to PATH: %s\n' "$install_dir" ;; esac
echo 'Run sealos-console skill install --agent codex (or your harness ID) to install the skill. Login profiles are preserved.'
