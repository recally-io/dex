#!/bin/sh
set -eu

fail() {
	printf 'dex-install: %s\n' "$*" >&2
	exit 1
}

as_root() {
	if [ "$(id -u)" -eq 0 ]; then
		"$@"
	else
		sudo "$@"
	fi
}

main() {
	[ "$#" -eq 3 ] || fail 'copy the complete install command from Dex'
	origin=$1
	environment=$2
	code=$3
	state=/var/lib/dex-agent
	binary=/usr/local/bin/dex-agent
	unit=/etc/systemd/system/dex-agent.service
	repo=https://github.com/recally-io/dex

	case $origin in https://?*) ;; *) fail 'the Dex address must start with https://' ;; esac
	case $environment in '' | *[!a-zA-Z0-9-]*) fail 'invalid environment ID' ;; esac
	[ "$(uname -s)" = Linux ] || fail 'this installer requires Linux'
	case $(uname -m) in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) fail "unsupported architecture: $(uname -m)" ;;
	esac
	for tool in curl tar sha256sum systemctl getent install runuser useradd; do
		command -v "$tool" >/dev/null || fail "$tool is required"
	done
	[ -d /run/systemd/system ] || fail 'this installer requires a running systemd system manager'
	if [ "$(id -u)" -ne 0 ]; then
		command -v sudo >/dev/null || fail 'use an account with sudo, or run the command as root'
		sudo -v </dev/tty || fail 'sudo authentication failed'
	fi
	if as_root test -e "$state/device.json"; then
		fail 'this machine is already connected; revoke it in Dex before connecting again'
	fi
	# A development build serves its own archives; a release publishes them on GitHub.
	if curl -fsSL -o /dev/null "$origin/machine-release/checksums.txt"; then
		version=v0.0.0-dev
		base="$origin/machine-release"
	else
		url=$(curl -fsSLo /dev/null -w '%{url_effective}' "$repo/releases/latest") || fail 'cannot find the latest Dex release'
		version=${url##*/}
		base="$repo/releases/download/$version"
	fi
	case $version in *[!a-zA-Z0-9.+-]*) fail 'invalid release tag' ;; v[0-9]*) ;; *) fail 'no Dex release found' ;; esac

	archive="dex-agent_${version}_linux_${arch}.tar.gz"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	printf 'Downloading the Dex machine service (%s, %s)…\n' "$version" "$arch"
	curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "this release has no $arch machine archive, or the download failed"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail 'cannot download the release checksums'
	want=$(awk -v file="$archive" '$2 == file { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail 'the release checksum is missing'
	printf '%s  %s\n' "$want" "$archive" | (cd "$tmp" && sha256sum -c --status) || fail 'the archive checksum does not match'
	tar -xzf "$tmp/$archive" -C "$tmp" dex-agent dex-agent.service

	if ! getent passwd dex-agent >/dev/null; then
		as_root useradd --system --user-group --home-dir "$state" --shell /usr/sbin/nologin dex-agent
	fi
	as_root install -d -m 0700 -o dex-agent -g dex-agent "$state"
	as_root install -m 0755 "$tmp/dex-agent" "$binary"
	as_root install -m 0644 "$tmp/dex-agent.service" "$unit"
	# The code goes through stdin, so it stays out of the process list.
	printf '%s\n' "$code" | as_root runuser -u dex-agent -- "$binary" enroll -state "$state" -url "$origin" -id "$environment" || fail 'Dex refused the enrollment; get a fresh install command'
	unset code
	as_root systemctl daemon-reload
	as_root systemctl enable --now dex-agent.service || fail 'the machine service did not start; read its log with: sudo journalctl -u dex-agent'
	printf 'Connected. The machine comes online in Dex within a few seconds.\n'
}

main "$@"
