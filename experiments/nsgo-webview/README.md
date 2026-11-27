# Dex WebView trial

This native shell opens the React UI that Dex already serves. The browser and the shell use the same pages, HTTP API, and event stream. The shell has no chat components. It does not inject app scripts or handle commands from the page. The Go server still runs the Bot. The first trial targets Apple Silicon Macs and the iOS Simulator. The shared chat composer uses 16 CSS pixels for touch input to prevent focus zoom on iPhone. Mouse and trackpad users keep their existing text size. The page still allows pinch zoom. See the [screenshots and observed differences](screenshots/README.md).

## Try it on a Mac

From the repository root, start the demo:

```sh
mise run poc:nsgo-webview-demo
```

The task builds the React UI and serves it with a local echo model. It binds only to `127.0.0.1:19681`. It uses a temporary home and data directory. It does not read `~/.dex` or require a provider key. Stop the task with Ctrl-C to delete the demo. Do not expose this unauthenticated test service to a network.

Open <http://127.0.0.1:19681/b/default> in a browser. In another terminal, start the shell:

```sh
cd experiments/nsgo-webview
mise exec -- go tool nsgo dev -target native -host 127.0.0.1
```

To use an iOS Simulator, start a simulator and run:

```sh
mise exec -- go tool nsgo dev -target ios_simulator -device '<simulator UDID>' -host 127.0.0.1
```

Send a short message in the shell. Check that its `echo:` reply appears in both clients. Open Board and return to chat. The iOS Simulator can reach the Mac through loopback; a physical iPhone cannot. This trial does not support remote servers or physical iPhones.

### Test iPhone input focus

Use the disposable demo and its default Bot. Install the shell on an iPhone simulator and enable the software keyboard. The [XCTest case](uitests/FocusZoom.swift) sends a message and checks the horizontal bounds of the composer and Send button during input, after keyboard dismissal, and after the echo. No app restart occurs between these steps.

To run the case in Xcode, add it to an iOS UI Testing Bundle. If you use XcodeGen, you can run the same test from `experiments/nsgo-webview/uitests`:

```sh
xcodegen generate
xcodebuild test -project ZoomQA.xcodeproj -scheme FocusZoom \
  -destination 'platform=iOS Simulator,id=<simulator UDID>' -parallel-testing-enabled NO
```

## Optional Android and Windows builds

These targets are outside the first trial. From `experiments/nsgo-webview`, explicitly build the Android arm64 APK and the Windows x64 package:

```sh
mise exec -- go tool nsgo build -target android_arm64,windows_x64 -installers=false
```

For an Android test device with USB debugging, install the APK and use `adb -s <serial> reverse tcp:19681 tcp:19681` to reach the demo on the development machine. After the test, run `adb -s <serial> reverse --remove tcp:19681`.

On Windows, install the WebView2 Runtime. Without it, the shell cannot display Dex. Run a disposable Dex at `127.0.0.1:19681` on that machine, or forward that local port to the demo over an authenticated tunnel. The shell does not include a Dex server. Open the unpacked package with its companion files in place. A build on a Mac does not test a Windows WebView or an Android device.

## Limits

Start the demo before opening the shell. It always loads the fixed loopback address. Stop and reopen the app after a server outage. This is a development tool, not a browser for arbitrary URLs or a production security boundary. NSGo checks navigation hosts; a load on the same host with another port or scheme can start before the shell receives its navigation event. Do not load untrusted pages or use real credentials in this trial.
