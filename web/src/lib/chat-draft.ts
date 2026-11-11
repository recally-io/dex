// A draft that another page hands to a chat's composer. It survives the navigation to that chat,
// and the chat takes it once. Session storage keeps long drafts out of the URL.
const keyFor = (agentId: string, chatId: number) => `dex-chat-draft:${agentId}:${chatId}`;

export const chatDraftEvent = "dex-chat-draft";

export function putChatDraft(agentId: string, chatId: number, text: string) {
  sessionStorage.setItem(keyFor(agentId, chatId), text);
  window.dispatchEvent(new Event(chatDraftEvent));
}

export function takeChatDraft(agentId: string, chatId: number) {
  const key = keyFor(agentId, chatId);
  const value = sessionStorage.getItem(key);

  sessionStorage.removeItem(key);

  return value;
}
