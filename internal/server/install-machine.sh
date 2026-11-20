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

# pin_curl fetches from Dex with the pinned certificate key. The certificate is self-signed,
# so the pin replaces the CA check.
pin_curl() {
	curl -fsSk --pinnedpubkey "sha256//$pin" "$@"
}

main() {
	[ "$#" -ge 4 ] && [ "$#" -le 5 ] || fail 'copy the complete install command from Dex'
	origin=$1
	environment=$2
	pin=$3
	code=$4
	version=${5:-}
	state=/var/lib/dex-runner
	binary=/usr/local/bin/dex-runner
	unit=/etc/systemd/system/dex-runner.service
	repo=https://github.com/recally-io/dex

	case $origin in https://?*) ;; *) fail 'the Dex address must start with https://' ;; esac
	case $environment in '' | *[!a-zA-Z0-9-]*) fail 'invalid environment ID' ;; esac
	case $pin in '' | *[!A-Za-z0-9+/=]*) fail 'invalid Dex certificate pin' ;; esac
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
	if [ "$version" = dev ]; then
		base="$origin/machine-release"
		version=v0.0.0-dev
	elif [ -z "$version" ]; then
		url=$(curl -fsSLo /dev/null -w '%{url_effective}' "$repo/releases/latest") || fail 'cannot find the latest Dex release'
		version=${url##*/}
	fi
	case $version in *[!a-zA-Z0-9.+-]*) fail 'invalid release tag' ;; v[0-9]*) ;; *) fail 'no Dex release found' ;; esac

	archive="dex-runner_${version}_linux_${arch}.tar.gz"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	printf 'Downloading the Dex machine service (%s, %s)…\n' "$version" "$arch"
	if [ "$version" = v0.0.0-dev ]; then
		pin_curl -o "$tmp/$archive" "$base/$archive" || fail 'this development build has no machine archive'
		pin_curl -o "$tmp/checksums.txt" "$base/checksums.txt" || fail 'this development build has no checksums'
	else
		curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "this release has no $arch machine archive, or the download failed"
		curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail 'cannot download the release checksums'
	fi
	want=$(awk -v file="$archive" '$2 == file { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail 'the release checksum is missing'
	printf '%s  %s\n' "$want" "$archive" | (cd "$tmp" && sha256sum -c --status) || fail 'the archive checksum does not match'
	tar -xzf "$tmp/$archive" -C "$tmp" dex-runner dex-runner.service

	if ! getent passwd dex-runner >/dev/null; then
		as_root useradd --system --user-group --home-dir "$state" --shell /usr/sbin/nologin dex-runner
	fi
	as_root install -d -m 0700 -o dex-runner -g dex-runner "$state"
	as_root install -m 0755 "$tmp/dex-runner" "$binary"
	as_root install -m 0644 "$tmp/dex-runner.service" "$unit"
	# The code goes through stdin, so it stays out of the process list.
	printf '%s\n' "$code" | as_root runuser -u dex-runner -- "$binary" enroll -state "$state" -url "$origin" -id "$environment" -server-pin "$pin" || fail 'Dex refused the enrollment; get a fresh install command'
	unset code
	as_root systemctl daemon-reload
	as_root systemctl enable --now dex-runner.service || fail 'the machine service did not start; read its log with: sudo journalctl -u dex-runner'
	printf 'Connected. The machine comes online in Dex within a few seconds.\n'
}

main "$@"
