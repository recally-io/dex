import { useSyncExternalStore } from "react";
import { client } from "../api/generated/client.gen";
import { getSession } from "../api/generated/sdk.gen";

// The API answers 401 when dex requires the access token and the browser has no session cookie.
let signedOut = false;
const listeners = new Set<() => void>();

client.interceptors.response.use((response) => {
  if (response.status === 401 && !signedOut) {
    signedOut = true;

    for (const listener of listeners) listener();
  }

  return response;
});

export function useSignedOut(): boolean {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);

      return () => listeners.delete(listener);
    },
    () => signedOut,
  );
}

// EventSource does not report the HTTP status of a failed stream. A request through the client
// does, and a 401 shows the sign-in form.
export function checkSession(): void {
  void getSession();
}
