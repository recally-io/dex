# Apple UI acceptance

Run this check on an Apple silicon Mac with the project tools, Xcode, an iOS Simulator runtime, XcodeGen, Caddy, OpenSSL and jq. Use only the QA fixture. Do not point these tests at an installed Dex instance.

## Start the fixture

From the repository root, run:

```sh
mise exec -- bash clients/native/uitests/fixture.sh
```

Wait for `QA_READY`. Keep the process running. The process prints its temporary directory and five app-facing URLs. It does not print the QA token or certificate keys. Stop the process with Control-C to remove its data, keys and temporary homes.

The two Dex processes use fresh data and `faux/echo`. The token process uses `--require-token --stored-token`. Caddy adds `Secure` to `dex_session` on the HTTPS routes. The fixture checks the unauthenticated health status at the upstream and the proxies. The navigation origin serves a small QA page at `/` and uses the authenticated Dex upstream for `/api/health`.

## Test iOS

Build with `mise run apple:build`. In a second terminal, pass the fixture values to the runner:

```sh
mise exec -- bash clients/native/uitests/run-ios.sh \
  .local/apple-v1/build/dev/Dex-0.1.0-ios_simulator/Dex.app \
  "$fixture_dir" "$primary" "$alternate" "$no_token" "$navigation" \
  .local/apple-v1/qa/ios
```

The runner makes and deletes its own simulator. It tests TLS before it adds the QA root to that simulator. It does not change host trust. Use `QA_SIM_RUNTIME` and `QA_SIM_DEVICE_TYPE` if the default iOS 26.5 runtime or iPhone 17 type is not available. Set `QA_IOS_ONLY=attachment` before the command to run only the Photos picker and attachment test.

The runner keeps test summaries and selected screenshots. It keeps the token, raw result bundles and temporary signing files out of the output directory. Use a new output directory for each run.

## Test macOS

Keep the fixture running and leave the default port `8787` free. In a second terminal, run:

```sh
mise exec -- bash clients/native/uitests/run-macos.sh \
  "$local_url" .local/apple-v1/qa/macos
```

The runner stops if port `8787` is in use. It builds an ad hoc app with a new bundle ID and launches it with a temporary home. Its proxy holds port `8787`, first returns `503`, then lets Retry reach the local echo fixture. The host must already allow XCTest UI automation. If XCUI cannot start, keep the failure log and do not change system settings for this check.
