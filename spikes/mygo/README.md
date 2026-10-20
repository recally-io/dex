# Dex MyGo v0.5.5 spike

这是隔离实验。实验只放在 `spikes/mygo/`，不接入 Dex 入口，也不修改 `internal/app`、`internal/server`、`web/src`、根 `go.mod` 或生产构建任务。

## 可重复命令

实验固定 `github.com/egoist/mygo v0.5.5`。当前仓库使用 mise 提供 Go 1.27.2。下面的临时缓存目录避免受限环境写入用户缓存目录。

```sh
cd spikes/mygo
mise exec -- env GOSUMDB=off GOMODCACHE=/tmp/dex-gomod GOCACHE=/tmp/dex-gocache GOPATH=/tmp/dex-gopath GOFLAGS=-mod=mod \
  go test ./...
mise exec -- env GOSUMDB=off GOMODCACHE=/tmp/dex-gomod GOCACHE=/tmp/dex-gocache GOPATH=/tmp/dex-gopath GOFLAGS=-mod=mod \
  go run github.com/egoist/mygo/cmd/mygo@v0.5.5 generate
TMPDIR=/tmp BUN_INSTALL_CACHE_DIR=/tmp/bun-cache mise exec -- bun install
TMPDIR=/tmp BUN_INSTALL_CACHE_DIR=/tmp/bun-cache mise exec -- bun run build:web
mise exec -- env GOSUMDB=off GOMODCACHE=/tmp/dex-gomod GOCACHE=/tmp/dex-gocache GOPATH=/tmp/dex-gopath GOFLAGS=-mod=mod \
  go run github.com/egoist/mygo/cmd/mygo@v0.5.5 build -skip-dmg -skip-notarize
```

本次结果：

- `go test ./...`：通过。Go 版本为 `go1.27.2 darwin/arm64`。
- `go vet ./...` 与 `go test -race ./...`：通过。当前 spike 没有测试文件，因此只验证编译和 race 运行路径。
- `mygo generate`：通过，生成 `src/mygo.ts`，包含 `Agent.ping`、`Agent.stream`、`Progress` 和 `events.agentProgress`。
- Vite 生产构建：通过，生成 `dist/index.html` 和 JS asset。
- macOS：通过，生成签名的 `build/darwin-arm64/Dex MyGo Spike.app`，约 8.9 MB。
- Linux：通过交叉编译和打包，生成 amd64 可执行文件、`.deb`、tarball 和 `install.sh`。
- Windows：通过交叉编译生成 `Dex MyGo Spike.exe`。Windows 安装器未生成，因为当前机器没有 `makensis`。
- `mygo doctor`：确认 Go 1.27.2、Bun 1.4.3、macOS 27.0.1/WKWebView；报告 `makensis`、`osslsigncode`、Developer ID 均未配置。

`bun run dev` 会启动真实 MyGo 窗口和 Vite HMR。此次没有自动化点击窗口，因为当前检查环境不提供稳定的 GUI 断言；已用生产构建验证嵌入 dist，已用 Go 编译验证窗口和绑定 API。

## 验证内容

`main.go` 用 `//go:embed all:dist` 调用 `mygo.SetFrontend`，因此构建后的 `.app` 不依赖 Node 或 Bun。`Agent.Ping` 验证 typed Go binding。`Agent.Stream` 验证 `*mygo.Channel[Progress]` 的逐项流式结果。`ProgressEvent` 验证向所有页面广播 typed event。页面通过生成的 `src/mygo.ts` 调用这些 API。

MyGo 的 channel 适合一次调用的一条流：页面关闭、导航或主动 `close()` 会取消 Go context，`Send` 返回 `mygo.ErrChannelClosed`。event 适合窗口生命周期内的广播。Dex 的 SSE 是长连接、快照加增量事件、15 秒心跳和自动重连。推荐保留 `internal/live.Hub` 作为唯一 transcript 投影，在桌面适配层把 SSE 的 `snapshot/item/append/run` 解码成页面状态；不要把每个 SSE 连接改成一个长生命周期 MyGo channel。发送用户输入仍调用现有 OpenAPI，桌面只替换浏览器传输和窗口容器。

### 接入建议与迁移成本

推荐分三层迁移：

1. **低成本**：把现有 `web/dist` 作为 MyGo `frontendDist`，复用 React/Vite 页面和 OpenAPI client。新增一个 desktop main，只负责 `SetFrontend`、窗口和生命周期。
2. **中成本**：把 SSE URL 改成桌面页面的 `EventSource` 仍访问 loopback HTTP。这样 Dex 的鉴权、重连、快照语义和测试基本不变；MyGo typed event 只用于窗口级状态、通知或轻量 agent progress。
3. **高成本且不推荐作为首步**：把 OpenAPI 请求逐个改成 MyGo typed binding。这样会重复生成一套 RPC 边界，增加错误、权限、取消、版本兼容和测试面，也会让 Web 与桌面出现两份协议。

预计第一层只需新增桌面入口和打包配置，第二层需要桌面启动时管理 loopback 服务和退出清理，第三层会重写整个 API 客户端。Dex 已有 `internal/live.Hub` 的 `engine.Event` 映射，迁移重点是复用它，而不是重建 transcript store。

## 平台与生命周期边界

官方文档说明：macOS 使用系统 WKWebView，要求 macOS 12+；Linux web window 要求 GTK 3 和 WebKitGTK 4.1/4.0；Windows 10/11 web window 要求 WebView2 Runtime。native UI 窗口不需要 Linux WebKitGTK 或 Windows WebView2。MyGo 可从任意平台无 cgo 交叉编译，但 macOS 签名/DMG、Windows 安装器和签名依赖各平台工具。

官方分发路径是 macOS `.app`/DMG、Windows `.exe`/NSIS installer、Linux archive/`install.sh`/Debian `.deb`。签名字段在 `mygo.json` 的 `macos`、`windows` 中配置。自动更新使用签名 manifest、Ed25519 公钥和 GitHub 或 HTTPS/S3 feed；私钥必须留在 CI/密码管理器。此次没有生成更新包，也没有签名证书和 notarization profile，因此没有验证发布链路。

生命周期 API 足够覆盖 Dex：`OnWindowAllClosed`、`OnActivate`、`OnBeforeQuit`/`OnWillQuit`、`App.Relaunch`、`RequestSingleInstanceLock`/`OnSecondInstance`、tray、notification 和 updater 均存在。spike 编译验证了单实例、关闭后保持进程、激活时重开窗口的最小路径；没有在真实桌面上验证 Dock/托盘点击、通知权限、崩溃重启、系统休眠和自动更新失败恢复。

适合：复用 React/Vite transcript、桌面窗口/菜单/通知、loopback API、signed update 和一个 Go 进程内的 typed 辅助调用。

不适合：把 MyGo 当作 WebView2/Chromium 的统一运行时、绕开现有 HTTP/OpenAPI/SSE 合同、在没有 Linux WebKitGTK 或 Windows WebView2 的机器上强制使用 web window、把高频 transcript 全量复制为广播 event、或在未建立签名和更新 feed 前承诺跨平台自动更新。

官方资料：

- [MyGo v0.5.5 README](https://github.com/egoist/mygo/tree/v0.5.5)
- [Frontend and embedded dist](https://github.com/egoist/mygo/blob/v0.5.5/docs/frontend.md)
- [Typed bindings, channels and events](https://github.com/egoist/mygo/blob/v0.5.5/docs/bindings.md)
- [App lifecycle and single instance](https://github.com/egoist/mygo/blob/v0.5.5/docs/app.md)
- [Distribution and platform requirements](https://github.com/egoist/mygo/blob/v0.5.5/docs/distribution.md)
- [Signed auto-updates](https://github.com/egoist/mygo/blob/v0.5.5/docs/updates.md)
