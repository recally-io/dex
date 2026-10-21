// The macOS shell loads Dex from the backend URL, not from the Wails asset server. Wails then
// injects only its core script, which can post messages but has no drag handling.

declare global {
  interface Window {
    _wails?: { invoke: (message: string) => void };
  }
}

// Wails names its global; the bracket form keeps the lint rule against leading underscores quiet.
function invoke(message: string) {
  window["_wails"]?.invoke(message);
}

export function installMacShell() {
  document.documentElement.dataset.shell = "mac";
  // Elements with --dex-drag: drag move the window, as a native title bar does.
  window.addEventListener("mousedown", (event) => {
    if (event.button !== 0 || !(event.target instanceof Element)) return;

    if (getComputedStyle(event.target).getPropertyValue("--dex-drag").trim() !== "drag") return;

    invoke(event.detail === 2 ? "wails:drag:doubleclick" : "wails:drag");
  });
}
