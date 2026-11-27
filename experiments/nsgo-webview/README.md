# Dex WebView trial

This native shell opens the React UI that Dex already serves. The browser and the shell use the same pages, HTTP API, and event stream. The shell has no chat components. It does not inject app scripts or handle commands from the page. The Go server still runs the Bot. This trial does not change the production client. See the [screenshots and observed differences](screenshots/README.md).

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

## Android and Windows

From `experiments/nsgo-webview`, build the Android arm64 APK and the Windows x64 package:

```sh
mise exec -- go tool nsgo build -target android_arm64,windows_x64 -installers=false
```

For an Android test device with USB debugging, install the APK and use `adb -s <serial> reverse tcp:19681 tcp:19681` to reach the demo on the development machine. After the test, run `adb -s <serial> reverse --remove tcp:19681`.

On Windows, install the WebView2 Runtime. Without it, the shell cannot display Dex. Run a disposable Dex at `127.0.0.1:19681` on that machine, or forward that local port to the demo over an authenticated tunnel. The shell does not include a Dex server. Open the unpacked package with its companion files in place. A build on a Mac does not test a Windows WebView or an Android device.

## Limits

Start the demo before opening the shell. It always loads the fixed loopback address. Stop and reopen the app after a server outage. This is a development tool, not a browser for arbitrary URLs or a production security boundary. NSGo checks navigation hosts; a load on the same host with another port or scheme can start before the shell receives its navigation event. Do not load untrusted pages or use real credentials in this trial.
