# Wails 3 beta spike（隔离实验）

日期：2026-10-20。版本固定：`github.com/wailsapp/wails/v3 v3.0.0-beta.33`，CLI `wails3 v3.0.0-beta.33`。本目录不引用 Dex 的 Go 包，也没有修改 `internal/app`、`internal/server`、`web/src`、根 `go.mod` 或生产任务。

## 可重复命令与结果

前置环境：macOS arm64，`go version go1.27.2 darwin/arm64`，Node `v26.11.1`，npm `11.20.0`。缓存目录使用临时路径，避免污染用户缓存：

```sh
cd spikes/wails3
export PATH="$HOME/.local/share/mise/installs/go/1.27.2/bin:/tmp/wails3-bin:$PATH"
export GOPATH=/tmp/wails3-gopath GOMODCACHE=/tmp/wails3-gomodcache GOCACHE=/tmp/wails3-gocache GOTOOLCHAIN=local
GOBIN=/tmp/wails3-bin go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.33
wails3 version
npm_config_cache=/tmp/wails3-npm-cache npm install --prefix frontend
wails3 generate bindings -clean=true -ts
npm run build --prefix frontend
go test ./...
wails3 build
```

结果：CLI 安装成功；绑定生成成功，发现 1 个 service、1 个 method；React/Vite 构建成功；`go test ./...` 成功（无测试）；`wails3 build` 成功并产出 `bin/dex-wails3-spike`。链接阶段出现 macOS deployment target 警告，但没有失败。

开发模式命令：

```sh
wails3 dev -config ./build/config.yml -port 9245
```

本机实际结果：Go 编译完成后，beta.33 的文件监听器在当前受限环境返回 `FSEventStreamStart: false`，进程退出。这是可复现的环境/文件监听阻塞证据；没有把它伪报成 dev 成功。可在普通本机终端重跑该命令验证 WebView 启动。

最初直接运行 `wails3 build` 会报 `No Taskfile found`。补上最小 `Taskfile.yml` 后构建成功，说明 beta CLI 的 build 入口依赖 Taskfile，而不是只调用裸 `go build`。

## 实验覆盖

- **React/Vite**：`frontend/` 是独立 React 19 + Vite 项目，构建产物通过 `embed.FS` 送给 Wails asset handler。
- **自动绑定**：`wails3 generate bindings -clean=true -ts` 生成 `frontend/bindings/dex-wails3-spike/transcriptservice.ts`。`Ping()` 由 Go service 自动暴露给 TS。
- **应用、窗口、服务生命周期**：`application.New` 注册 service；`ServiceStartup` 和 `ServiceShutdown` 写日志；`Window.NewWithOptions` 创建窗口；`app.Run` 进入生命周期。
- **transcript streaming**：Go 注册 `app.HandleStream("transcript", ...)`，每 250 ms 发送一个 JSON chunk。前端使用 `JSONStream("transcript")`，窗口重载或关闭时连接上下文取消。这个模型是命名、双向、无监听 socket 的 byte stream，适合把 Dex 的增量 transcript 投影成有序帧，但需要自己定义重连、snapshot、游标、背压、丢帧恢复和权限边界。

## 现有 HTTP/SSE 与 Wails IPC 的并列方案

| 方案                    | 复用与改动                                                                                                                                | transcript                                                                                                        | 客户端兼容性                                                              | 主要风险                                                                                         |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| 桌面壳复用现有 HTTP/SSE | Wails 只负责窗口和静态资源；Dex HTTP API、OpenAPI 生成客户端、SSE、channel clients 原样保留。桌面端用 loopback URL 或现有 server 地址。   | 直接复用当前 SSE snapshot/reconnect 语义。                                                                        | 浏览器、远程浏览器、channel clients、桌面端共用一个契约。                 | loopback 端口管理、token/CORS/跨源策略、服务启动顺序和端口冲突仍需处理；桌面壳没有减少后端边界。 |
| 迁移到 Wails 3 IPC      | Go service 绑定替代部分 HTTP endpoint；React 改用生成 bindings，transcript 改用 Events 或 Streams。OpenAPI 仍需为浏览器和外部客户端保留。 | Streams 适合有序双向 chunk；Events 适合广播通知。必须补齐 SSE 现有的 snapshot、重连、慢客户端关闭和历史游标协议。 | 浏览器和 channel clients 不能直接使用 Wails IPC，需维护 HTTP/SSE 适配层。 | beta API/生成代码变动；双协议语义漂移；IPC 不是远程 API；重连和持久化语义需要 Dex 自己实现。     |

**结论**：首版桌面版采用“Wails 3 壳 + 现有 HTTP/SSE”更稳。它保留一个已有的 OpenAPI/transcript 契约，并让浏览器、远程客户端和 channel clients 继续工作。只有在确认端口策略、认证和桌面独有高频双向流确实成为瓶颈后，才应把局部操作迁移到 bindings/Streams；不建议首版整体替换 SSE。

## no-open-port / WebView / 平台说明

Wails 3 的 Streams 文档把 stream 定义为无监听 socket 的命名双向 byte channel；这解决的是 WebView 与 Go 的进程内传输，不会让浏览器、远程客户端或 channel clients 获得 Dex API。Wails 3 同时提供 bundled asset handler，因此可把 React dist 嵌入应用。

官方 README 当前把 v3 标为 **Beta**，安装入口为 `go install github.com/wailsapp/wails/v3/cmd/wails3@latest`。beta.33 的源码 module 要求 Go `>=1.26.0`，本机 Go 1.27.2 可编译。桌面 WebView 使用平台原生渲染引擎；发布仍需分别处理 macOS app bundle、签名/notarization，Windows 资源/签名，Linux 打包与 WebKitGTK 依赖。官方仓库包含 Windows/Linux/macOS 构建任务以及 iOS/Android 实验性代码和 WKWebView/Android WebView 路径，但本 spike 没有验证这些平台，也没有验证签名、notarization、商店发布或交叉编译。

参考：

- [Wails v3 README（Beta、安装入口）](https://github.com/wailsapp/wails/tree/v3)
- [Wails v3 文档](https://v3.wails.io/)
- [Your first app](https://v3.wails.io/getting-started/your-first-app/)
- [Streams](https://v3.wails.io/guides/streams/)
- [Migrating WebSockets to Streams](https://v3.wails.io/guides/streams-from-websockets/)
- [Releases（beta.33）](https://github.com/wailsapp/wails/releases/tag/v3.0.0-beta.33)

## beta 风险清单

1. **版本漂移**：`latest` 会继续变化；必须固定 beta tag，并把 CLI、Go module、`@wailsio/runtime` 一起锁定。
2. **CLI 约束**：`wails3 build` 依赖 Taskfile；`dev` 依赖文件监听器和前端 dev server。错误信息和配置字段仍在 beta 中变化。
3. **生成代码/API 不稳定**：bindings 的包路径和方法 ID 是生成结果。不要把生成目录当作稳定手写 API；每次升级都要重新生成并编译。
4. **流语义缺口**：Streams 提供连接生命周期和帧传输，不提供 Dex 现有 SSE 的 durable snapshot、cursor、replay、权限和业务 receipt 语义。
5. **原生构建边界**：本机 build 成功，但出现 deployment target 警告；未验证 CI、签名、notarization、WebView 版本矩阵、Windows/Linux/iOS/Android。
6. **开发环境阻塞**：当前受限环境的 FSEventStream 启动失败，导致 `wails3 dev` 未完成运行验证。

## 未验证项

Windows、Linux、iOS、Android；macOS 签名/notarization；安装包、DMG、MSI、DEB/RPM；真实 WebView 兼容性；Dex 真正 agent transcript 压力、断线重连和背压；浏览器/远程客户端通过 Wails IPC 访问；生产发布流水线。
