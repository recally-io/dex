(() => {
  const apple = window.webkit?.messageHandlers?.dexLocal;
  const android = window.dexLocal;
  if (!apple && !android) return;

  const send = (message) => {
    const json = JSON.stringify(message);
    if (apple) apple.postMessage(json);
    else android.postMessage(json);
  };
  const prefix = Array.from(crypto.getRandomValues(new Uint32Array(4))).join("-");
  let sequence = 0;
  const nextID = () => `${prefix}:${++sequence}`;
  const pending = new Map();
  const streams = new Map();
  const originalFetch = window.fetch.bind(window);
  const OriginalEventSource = window.EventSource;
  const apiURL = (input) => {
    const url = new URL(input, location.href);
    if (url.protocol !== location.protocol || url.host !== location.host ||
        url.username || url.password || !url.pathname.startsWith("/api/")) return null;
    return url;
  };
  const decode = (text) => Uint8Array.from(atob(text ?? ""), (c) => c.charCodeAt(0));
  const encode = (bytes) => {
    const parts = [];
    for (let i = 0; i < bytes.length; i += 16384) {
      parts.push(String.fromCharCode(...bytes.subarray(i, i + 16384)));
    }
    return btoa(parts.join(""));
  };

  window.fetch = async (input, init) => {
    const url = apiURL(input instanceof Request ? input.url : input);
    if (!url) return originalFetch(input, init);
    // Request serializes all browser body types and sets the multipart boundary.
    const req = new Request(input, init);
    const body = req.body ? encode(new Uint8Array(await req.arrayBuffer())) : "";
    if (req.signal.aborted) throw req.signal.reason;
    const id = nextID();
    return new Promise((resolve, reject) => {
      const abort = () => {
        pending.delete(id);
        reject(req.signal.reason);
      };
      req.signal.addEventListener("abort", abort, { once: true });
      pending.set(id, (result) => {
        req.signal.removeEventListener("abort", abort);
        try {
          if (result.error) throw new TypeError(result.error);
          const headers = new Headers();
          for (const [name, values] of Object.entries(result.headers ?? {})) {
            for (const value of values) headers.append(name, value);
          }
          const noBody = req.method === "HEAD" || [204, 205, 304].includes(result.status);
          const response = new Response(noBody ? null : decode(result.body), { status: result.status, headers });
          Object.defineProperty(response, "url", { value: req.url });
          resolve(response);
        } catch (error) {
          reject(error);
        }
      });
      try {
        send({ op: "fetch", id, url: url.pathname + url.search, method: req.method,
          headers: Object.fromEntries(Array.from(req.headers, ([key, value]) => [key, [value]])), body });
      } catch (error) {
        pending.delete(id);
        req.signal.removeEventListener("abort", abort);
        reject(error);
      }
    });
  };

  class LocalEventSource extends EventTarget {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSED = 2;
    CONNECTING = 0;
    OPEN = 1;
    CLOSED = 2;
    readyState = 0;
    onopen = null;
    onmessage = null;
    onerror = null;
    #id;
    #cursor = "";
    #retry = 1000;
    #timer;
    #text;
    #type;
    #data;
    #eventID;
    #skipLF;
    #decoder;

    constructor(input, options) {
      super();
      const url = apiURL(input);
      if (!url) return new OriginalEventSource(input, options);
      this.url = url.href;
      this.withCredentials = !!options?.withCredentials;
      this.#start();
    }

    #start() {
      this.#text = "";
      this.#type = "";
      this.#data = [];
      this.#eventID = this.#cursor;
      this.#skipLF = false;
      this.#decoder = new TextDecoder();
      this.#id = nextID();
      streams.set(this.#id, this);
      const url = new URL(this.url);
      send({ op: "sseOpen", id: this.#id, url: url.pathname + url.search, lastEventId: this.#cursor });
    }

    #dispatch(event) {
      this.dispatchEvent(event);
      if (["open", "message", "error"].includes(event.type)) this[`on${event.type}`]?.(event);
    }

    chunk(bytes) {
      if (this.readyState === this.CONNECTING) {
        this.readyState = this.OPEN;
        this.#dispatch(new Event("open"));
      }
      let text = this.#decoder.decode(decode(bytes), { stream: true });
      if (!text) return;
      if (this.#skipLF && text.startsWith("\n")) text = text.slice(1);
      this.#skipLF = text.endsWith("\r");
      const lines = (this.#text + text).split(/\r\n|\r|\n/);
      this.#text = lines.pop();
      for (const line of lines) {
        if (this.readyState !== this.OPEN) break;
        this.#line(line);
      }
    }

    #line(line) {
      if (!line) {
        this.#cursor = this.#eventID;
        if (this.#data.length) {
          this.#dispatch(new MessageEvent(this.#type || "message", {
            data: this.#data.join("\n"), lastEventId: this.#cursor, origin: location.origin,
          }));
        }
        this.#type = "";
        this.#data = [];
        return;
      }
      const colon = line.indexOf(":");
      const field = colon < 0 ? line : line.slice(0, colon);
      const value = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
      if (field === "event") this.#type = value;
      else if (field === "data") this.#data.push(value);
      else if (field === "id" && !value.includes("\0")) this.#eventID = value;
      else if (field === "retry" && /^\d+$/.test(value)) this.#retry = Math.min(Number(value), 2147483647);
    }

    end(failed) {
      streams.delete(this.#id);
      this.readyState = failed ? this.CLOSED : this.CONNECTING;
      this.#dispatch(new Event("error"));
      if (this.readyState !== this.CLOSED) this.#timer = setTimeout(() => this.#start(), this.#retry);
    }

    close() {
      this.readyState = this.CLOSED;
      clearTimeout(this.#timer);
      if (streams.delete(this.#id)) send({ op: "sseClose", id: this.#id });
    }
  }

  window.EventSource = LocalEventSource;
  window.__dexLocalReceive = (kind, id, data) => {
    if (kind === "fetch") {
      const resolve = pending.get(id);
      pending.delete(id);
      resolve?.(data);
    } else if (kind === "chunk") {
      streams.get(id)?.chunk(data);
    } else if (kind === "end") {
      streams.get(id)?.end(data);
    }
  };
  addEventListener("pagehide", () => {
    for (const stream of streams.values()) stream.close();
  });
})();
