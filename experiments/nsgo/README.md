# Dex Workspace with NSGo

This experiment is a Dex client for macOS, the iOS Simulator, and the web. It uses one Go UI and the existing [Dex HTTP API](../../api/openapi.yaml). Dex still runs the bots and stores their data. The [React web UI](../../web/AGENTS.md) remains the production client.

The NSGo workspace follows the React UI layout: a Bot sidebar on wide screens, a navigation drawer on narrow screens, and a main area for chat, the Board, and settings. Open the two clients side by side to compare them. Use the [Dex UI design contract](../../docs/dev/ui-design.md) to choose the reference screen and review differences. Full layout and feature parity is not an acceptance result.

## Try the workspace

Use the [Dex development setup](../../docs/dev/development.md). The preview needs `jq`, the installed web dependencies, and Go 1.27.3. The module pins NSGo 0.3.4. You do not need a global NSGo install.

From the repository root, run:

```sh
mise run poc:nsgo-preview
```

- Open the **NSGo client** at <http://127.0.0.1:19487/>.
- Open the **original React UI** at <http://127.0.0.1:19486/>. Both clients use the same temporary Dex, Bot, and Board. Changes in one client are visible in the other.
- Send a message in a Main chat. The test model replies with `echo: <message>`. Use the seeded Task to compare how each client opens a linked conversation or the assigned Bot's Main chat.

Use `127.0.0.1` in both URLs. The preview does not use a real provider key or `~/.dex`. Stop it with Ctrl+C to remove the temporary Dex home and data. Do not expose this unauthenticated preview on a network.

With the trial preview running, start a native client from `experiments/nsgo`:

```sh
mise exec -- go tool nsgo dev
mise exec -- go tool nsgo dev -target ios_simulator
```

The first command opens the macOS client. The second opens the iOS Simulator client. A physical iPhone cannot reach the Mac through the iPhone's loopback address.

## What the client implements

- **Workspace and chat.** Switch Bots, create a Bot with the default settings, and search Bot names and session titles. Open Main and other sessions. Send text or files, read live replies and basic tool or error entries, and reconnect. During a run, steer the current turn, queue a message, or stop the run. Use `/clear` (also `/new`) or `/compact` in a writable session. Open Activity from Main or settings from a separate session. Create, archive, and restore eligible sessions.
- **Board and automation.** Create a Task, read its details, change its status, add a note, or send a message to the assigned Bot. Open a linked conversation or the assigned Bot's Main chat. Create a paused interval automation, enable or pause it, run it now, and open a session from its run history.
- **Settings and files.** Edit a Bot's name, primary model, thinking level, fallback model, and instructions. Restore an archived Bot. View shared settings and integrations. View Bot Skills, MCP servers, and channel status. Reload Bot tools, disconnect a channel, and manage Bot secrets. List artifacts and preview supported UTF-8 files up to 64 KiB as plain text.

## Limits

The client does not load older transcript pages or render Markdown. Use the file picker for attachments. Clipboard images, file drops, and image thumbnails do not have full React parity. On the iPhone software keyboard, Return inserts a new line. Use the Send button to submit. It does not replace the React UI for provider setup, plugin controls, OAuth, channel setup, or other admin work. It does not send push notifications. The trial does not validate remote token login, real provider or channel access, or operation on a physical iPhone.

## Run a native acceptance check

Use a separate temporary Dex for tests that write data. From the repository root, start the dedicated preview in a separate terminal:

```sh
NSGO_PREVIEW_PORT=19489 mise run poc:nsgo-preview
```

This starts the NSGo gateway on <http://127.0.0.1:19489/> and its separate Dex on <http://127.0.0.1:19488/>. Native tests use the gateway on `19489`, not the trial on `19487`. From `experiments/nsgo`, select a test:

```sh
mise exec -- go tool nsgo test -target macos -activate \
  -env DEX_NSGO_POC_TEST=1 -env DEX_NSGO_TEST_URL=http://127.0.0.1:19489 \
  -run TestUserSendsChinese

mise exec -- go tool nsgo test -target macos -activate \
  -env DEX_NSGO_POC_TEST=1 -env DEX_NSGO_TEST_URL=http://127.0.0.1:19489 \
  -env DEX_NSGO_BOTS_TEST=1 -run TestUserCreatesBotAndRenamesItWithoutLosingNewInstructions
```

From the repository root, compare the React and NSGo sidebars against the same Bot list. The check uses Chrome at 1280 × 800 and 390 × 820. It checks the order of the Bot rows, the fixed footer, and the drawer. It saves four screenshots locally. Do not publish screenshots that contain private Bot names.

```sh
node experiments/nsgo/sidebar-check.cjs 19489 .local/nsgo-poc/screens
```

For a shell or chat style review, use a fresh disposable preview. In the default Bot, send `The release notes are ready for review.` as the only message. Wait for the echo, leave the draft empty, and run the comparison with that exact reply. This also measures the short conversation in both appearances. The sidebar-only command does not check the chat style.

```sh
node experiments/nsgo/sidebar-check.cjs 19489 .local/nsgo-poc/screens 'echo: The release notes are ready for review.'
```

For pixel comparison, use the same disposable preview and the same single reply. Add any demo Bots **before** the capture so the roster stays fixed. With the preview on `19489`, run:

```sh
node experiments/nsgo/pixel-check.cjs 19489 .local/nsgo-poc/pixel
```

The check uses a fresh browser context for each client, width, navigation state, and theme. It saves the React, NSGo, and difference images with regional metrics in a new run directory. Exit status `1` means the capture failed or a measured region missed its limit. Read `metrics-report.txt` for the reason. The full-screen difference count is diagnostic only: an empty area cannot offset a failed control. The check does not send the draft or change the reference UI. Its phone viewport measures the Web renderer only.

To check the composer in Chrome, run `DEX_NSGO_POC_TEST=1 node experiments/nsgo/composer-check.cjs 19489` against the disposable preview above. The check adds a Bot and sends messages. It does not use a connected service or a real provider.

The macOS and iOS Simulator checks covered navigation from Main to the Board and back, a chat reply after reconnect, and session archive and restore. A separate macOS check covered Bot creation, rename, and retention of a concurrent instruction change. These checks do not verify the full client or a physical iPhone.

An earlier chat-only Web build sent 25,287,929 bytes for its Wasm file. The Brotli file was 5,200,513 bytes. Curl sent `Accept-Encoding: br, gzip` to the Wasm URL on port `19487`; the HTTP 200 response had no `Content-Encoding` and sent the full 25,287,929-byte body. Measure the current workspace build again with an empty browser cache and a slow connection. The earlier result is not a current size or cold-start measurement.
