# Dex for Apple

The app opens the workspace from an existing Dex server. It does not start Dex, bundle the web UI, or store the Dex access token in the app shell.

## Download the Mac app

Find `Dex-<tag>-macOS-arm64.zip` and `checksums.txt` on the same [GitHub Release](https://github.com/vaayne/dex/releases). The archive contains `Dex.app` for Apple silicon and macOS 14 or later. It does not include the Dex server. Unzip the archive and move `Dex.app` to Applications.

The app has an ad hoc signature. Apple has not notarized it. macOS can block it on first launch because it cannot verify the developer. See [Apple's guide to opening downloaded apps](https://support.apple.com/en-us/102445). The release does not include an installable iPhone app.

## Connect to Dex

- **Mac:** The initial address is `http://127.0.0.1:8787`. Use the port of your existing server if it differs. Plain HTTP is limited to `127.0.0.1` on macOS.
- **iPhone and iOS Simulator:** Enter a reachable HTTPS origin, such as `https://dex.example.com`. The app has no default address. An origin contains a scheme, host, and optional port. Do not include a path, query, fragment, or credentials.
- **Remote access:** Follow [Deploy an HTTPS entry point](../README.md#deploy-an-https-entry-point). The server must require a Dex token even when the reverse proxy uses a loopback upstream. Use a certificate the client trusts. Do not disable certificate checks or expose a plain HTTP listener to the network.

To change servers, open **Settings → General → Workspace → Switch workspace**. The sign-in page has the same link if you do not have the token for the selected server. If an older server does not show the link, open `dex-workspace://settings` on the device to open the app's connection screen. Opening the screen keeps the current login. If the server is offline, the app opens this screen directly.

Enter the access token on the Dex login page when it asks for one. The web view keeps its own login data across retries and restarts. Switching to another origin or using **Forget** clears that data. The web view blocks navigation outside the configured scheme, host, and port. If this blocks an OAuth flow, open the Dex origin in your browser and start the flow again there.

## Build for local development

Use an Apple silicon Mac and full Xcode. The app needs macOS 14.0 or later, or iOS 17.0 or later on a device or simulator. These versions support the named WebKit data store. Install a compatible iOS Simulator runtime and select the full Xcode toolchain before you build. The client module pins NSGo to `v0.3.4` and uses Go `1.27.3`. The Apple task selects this Go version without changing the server toolchain.

From the repository root:

```sh
mise install go@1.27.3
xcodebuild -version
xcrun simctl list runtimes
mise run apple:build
```

The task builds only `macos_arm64` and `ios_simulator`. It does not launch the app, install it, start a simulator, or contact Apple's signing services. The ad hoc signatures are for local use. They do not identify a developer. There is no device IPA or Mac installer in this build.

Find the two app bundles under `.local/apple-v1/build/dev/<name>-<version>-<target>/`. Rebuild before runtime checks if `nsgo.yaml` changes. Reinstall the new simulator app so the test uses its current `Info.plist`. To launch the apps, set `MAC_APP` and `SIM_APP` to the corresponding `.app` paths. Set `SIM_UDID` to the ID of an already booted iPhone simulator. Run these commands only on a simulator reserved for your work. The simulator install can replace an app with the same bundle ID.

```sh
open "${MAC_APP:?Set the Mac app path}"
xcrun simctl list devices booted
xcrun simctl install "${SIM_UDID:?Set the simulator ID}" "${SIM_APP:?Set the simulator app path}"
xcrun simctl launch "$SIM_UDID" "$(plutil -extract CFBundleIdentifier raw -o - "$SIM_APP/Info.plist")"
```

## Check the client

Run `mise run apple:test` for the connection rules and the macOS WebKit navigation and cookie checks. Follow [Apple UI acceptance](uitests/README.md) to run the app against disposable Dex services. Never use a production Dex with these tests.

## Build a signed IPA for a physical iPhone

Run the remaining build commands from `native/`. Keep all signing values local. Do not add team IDs, profile paths, profiles, certificates, private keys, or passwords to the repository.

Use a valid Apple Development signing identity with its private key in an unlocked keychain. Obtain an iOS development provisioning profile from the same team. The profile must authorize the bundle ID from `nsgo.yaml`, the signing certificate, and each test iPhone's UDID. For an Ad Hoc distribution IPA, use the matching Ad Hoc profile and Apple Distribution identity instead. Set `IOS_PROFILE` to the absolute path of the applicable `.mobileprovision` file. Set `IOS_SIGN_ID` to the exact identity name or SHA-1 fingerprint.

```sh
mise exec go@1.27.3 -- go tool nsgo build -target ios -channel none \
  -sign "${IOS_SIGN_ID:?Set the iOS signing identity}" \
  -profile "${IOS_PROFILE:?Set the iOS development or Ad Hoc profile path}" \
  -output ../.local/apple-v1/build/device
```

The build creates a signed iPhoneOS `.app` and an `.ipa` in the target directory. NSGo also copies the IPA and its SHA-256 checksum to the output's `releases/` directory. Verify the app before you keep or hand off the IPA. Set `DEVICE_APP` to the `.app` path from that build:

```sh
codesign --verify --deep --strict "${DEVICE_APP:?Set the iPhoneOS app path}"
codesign -dvv "$DEVICE_APP"
plutil -extract CFBundleSupportedPlatforms xml1 -o - "$DEVICE_APP/Info.plist"
test -f "$DEVICE_APP/embedded.mobileprovision"
```

Check for `iPhoneOS`, the expected signing authority, and the embedded profile. Keep the IPA and checksum as the distribution archive. Without `-profile` NSGo can produce an unsigned IPA. That IPA cannot be installed or uploaded. NSGo 0.3.4 does not produce a literal Xcode `.xcarchive`. An Organizer archive requires a separate Xcode host project and archive flow, which this client does not ship.

The command does not need a connected phone. It does not use `-run`. A future device test needs a provisioned iPhone. For a development-signed app, also enable Developer Mode. A build and signature check do not prove device install or launch.

## Developer ID for Mac (reference only)

Use a `Developer ID Application` identity with its private key in the keychain. Set `MAC_SIGN_ID` to that identity. Set `MAC_NOTARY_PROFILE` to an existing `notarytool` keychain profile for the team. If necessary, create that profile with the first command below. It prompts for the app-specific password. Skip that command when the profile already exists. Unset `NSGO_NOTARY_KEY` when you want to use the keychain profile instead of ambient API credentials.

```sh
xcrun notarytool store-credentials "${MAC_NOTARY_PROFILE:?Set the notary profile name}" \
  --apple-id "${APPLE_ID:?Set the Apple account}" --team-id "${APPLE_TEAM_ID:?Set the team ID}"

unset NSGO_NOTARY_KEY
mise exec go@1.27.3 -- go tool nsgo build -target macos_arm64 -channel direct \
  -installers=true -skip-installers= \
  -sign "${MAC_SIGN_ID:?Set the Developer ID Application identity}" \
  -notarize "${MAC_NOTARY_PROFILE:?Set the notary profile name}" \
  -output ../.local/apple-v1/build/developer-id
```

The build uses the hardened runtime and sends the package to Apple's notary service. After acceptance, NSGo staples a ticket to the app and disk image. Set `MAC_RELEASE_APP` to the `.app` in the target output and `MAC_DMG` to the `.dmg` in `releases/`. Verify both before distribution:

```sh
codesign --verify --deep --strict "${MAC_RELEASE_APP:?Set the signed Mac app path}"
xcrun stapler validate "$MAC_RELEASE_APP"
xcrun stapler validate "${MAC_DMG:?Set the notarized DMG path}"
```

## iOS TestFlight (reference only)

Register the bundle ID and create the matching App Store Connect app record. Use an App Store distribution provisioning profile and its Apple Distribution identity. A development or Ad Hoc IPA cannot be used. The build uses the same 1024 × 1024 [app icon](../shared/assets/dex.png) as macOS and the web UI. Confirm privacy and export compliance. Use a new build number for the app version. Set `IOS_STORE_PROFILE` to the absolute profile path, `IOS_DISTRIBUTION_ID` to the signing identity, and `IOS_BUILD_NUMBER` to the next build number.

```sh
mise exec go@1.27.3 -- go tool nsgo build -target ios -channel store \
  -sign "${IOS_DISTRIBUTION_ID:?Set the Apple Distribution identity}" \
  -profile "${IOS_STORE_PROFILE:?Set the App Store profile path}" \
  -build-number "${IOS_BUILD_NUMBER:?Set the next build number}" \
  -output ../.local/apple-v1/build/testflight
```

For upload, provide an authorized App Store Connect team API key through `NSGO_APP_STORE_KEY` (the `.p8` path), `NSGO_APP_STORE_KEY_ID`, and `NSGO_APP_STORE_ISSUER`. Set `IOS_STORE_IPA` to the signed IPA from the `testflight` output. Run the next command only when you intend to upload the build:

```sh
mise exec go@1.27.3 -- go tool nsgo publish ios "${IOS_STORE_IPA:?Set the App Store IPA path}"
```

Wait for processing in App Store Connect. Complete any required compliance or beta review steps there. Then assign the build to the intended TestFlight testers. A successful upload does not release the app on the App Store.
