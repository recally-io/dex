// Dex.app loads the web app in a WKWebView with a message handler named "external". The shell
// answers dex:pick-folder with the dex:folder-picked window event; see desktop/wails3/folder_picker.go.
type External = { postMessage(message: string): void };

function external(): External | undefined {
  // SAFETY: the optional chain reads only properties that WKWebView defines; a browser leaves them undefined.
  const webkit = (window as { webkit?: { messageHandlers?: { external?: External } } }).webkit;

  return webkit?.messageHandlers?.external;
}

/** Whether the native folder panel is available: true only inside Dex.app. */
export function canPickFolder() {
  return external() !== undefined;
}

/** Opens the native folder panel. Resolves to the chosen absolute path, or "" when cancelled. */
export function pickFolder(start: string): Promise<string> {
  const handler = external();

  if (!handler) return Promise.resolve("");

  const id = crypto.randomUUID();

  return new Promise((resolve) => {
    const onPicked = (event: Event) => {
      // SAFETY: only the shell dispatches dex:folder-picked, always with an id and a path.
      const detail = (event as CustomEvent<{ id: string; path: string }>).detail;

      if (detail?.id !== id) return;

      window.removeEventListener("dex:folder-picked", onPicked);
      resolve(detail.path);
    };

    window.addEventListener("dex:folder-picked", onPicked);
    handler.postMessage("dex:pick-folder " + JSON.stringify({ id, start }));
  });
}
