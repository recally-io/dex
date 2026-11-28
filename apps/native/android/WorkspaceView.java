package io.recally.dex;

import android.content.Context;
import android.net.Uri;
import android.view.ViewGroup;
import android.webkit.CookieManager;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import java.io.ByteArrayInputStream;
import java.util.concurrent.atomic.AtomicBoolean;

/** NSGo sets origin once per mounted workspace. Remote pages have no Java bridge. */
public final class WorkspaceView extends FrameLayout {
    public interface Listener {
        void settings();
        void failure();
        void blocked();
        void external(String url);
    }

    private Uri origin;
    private Listener events;
    private WebView page;

    public WorkspaceView(Context context) { super(context); }
    public void setOnWorkspaceListener(Listener listener) { events = listener; }

    public void setOrigin(String address) {
        if (origin != null) return;
        origin = Uri.parse(address);
        if (isAttachedToWindow()) mount();
    }

    @Override protected void onAttachedToWindow() {
        super.onAttachedToWindow();
        if (origin != null) mount();
    }

    private void mount() {
        if (page != null) return;
        page = new WebView(getContext());
        WebSettings prefs = page.getSettings();
        prefs.setJavaScriptEnabled(true);
        prefs.setDomStorageEnabled(true);
        prefs.setAllowFileAccess(false);
        prefs.setAllowContentAccess(false);
        prefs.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        prefs.setCacheMode(WebSettings.LOAD_NO_CACHE);
        prefs.setSupportMultipleWindows(true); // No WebChromeClient can create a popup.
        prefs.setUserAgentString(prefs.getUserAgentString() + " DexWorkspace/1");
        CookieManager.getInstance().setAcceptThirdPartyCookies(page, false);
        page.setWebViewClient(new Client());
        addView(page, new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        page.loadUrl(origin.toString());
    }

    @Override protected void onDetachedFromWindow() {
        if (page != null) {
            WebView old = page;
            page = null;
            old.stopLoading();
            old.setWebViewClient(new WebViewClient());
            removeView(old);
            old.destroy();
        }
        super.onDetachedFromWindow();
    }

    private boolean inside(Uri url) {
        return "https".equalsIgnoreCase(url.getScheme()) && url.getUserInfo() == null &&
            origin.getHost().equalsIgnoreCase(url.getHost()) &&
            port(url) == port(origin);
    }

    private static int port(Uri url) { return url.getPort() == -1 ? 443 : url.getPort(); }

    private final class Client extends WebViewClient {
        private final AtomicBoolean flushAfterLogin = new AtomicBoolean();

        @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest req) {
            Uri url = req.getUrl();
            if (inside(url)) return false;
            if (view != page || !req.isForMainFrame()) return true;
            boolean gesture = req.hasGesture() && !req.isRedirect() && "GET".equals(req.getMethod());
            if (gesture && "dex-workspace://settings".equals(url.toString())) events.settings();
            else if (gesture && "https".equals(url.getScheme()) && url.getHost() != null &&
                url.getUserInfo() == null && url.getQuery() == null && url.getFragment() == null) events.external(url.toString());
            else events.blocked();
            return true;
        }

        @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest req) {
            if (inside(req.getUrl())) {
                String path = req.getUrl().getPath();
                if ("POST".equals(req.getMethod()) && "/api/session".equals(path)) {
                    flushAfterLogin.set(true);
                } else if (path != null && path.startsWith("/api/") && flushAfterLogin.get()) {
                    // Chromium can defer disk writes past an abrupt app exit. The first
                    // API request with the login cookie means Chromium accepted it.
                    CookieManager store = CookieManager.getInstance();
                    if (store.getCookie(req.getUrl().toString()) != null && flushAfterLogin.compareAndSet(true, false)) {
                        store.flush();
                    }
                }
                return null;
            }
            return new WebResourceResponse("text/plain", "UTF-8", 403, "Blocked", null, new ByteArrayInputStream(new byte[0]));
        }

        @Override public void onReceivedError(WebView view, WebResourceRequest req, WebResourceError err) {
            if (view == page && req.isForMainFrame()) events.failure();
        }

        @Override public void onReceivedHttpError(WebView view, WebResourceRequest req, WebResourceResponse response) {
            if (view == page && req.isForMainFrame() && response.getStatusCode() >= 400) events.failure();
        }

        @Override public boolean onRenderProcessGone(WebView view, RenderProcessGoneDetail detail) {
            if (view == page) events.failure();
            return true;
        }
    }
}
