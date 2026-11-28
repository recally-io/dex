// A draft that another page hands to a session's composer. It survives the navigation to that session,
// and the session takes it once. Session storage keeps long drafts out of the URL.
const keyFor = (botId: string, sessionId: number) => `dex-chat-draft:${botId}:${sessionId}`;

export const sessionDraftEvent = "dex-chat-draft";

export function putSessionDraft(botId: string, sessionId: number, text: string) {
  sessionStorage.setItem(keyFor(botId, sessionId), text);
  window.dispatchEvent(new Event(sessionDraftEvent));
}

export function takeSessionDraft(botId: string, sessionId: number) {
  const key = keyFor(botId, sessionId);
  const value = sessionStorage.getItem(key);

  sessionStorage.removeItem(key);

  return value;
}
