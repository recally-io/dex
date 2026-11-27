# WebView trial captures

The screens show the same disposable Dex and the same Bot Main route, `/b/default`. No screen contains a real provider response.

- [Browser, desktop](browser-desktop.png) and [browser, phone width](browser-phone.png): Chrome at 1280 × 800 and 390 × 844 CSS pixels. The desktop browser sent the first message. Both browsers displayed its reply.
- [iOS Simulator, initial view](ios.png): the app shows the same first message and reply. This is a native app capture, not a browser at phone width.
- [iOS Simulator, after sending](ios-after-send.png): the second user message and its echo came from a send in the app. The viewport is still enlarged after entry. Compare the initial view to see the change in scale and visible controls.

- [iOS Simulator, after reopening](ios-reopened.png): both messages and replies are visible again at the original scale.
- [macOS, WebKit content](macos-webkit-content.png): a separate QA copy captured only its own 1280 × 800 pt `WKWebView` with `takeSnapshot`. The image shows the desktop layout and both replies, but not the user bubbles. The cause is unknown. The unmodified app ran on the Mac, but the system did not allow window capture or input automation. This is not a screenshot of the distributed app window.

These captures show appearance and selected interactions. They do not certify pixel equality, a physical iPhone, or the Android and Windows clients.
