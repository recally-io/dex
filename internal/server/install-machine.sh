#!/bin/sh
set -eu

fail() {
	printf 'dex-install: %s\n' "$*" >&2
	exit 1
}

note() {
	printf 'dex-install: %s\n' "$*" >&2
}

# The installer runs the machine as the account that installs it, by default. That account has the
# PATH, the tools, the logins, and the projects that the work needs, and its work can read its
# home. A host that serves several people
# installs a dedicated account instead, with --dedicated, as root.
main() {
	[ "$#" -ge 3 ] || fail 'copy the complete install command from Dex'
	origin=$1
	runner=$2
	code=$3
	shift 3
	mode=user
	for arg in "$@"; do
		case $arg in
			--dedicated) mode=dedicated ;;
			--user) mode=user ;;
			*) fail "unknown option: $arg (expected --user or --dedicated)" ;;
		esac
	done
	repo=https://github.com/recally-io/dex

	case $origin in https://?*) ;; *) fail 'the Dex address must start with https://' ;; esac
	case $runner in '' | *[!a-zA-Z0-9-]*) fail 'invalid Runner ID' ;; esac
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
		if [ "$mode" = user ]; then
			install_linux_user
		else
			install_linux_dedicated
		fi
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

# linger_status reports whether the account keeps its user services after it logs out. It prints
# nothing when the host does not run logind.
linger_status() {
	loginctl show-user "$1" --property=Linger --value 2>/dev/null || true
}

# install_linux_user installs the machine for the account that runs the command: a systemd user
# service that lingers after the last session ends. It needs no root, and root would run the work
# of the account of the service manager, which is no account of a person.
install_linux_user() {
	[ "$(id -u)" -ne 0 ] || fail 'run this command as the account that will use the machine, without sudo. A host that serves several people installs a dedicated account as root, with --dedicated.'
	for tool in curl tar install systemctl sed; do
		command -v "$tool" >/dev/null || fail "$tool is required"
	done
	systemctl --user show-environment >/dev/null 2>&1 || fail 'this machine has no systemd user session for this account: log in to the machine and run the command again, or install a dedicated account with --dedicated as root'
	user=$(id -un)
	[ -n "${HOME:-}" ] && [ -d "$HOME" ] || fail "cannot find the home directory of $user"
	state=${XDG_STATE_HOME:-$HOME/.local/state}/dex-agent
	binary=${XDG_BIN_HOME:-$HOME/.local/bin}/dex-agent
	unit=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/dex-agent.service
	[ ! -e "$state/device.json" ] || fail 'this machine is already connected for this account; revoke it in Dex before connecting again'
	# A machine that a dedicated account already serves stays as it is: a second install of the
	# same host would connect twice, and the existing machine is not migrated without saying so.
	if systemctl list-unit-files dex-agent.service 2>/dev/null | grep -q '^dex-agent\.service'; then
		fail 'this host already runs the machine as the dedicated account dex-agent. Keep it, or move it: sudo systemctl disable --now dex-agent.service, remove /var/lib/dex-agent (including /var/lib/dex-agent/bin/dex-agent) and /etc/systemd/system/dex-agent.service, then run this command again. See the machine section of the Dex docs for the whole procedure.'
	fi
	tar -xzf "$tmp/$archive" -C "$tmp" dex-agent dex-agent-user.service
	install -d -m 0700 "$state"
	install -d -m 0755 "${binary%/*}" "${unit%/*}"
	install -m 0755 "$tmp/dex-agent" "$binary"
	sed -e "s|__BINARY__|$binary|g" -e "s|__STATE__|$state|g" "$tmp/dex-agent-user.service" >"$tmp/dex-agent-user.service.final"
	install -m 0644 "$tmp/dex-agent-user.service.final" "$unit"
	# The code goes through stdin, so it stays out of the process list.
	printf '%s\n' "$code" | "$binary" enroll -state "$state" -url "$origin" -id "$runner" -mode user || fail 'Dex refused the enrollment; get a fresh install command'
	systemctl --user daemon-reload
	systemctl --user enable --now dex-agent.service || fail "the machine service did not start; read its log with: journalctl --user -u dex-agent"
	# A user service stops with the last session of the account unless the account lingers.
	if command -v loginctl >/dev/null; then
		[ "$(linger_status "$user")" = yes ] || loginctl enable-linger "$user" >/dev/null 2>&1 || true
		if [ "$(linger_status "$user")" != yes ]; then
			note "the machine is connected but stops when $user logs out: run sudo loginctl enable-linger $user"
		fi
	else
		note 'loginctl is unavailable. The machine can stop when this account logs out.'
	fi
	note "the machine runs as $user, so its work can read the files of $user. Check it with: $binary status"
}

# install_linux_dedicated keeps the machine in a system account that holds no login state of a
# person, for a host that several people use.
install_linux_dedicated() {
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
	if [ "$(id -u)" -ne 0 ]; then
		command -v sudo >/dev/null || fail 'use an account with sudo, or run the command as root'
		sudo -v </dev/tty || fail 'sudo authentication failed'
	fi
	as_root systemctl show --property=Version >/dev/null 2>&1 || fail 'this installer requires a running systemd system manager'
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
	as_root install -d -m 0700 -o dex-agent -g dex-agent "$state"
	# Do not write as root below the directory owned by dex-agent.
	as_root runuser -u dex-agent -- install -d -m 0700 "$state/bin"
	as_root runuser -u dex-agent -- sh -eu -c '
		stage=$(mktemp "${1%/*}/.install-XXXXXX")
		trap '\''rm -f "$stage"'\'' EXIT
		cat >"$stage"
		chmod 0755 "$stage"
		mv -fT "$stage" "$1"
	' sh "$binary" <"$tmp/dex-agent"
	as_root install -m 0644 "$tmp/dex-agent.service" "$unit"
	printf '%s\n' "$code" | as_root runuser -u dex-agent -- "$binary" enroll -state "$state" -url "$origin" -id "$runner" -mode dedicated || fail 'Dex refused the enrollment; get a fresh install command'
	as_root systemctl daemon-reload
	as_root systemctl enable --now dex-agent.service || fail 'the machine service did not start; read its log with: sudo journalctl -u dex-agent'
	note 'the machine runs as the dedicated account dex-agent'
}

# On macOS the LaunchAgent runs in the login session of the current account.
install_darwin() {
	[ "$(id -u)" -ne 0 ] || fail 'run this command without sudo as the account that will use the machine'
	[ "$mode" = user ] || fail '--dedicated is only available on Linux. On macOS, run the install command as the account that will use the machine.'
	for tool in curl tar install launchctl; do
		command -v "$tool" >/dev/null || fail "$tool is required"
	done
	target_user=$(id -un)
	[ -n "${HOME:-}" ] && [ -d "$HOME" ] || fail "cannot find the home directory of $target_user"
	state="$HOME/Library/Application Support/dex-agent"
	binary="$HOME/.local/bin/dex-agent"
	plist="$HOME/Library/LaunchAgents/com.recally.dex-agent.plist"
	log="$HOME/Library/Logs/dex-agent.log"
	label=com.recally.dex-agent
	target_uid=$(id -u)
	[ ! -e "$state/device.json" ] || fail 'this machine is already connected; revoke it in Dex before connecting again'
	tar -xzf "$tmp/$archive" -C "$tmp" dex-agent com.recally.dex-agent.plist
	install -d -m 0700 "$state" "$HOME/.local/bin" "$HOME/Library/Logs" "${plist%/*}"
	install -m 0755 "$tmp/dex-agent" "$binary"
	sed -e "s|__BINARY__|$binary|g" -e "s|__STATE__|$state|g" -e "s|__LOG__|$log|g" "$tmp/com.recally.dex-agent.plist" >"$tmp/com.recally.dex-agent.plist.final"
	install -m 0644 "$tmp/com.recally.dex-agent.plist.final" "$plist"
	printf '%s\n' "$code" | "$binary" enroll -state "$state" -url "$origin" -id "$runner" -mode user || fail 'Dex refused the enrollment; get a fresh install command'
	# bootstrap refuses a label that is already loaded, so unload it first.
	launchctl bootout "gui/$target_uid/$label" 2>/dev/null || true
	launchctl bootstrap "gui/$target_uid" "$plist" || fail "the agent did not start; read $log"
	note "the machine runs as $target_user, so its work can read the files of $target_user. Check it with: $binary status"
}

main "$@"
