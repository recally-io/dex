#!/bin/sh
# Install the dex release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/recally-io/dex/main/install.sh | sh
#
# DEX_VERSION selects a tag such as v0.1.0; the default is the latest stable release.
# DEX_INSTALL_DIR selects the directory; the default is ~/.local/bin. Use a permanent directory,
# because the service runs dex from where it is installed.
set -eu

repo=recally-io/dex
dir=${DEX_INSTALL_DIR:-$HOME/.local/bin}

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS: $(uname -s)" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported architecture: $(uname -m)" ;;
esac
command -v curl >/dev/null || fail "curl is required"

version=${DEX_VERSION:-}
if [ -z "$version" ]; then
	# The latest-release page redirects to its tag. This avoids the API rate limit.
	url=$(curl -fsSLo /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
		fail "cannot find the latest release"
	version=${url##*/}
fi
case $version in v*) ;; *) fail "no release found (got '$version')" ;; esac

archive="dex_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading dex $version for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "cannot download $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "cannot download checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt has no entry for $archive"
if command -v sha256sum >/dev/null; then
	got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" dex
mkdir -p "$dir"
# Write beside the target and rename, so a running dex keeps its old file.
cp "$tmp/dex" "$dir/.dex.tmp"
chmod 755 "$dir/.dex.tmp"
mv -f "$dir/.dex.tmp" "$dir/dex"
echo "Installed $("$dir/dex" --version | head -n 1) to $dir/dex"

case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Add $dir to PATH in your shell profile, then open a new shell." ;;
esac
echo "Next: run 'dex service install' to start dex now and at login."
