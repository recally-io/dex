# WebView trial captures

The screens show the same disposable Dex and the same Bot Main route, `/b/default`. No screen contains a real provider response.

- [Browser, desktop](browser-desktop.png) and [browser, phone width](browser-phone.png): Chrome at 1280 × 800 and 390 × 844 CSS pixels. The desktop browser sent the first message. Both browsers displayed its reply.
- [iOS Simulator, initial view](ios.png): the app shows the same first message and reply. This is a native app capture, not a browser at phone width.
- [iOS Simulator, before the fix](ios-before-fix.png): focus enlarged the 352 pt composer to 403 pt on a 402 pt wide iPhone. The right edge remained clipped after the reply.
- [iOS Simulator, keyboard open](ios-keyboard.png) and [after sending](ios-after-send.png): with the fix, the composer stays 352 pt wide and the Send button stays 28 pt wide. Dismissing the keyboard restores the header. The reply appears in the same view without a restart.
- [Earlier trial, after reopening](ios-reopened.png): the old app needed a restart to show the whole chat again in this run.
- [macOS, WebKit content](macos-webkit-content.png): a separate QA copy captured only its own 1280 × 800 pt `WKWebView` with `takeSnapshot`. The image shows the desktop layout and both replies, but not the user bubbles. The cause is unknown. The unmodified app ran on the Mac, but the system did not allow window capture or input automation. This is not a screenshot of the distributed app window.

These captures show appearance and selected interactions. They do not certify pixel equality, a physical iPhone, or the Android and Windows clients.
