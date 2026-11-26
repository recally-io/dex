// A draft that another page hands to a session's composer. It survives the navigation to that session,
// and the session takes it once. Session storage keeps long drafts out of the URL.
const keyFor = (agentId: string, sessionId: number) => `dex-chat-draft:${agentId}:${sessionId}`;

export const sessionDraftEvent = "dex-chat-draft";

export function putSessionDraft(agentId: string, sessionId: number, text: string) {
  sessionStorage.setItem(keyFor(agentId, sessionId), text);
  window.dispatchEvent(new Event(sessionDraftEvent));
}

export function takeSessionDraft(agentId: string, sessionId: number) {
  const key = keyFor(agentId, sessionId);
  const value = sessionStorage.getItem(key);

  sessionStorage.removeItem(key);

  return value;
}
