#!/bin/sh
set -eu

fail() {
	printf 'dex-install: %s\n' "$*" >&2
	exit 1
}

main() {
	[ "$#" -eq 3 ] || fail 'copy the complete install command from Dex'
	origin=$1
	environment=$2
	code=$3
	repo=https://github.com/recally-io/dex

	case $origin in https://?*) ;; *) fail 'the Dex address must start with https://' ;; esac
	case $environment in '' | *[!a-zA-Z0-9-]*) fail 'invalid environment ID' ;; esac
	case $(uname -s) in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) fail "unsupported system: $(uname -s)" ;;
	esac
	case $(uname -m) in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) fail "unsupported architecture: $(uname -m)" ;;
	esac

	# Dex sets this tag when it serves the installer.
	version='__DEX_MACHINE_TAG__'
	if [ "$version" = v0.0.0-dev ]; then
		base="$origin/machine-release"
	else
		base="$repo/releases/download/$version"
	fi

	archive="dex-agent_${version}_${os}_${arch}.tar.gz"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	printf 'Downloading the Dex machine service (%s, %s, %s)…\n' "$version" "$os" "$arch"
	curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "this release has no $os $arch machine archive, or the download failed"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail 'cannot download the release checksums'
	verify_checksum "$tmp" "$archive" || fail 'the archive checksum does not match'

	if [ "$os" = linux ]; then
		install_linux
	else
		install_darwin
	fi
	unset code
	printf 'Connected. The machine comes online in Dex within a few seconds.\n'
}

# macOS has a BSD sha256sum without --status, so the digests are compared directly.
verify_checksum() {
	want=$(awk -v file="$2" '$2 == file { print $1 }' "$1/checksums.txt")
	[ -n "$want" ] || return 1
	if command -v sha256sum >/dev/null; then
		got=$(cd "$1" && sha256sum "$2" | awk '{ print $1 }')
	else
		got=$(cd "$1" && shasum -a 256 "$2" | awk '{ print $1 }')
	fi
	[ "$got" = "$want" ]
}

# The binary runs as an unprivileged account on both systems. The target account owns the
# token and the workspace, and the service manager restarts the agent after a failure.
install_linux() {
	as_root() {
		if [ "$(id -u)" -eq 0 ]; then
			"$@"
		else
			sudo "$@"
		fi
	}
	for tool in curl tar sha256sum systemctl getent install runuser useradd; do
		command -v "$tool" >/dev/null || fail "$tool is required"
	done
	[ -d /run/systemd/system ] || fail 'this installer requires a running systemd system manager'
	if [ "$(id -u)" -ne 0 ]; then
		command -v sudo >/dev/null || fail 'use an account with sudo, or run the command as root'
		sudo -v </dev/tty || fail 'sudo authentication failed'
	fi
	state=/var/lib/dex-agent
	binary=/var/lib/dex-agent/bin/dex-agent
	unit=/etc/systemd/system/dex-agent.service
	if as_root test -e "$state/device.json"; then
		fail 'this machine is already connected; revoke it in Dex before connecting again'
	fi
	tar -xzf "$tmp/$archive" -C "$tmp" dex-agent dex-agent.service
	if ! getent passwd dex-agent >/dev/null; then
		as_root useradd --system --user-group --home-dir "$state" --shell /usr/sbin/nologin dex-agent
	fi
	as_root install -d -m 0700 -o dex-agent -g dex-agent "$state" "$state/bin"
	as_root install -m 0755 -o dex-agent -g dex-agent "$tmp/dex-agent" "$binary"
	as_root install -m 0644 "$tmp/dex-agent.service" "$unit"
	# The code goes through stdin, so it stays out of the process list.
	printf '%s\n' "$code" | as_root runuser -u dex-agent -- "$binary" enroll -state "$state" -url "$origin" -id "$environment" || fail 'Dex refused the enrollment; get a fresh install command'
	as_root systemctl daemon-reload
	as_root systemctl enable --now dex-agent.service || fail 'the machine service did not start; read its log with: sudo journalctl -u dex-agent'
}

# A Mac is a single-user machine, so the agent runs in the login session of the account that
# installed it. No system directory and no separate account is needed.
install_darwin() {
	target_user=${SUDO_USER:-$(id -un)}
	[ "$target_user" != root ] || fail 'run this command as your own account, not as root'
	for tool in curl tar install launchctl; do
		command -v "$tool" >/dev/null || fail "$tool is required"
	done
	target_home=$(eval echo "~$target_user")
	[ -d "$target_home" ] || fail "cannot find the home directory of $target_user"
	state="$target_home/Library/Application Support/dex-agent"
	binary="$target_home/.local/bin/dex-agent"
	plist="$target_home/Library/LaunchAgents/com.recally.dex-agent.plist"
	log="$target_home/Library/Logs/dex-agent.log"
	label=com.recally.dex-agent
	target_uid=$(id -u "$target_user")
	run_as_user() {
		if [ "$(id -u)" -eq 0 ]; then
			sudo -u "$target_user" "$@"
		else
			"$@"
		fi
	}
	[ ! -e "$state/device.json" ] || fail 'this machine is already connected; revoke it in Dex before connecting again'
	tar -xzf "$tmp/$archive" -C "$tmp" dex-agent com.recally.dex-agent.plist
	run_as_user install -d -m 0700 "$state" "$target_home/.local/bin" "$target_home/Library/Logs" "${plist%/*}"
	run_as_user install -m 0755 "$tmp/dex-agent" "$binary"
	run_as_user install -m 0644 "$tmp/com.recally.dex-agent.plist" "$plist"
	run_as_user sed -i '' -e "s|__BINARY__|$binary|g" -e "s|__STATE__|$state|g" -e "s|__LOG__|$log|g" "$plist"
	printf '%s\n' "$code" | run_as_user "$binary" enroll -state "$state" -url "$origin" -id "$environment" || fail 'Dex refused the enrollment; get a fresh install command'
	# bootstrap refuses a label that is already loaded, so unload it first.
	launchctl bootout "gui/$target_uid/$label" 2>/dev/null || true
	launchctl bootstrap "gui/$target_uid" "$plist" || fail "the agent did not start; read $log"
}

main "$@"
