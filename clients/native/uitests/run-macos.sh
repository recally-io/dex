#!/bin/bash
set -euo pipefail
umask 077

if [[ $# != 2 || ! $1 =~ ^http://127\.0\.0\.1:[0-9]+$ || $1 == http://127.0.0.1:8787 ]]; then
  printf 'Usage: bash %s QA_LOOPBACK_URL OUTPUT_DIR\n' "$0" >&2
  exit 2
fi
if lsof -nP -iTCP:8787 -sTCP:LISTEN >/dev/null; then
  printf 'Port 8787 is in use. Stop the check without launching the app.\n' >&2
  exit 2
fi
local_url=$1
mkdir -p "$2"
output=$(cd "$2" && pwd)
source=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/dex-apple-mac.XXXXXXXX")
gate_pid=
cleanup() {
  if [[ -n "$gate_pid" ]]; then
    kill "$gate_pid" 2>/dev/null || true
    wait "$gate_pid" 2>/dev/null || true
  fi
  rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

http=(env -i PATH=/usr/bin:/bin curl -q --silent --noproxy '*' --max-time 2 --fail)
"${http[@]}" "$local_url/api/bots/default" | jq -e '.name == "Dex QA" and .provider == "faux" and .modelId == "echo"' >/dev/null
mkdir "$work/home" "$work/gate"
# A separate app identifier keeps the QA WebKit store out of the installed Dex app.
id="io.recally.dex.qa.mac.$(uuidgen | tr '[:upper:]' '[:lower:]')"
(
  cd "$source/.."
  unset NSGO_NOTARY_KEY
  mise exec go@1.27.3 -- go tool nsgo build -target macos_arm64 -id "$id" \
    -channel none -installers=false -open=false -sign=- -notarize= -output "$work/build"
) > "$output/mac-build.log" 2>&1
app=$(find "$work/build" -type d -name Dex.app -print -quit)
[[ -n "$app" ]]
cp "$source/project.yml" "$source/AppleSmoke.swift" "$source/MacSmoke.swift" "$work/"
plutil -create xml1 "$work/Fixture.plist"
plutil -insert app -string "$app" "$work/Fixture.plist"
plutil -insert home -string "$work/home" "$work/Fixture.plist"
plutil -insert gate -string "$work/gate" "$work/Fixture.plist"
(cd "$work" && xcodegen generate)

printf '{\n  admin off\n  auto_https off\n}\nhttp://127.0.0.1:8787 {\n  bind 127.0.0.1\n  reverse_proxy %s\n}\n' \
  "${local_url#http://}" > "$work/gate/Caddyfile"
proxy=$(command -v caddy)
(
  proxy_pid=
  stop_proxy() {
    if [[ -n "$proxy_pid" ]]; then
      kill "$proxy_pid" 2>/dev/null || true
      wait "$proxy_pid" 2>/dev/null || true
    fi
  }
  trap stop_proxy EXIT
  trap 'exit 143' TERM
  until [[ -f "$work/gate/start-default" ]]; do sleep 0.2; done
  if lsof -nP -iTCP:8787 -sTCP:LISTEN >/dev/null; then
    exit 2
  fi
  env -i HOME="$work/home" PATH=/usr/bin:/bin "$proxy" run --config "$work/gate/Caddyfile" --adapter caddyfile &
  proxy_pid=$!
  until lsof -nP -a -p "$proxy_pid" -iTCP:8787 -sTCP:LISTEN >/dev/null \
    && "${http[@]}" http://127.0.0.1:8787/api/health >/dev/null; do
    kill -0 "$proxy_pid"
    sleep 0.2
  done
  touch "$work/gate/ready-default"
  wait "$proxy_pid"
) > "$output/mac-gate.log" 2>&1 &
gate_pid=$!

result="$work/mac.xcresult"
status=0
xcodebuild -project "$work/DexAppleQA.xcodeproj" -scheme DexMacQA -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath "$work/derived" -parallel-testing-enabled NO -collect-test-diagnostics never \
  -resultBundlePath "$result" -testLanguage en CODE_SIGN_IDENTITY=- test > "$output/mac-ui.log" 2>&1 || status=$?
if [[ -d "$result" ]]; then
  xcrun xcresulttool get test-results summary --path "$result" > "$output/mac-ui.json" 2>/dev/null || true
  mkdir "$work/images"
  if xcrun xcresulttool export attachments --path "$result" --filter '*.png' --output-path "$work/images" >/dev/null; then
    jq -r '.[] | .attachments[] | select(.suggestedHumanReadableName | test("^[0-9]{2}-mac-[a-z-]+")) | [.exportedFileName, .suggestedHumanReadableName] | @tsv' "$work/images/manifest.json" |
      while IFS=$'\t' read -r blob name; do cp "$work/images/$blob" "$output/$name"; done
  fi
fi
exit "$status"
