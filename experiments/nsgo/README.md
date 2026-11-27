# Dex chat with NSGo

This experiment uses one Go screen for macOS, iOS and the web. It reads the default Bot's Main session from dex over HTTP and SSE. The bot runs on the dex host. The production UI and the root Go module do not use NSGo.

## Try it

Use the [dex development setup](../../docs/dev/development.md). The preview needs `jq`, the installed web dependencies, and Go 1.27.3 or later for this module. Go can select the required toolchain from `go.mod`. The module pins NSGo 0.3.4; no global NSGo install is necessary.

1. From the repository root, run `mise run poc:nsgo-preview`.
2. Open <http://127.0.0.1:19487/> and press **Connect**.
3. Type a message. Return adds a line break. Press **发送** to send. The test provider replies with `echo: <message>`.
4. Press **Reconnect**, or reload the browser and connect again. The latest snapshot should show each message once.
5. Stop the preview with Ctrl+C. It removes the temporary dex home and data directory.

The preview serves the built Web app and proxies `/api` to its own loopback-only dex on port 19486. It does not use a provider key or `~/.dex`. Use the exact browser address above: `localhost` is a different origin from `127.0.0.1`.

## Try another target

Keep the preview running on the Mac. From `experiments/nsgo`, use `go tool nsgo dev` for macOS or `go tool nsgo dev -target ios_simulator` for a local iOS simulator.

`go tool nsgo build` builds the targets in `nsgo.yaml`. A successful build does not verify input, scrolling or network access on the device. A physical iPhone cannot reach a Mac through the iPhone's loopback address. The current iOS preview is simulator-only. Do not expose this unauthenticated preview on a network.

## What to check

- Compose Chinese with a system keyboard. Confirm a candidate with Return. The draft must stay in the composer until you press the send button.
- Open the same chat on two targets. Check that each sees the other's reply. Scroll up during a reply and use **最新消息** to return to the end.
- Put the mobile app in the background, send a message on desktop, return and press **Reconnect**. Check for missing or duplicate messages.
- In a fresh browser profile, measure the transfer and the time until **Connect** works with a slow network. Localhost timing does not predict a mobile cold start.

In this build, the raw Wasm file is 25,287,929 bytes and the Brotli file is 5,200,513 bytes; a curl GET for the Wasm URL used on first load sent `Accept-Encoding: br, gzip` to `127.0.0.1:19487` and received HTTP 200 with no `Content-Encoding` and a 25,287,929-byte body, so the preview did not negotiate compression for that URL.

Keep the isolated preview running. From `experiments/nsgo`, run this command to check chat in a native macOS window:

```sh
mise exec -- go tool nsgo test -target macos -activate -env DEX_NSGO_POC_TEST=1 -run TestUserSendsChinese
```

The PoC displays plain text from the current snapshot and stream. It does not page through older history, render tool calls or Markdown, send attachments, authenticate to a remote host, or deliver push notifications. Reconnection is manual. Test on a physical iPhone before making a migration decision.
