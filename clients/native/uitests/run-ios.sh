#!/bin/bash
set -euo pipefail
umask 077

if [[ $# != 7 ]]; then
  printf 'Usage: bash %s APP FIXTURE_DIR TLS_PRIMARY TLS_ALTERNATE TLS_NO_TOKEN TLS_NAVIGATION OUTPUT_DIR\n' "$0" >&2
  exit 2
fi
app=$(cd "$1" && pwd)
fixture=$(cd "$2" && pwd)
primary=$3
alternate=$4
open=$5
navigation=$6
mkdir -p "$7"
output=$(cd "$7" && pwd)
source=$(cd "$(dirname "$0")" && pwd)
bundle=$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Info.plist")
platform=$(/usr/libexec/PlistBuddy -c 'Print CFBundleSupportedPlatforms:0' "$app/Info.plist")
[[ "$platform" == iPhoneSimulator ]] || { printf 'Use an iOS Simulator app.\n' >&2; exit 2; }
for origin in "$primary" "$alternate" "$open" "$navigation"; do
  [[ "$origin" =~ ^https://127\.0\.0\.1:[0-9]+$ ]] || exit 2
done
[[ "$(basename "$fixture")" == dex-apple-qa.* && -f "$fixture/ca.pem" ]] || exit 2
token=$(< "$fixture/token")
[[ "$token" =~ ^[A-Za-z0-9_-]{32,256}$ ]] || exit 2

work=$(mktemp -d "${TMPDIR:-/tmp}/dex-apple-ui.XXXXXXXX")
device=
cleanup() {
  if [[ -n "$device" ]]; then
    xcrun simctl shutdown "$device" >/dev/null 2>&1 || true
    xcrun simctl delete "$device" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cp "$source/project.yml" "$source/AppleSmoke.swift" "$source/MacSmoke.swift" "$work/"
plutil -create xml1 "$work/Fixture.plist"
plutil -insert bundle -string "$bundle" "$work/Fixture.plist"
plutil -insert primary -string "$primary" "$work/Fixture.plist"
plutil -insert alternate -string "$alternate" "$work/Fixture.plist"
plutil -insert open -string "$open" "$work/Fixture.plist"
plutil -insert navigation -string "$navigation" "$work/Fixture.plist"
plutil -insert token -string "$token" "$work/Fixture.plist"
(cd "$work" && xcodegen generate)

runtime=${QA_SIM_RUNTIME:-com.apple.CoreSimulator.SimRuntime.iOS-26-5}
type=${QA_SIM_DEVICE_TYPE:-com.apple.CoreSimulator.SimDeviceType.iPhone-17}
device=$(xcrun simctl create DexAppleQA "$type" "$runtime")
xcrun simctl boot "$device"
xcrun simctl bootstatus "$device" -b > "$output/sim-boot.log" 2>&1
xcrun simctl install "$device" "$app"

xcode=(xcodebuild -project "$work/DexAppleQA.xcodeproj" -scheme DexAppleQA
  -destination "platform=iOS Simulator,id=$device" -derivedDataPath "$work/derived"
  -parallel-testing-enabled NO -collect-test-diagnostics never)

run_test() {
  local label=$1 test=$2 status=0 result="$work/$1.xcresult"
  local export_path="$work/$1-attachments"
  "${xcode[@]}" -resultBundlePath "$result" -testLanguage en \
    -only-testing:"DexAppleQA/AppleSmoke/$test" test-without-building > "$work/$label.log" 2>&1 || status=$?
  QA_SECRET="$token" perl -pe 's/\Q$ENV{QA_SECRET}\E/[QA token removed]/g' "$work/$label.log" > "$output/$label.log"
  if [[ -d "$result" ]]; then
    xcrun xcresulttool get test-results summary --path "$result" > "$work/$label.json" 2>/dev/null || true
    QA_SECRET="$token" perl -pe 's/\Q$ENV{QA_SECRET}\E/[QA token removed]/g' "$work/$label.json" > "$output/$label.json"
    mkdir "$export_path"
    if xcrun xcresulttool export attachments --path "$result" --filter '*.png' --output-path "$export_path" >/dev/null; then
      jq -r '.[] | .attachments[] | select(.suggestedHumanReadableName | test("^[0-9]{2}-[a-z-]+")) | [.exportedFileName, .suggestedHumanReadableName] | @tsv' "$export_path/manifest.json" |
        while IFS=$'\t' read -r blob name; do
          cp "$export_path/$blob" "$output/$name"
        done
    fi
  fi
  return "$status"
}

"${xcode[@]}" build-for-testing > "$output/ui-build.log" 2>&1
run_test untrusted testUserCannotConnectToUntrustedTLS || untrusted=$?
# Clear app state from the negative test. Trust only this new simulator.
xcrun simctl uninstall "$device" "$bundle"
xcrun simctl keychain "$device" add-root-cert "$fixture/ca.pem"
xcrun simctl install "$device" "$app"
run_test trusted testUserConnectsSignsInAndCatchesUpAfterBackground || trusted=$?
xcrun simctl uninstall "$device" "$bundle"
xcrun simctl install "$device" "$app"
run_test switch testUserSwitchesAndForgetsTheServer || switched=$?
xcrun simctl uninstall "$device" "$bundle"
xcrun simctl install "$device" "$app"
printf 'P3\n2 2\n255\n230 30 30 30 230 30\n30 30 230 250 250 250\n' > "$work/file.ppm"
sips -s format png "$work/file.ppm" --out "$work/dex-qa-image.png" >/dev/null
xcrun simctl addmedia "$device" "$work/dex-qa-image.png"
run_test attachment testUserAttachesAndSendsAnImage || attached=$?
xcrun simctl uninstall "$device" "$bundle"
xcrun simctl install "$device" "$app"
run_test navigation testUserSeesDifferentPromptsForLinksAndSilentOAuth || navigated=$?
(( ${untrusted:-0} == 0 && ${trusted:-0} == 0 && ${switched:-0} == 0 && ${attached:-0} == 0 && ${navigated:-0} == 0 ))
