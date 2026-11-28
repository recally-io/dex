package io.recally.dex;

import android.content.Context;
import android.net.Uri;
import android.util.Base64;
import android.view.ViewGroup;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import java.io.ByteArrayInputStream;
import java.util.HashMap;
import java.util.Iterator;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

/** NSGo sets origin once per mounted workspace. Remote pages have no Java bridge. */
public final class WorkspaceView extends FrameLayout {
    public interface Listener {
        void settings();
        void failure();
        void blocked();
        void external(String url);
        void localMessage(String json);
        void localStatic(String json);
    }

    private Uri origin;
    private boolean local;
    private Listener events;
    private volatile WebView page;
    private final AtomicLong requestID = new AtomicLong();
    private final Map<String, CompletableFuture<JSONObject>> requests = new ConcurrentHashMap<>();

    public WorkspaceView(Context context) { super(context); }
    public void setOnWorkspaceListener(Listener listener) { events = listener; }

    public void setOrigin(String address) {
        if (origin != null) return;
        local = "local".equals(address);
        origin = Uri.parse(local ? "https://local.dex.internal/" : address);
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
        if (local) page.addJavascriptInterface(new LocalInterface(page), "dexLocal");
        addView(page, new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        page.loadUrl(origin.toString());
    }

    @Override protected void onDetachedFromWindow() {
        if (page != null) {
            WebView old = page;
            page = null;
            if (local) {
                old.removeJavascriptInterface("dexLocal");
                for (CompletableFuture<JSONObject> request : requests.values()) request.cancel(false);
                requests.clear();
            }
            old.stopLoading();
            old.setWebViewClient(new WebViewClient());
            removeView(old);
            old.destroy();
        }
        super.onDetachedFromWindow();
    }

    public void eval(String js) {
        if (local && page != null) page.evaluateJavascript(js, null);
    }

    public void staticResult(String json) {
        if (!local) return;
        try {
            JSONObject result = new JSONObject(json);
            CompletableFuture<JSONObject> request = requests.remove(result.getString("id"));
            if (request != null) request.complete(result.getJSONObject("response"));
        } catch (JSONException ignored) {
            // The request deadline releases the interception thread on a bad response.
        }
    }

    public final class LocalInterface {
        private final WebView owner;
        LocalInterface(WebView owner) { this.owner = owner; }

        @JavascriptInterface public void postMessage(String json) {
            post(() -> {
                if (page == owner && events != null) events.localMessage(json);
            });
        }
    }

    private WebResourceResponse localResource(WebView owner, WebResourceRequest req) {
        String id = Long.toString(requestID.incrementAndGet());
        CompletableFuture<JSONObject> reply = new CompletableFuture<>();
        requests.put(id, reply);
        try {
            JSONObject headers = new JSONObject();
            for (Map.Entry<String, String> entry : req.getRequestHeaders().entrySet()) {
                headers.put(entry.getKey(), new JSONArray().put(entry.getValue()));
            }
            String message = new JSONObject().put("op", "static").put("id", id)
                .put("url", req.getUrl().toString()).put("method", req.getMethod())
                .put("headers", headers).toString();
            if (!post(() -> {
                if (page == owner && events != null) events.localStatic(message);
                else reply.cancel(false);
            })) return unavailable();
            JSONObject response = reply.get(20, TimeUnit.SECONDS);
            int status = response.getInt("status");
            // WebResourceResponse rejects redirects. Local resources do not redirect.
            if (status < 200 || (status >= 300 && status < 400)) return unavailable();
            Map<String, String> fields = new HashMap<>();
            JSONObject responseHeaders = response.optJSONObject("headers");
            String contentType = "application/octet-stream";
            if (responseHeaders != null) {
                for (Iterator<String> keys = responseHeaders.keys(); keys.hasNext();) {
                    String key = keys.next();
                    JSONArray values = responseHeaders.getJSONArray(key);
                    StringBuilder joined = new StringBuilder();
                    for (int i = 0; i < values.length(); i++) {
                        if (i != 0) joined.append(", ");
                        joined.append(values.getString(i));
                    }
                    fields.put(key, joined.toString());
                    if ("Content-Type".equalsIgnoreCase(key)) contentType = joined.toString();
                }
            }
            String[] type = contentType.split(";", 2);
            String encoding = type.length == 2 && type[1].trim().equalsIgnoreCase("charset=utf-8") ? "UTF-8" : null;
            byte[] body = Base64.decode(response.optString("body"), Base64.DEFAULT);
            return new WebResourceResponse(type[0].trim(), encoding, status, "Local response", fields, new ByteArrayInputStream(body));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return unavailable();
        } catch (ExecutionException | TimeoutException | JSONException | IllegalStateException | IllegalArgumentException e) {
            return unavailable();
        } finally {
            requests.remove(id);
        }
    }

    private static WebResourceResponse unavailable() {
        return new WebResourceResponse("text/plain", "UTF-8", 503, "Workspace unavailable", null, new ByteArrayInputStream(new byte[0]));
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
            if (inside(url)) return local && !req.isForMainFrame();
            if (view != page || !req.isForMainFrame()) return true;
            boolean gesture = req.hasGesture() && !req.isRedirect() && "GET".equals(req.getMethod());
            if (gesture && "dex-workspace://settings".equals(url.toString())) events.settings();
            else if (gesture && "https".equals(url.getScheme()) && url.getHost() != null &&
                url.getUserInfo() == null && (local || (url.getQuery() == null && url.getFragment() == null))) events.external(url.toString());
            else events.blocked();
            return true;
        }

        @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest req) {
            if (inside(req.getUrl())) {
                if (local) return localResource(view, req);
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
