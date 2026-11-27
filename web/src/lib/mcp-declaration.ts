import { createMcpServer, createSharedMcpServer, deleteVault, putVault } from "../api/generated/sdk.gen";
import type { McpServerInput } from "../api/generated/types.gen";
import { errorText } from "./queries";
import { newId } from "./utils";

export type McpHeaderDraft = {
  name: string;
  value: string;
  secret: boolean;
  description?: string;
  required?: boolean;
  prefix?: string;
};

export type McpDraft = {
  name: string;
  url: string;
  auth: "none" | "oauth" | "header";
  headers?: McpHeaderDraft[];
};

export const mcpServerName = /^[A-Za-z0-9_-]{1,32}$/;
const headerName = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

export function requiresVault(name: string): boolean {
  return ["authorization", "proxy-authorization", "cookie", "x-api-key", "api-key"].includes(
    name.trim().toLowerCase(),
  );
}

export function checkMcpHeaders(headers: McpHeaderDraft[]): string | undefined {
  if (!headers.length) return "Add at least one HTTP header.";

  const names = new Set<string>();

  for (const header of headers) {
    const name = header.name.trim().toLowerCase();

    if (!headerName.test(name)) return "Enter a valid name for each HTTP header.";

    if (names.has(name)) return "Use a different name for each HTTP header.";

    if (!header.value.trim()) return "Enter a value for each HTTP header.";

    names.add(name);
  }
}

export async function saveMcpDeclaration(
  botId: string | undefined,
  body: McpServerInput,
  headers: McpHeaderDraft[],
) {
  const scope = botId ? "agent" : "dex";
  const path = { botId: botId ?? "default" };
  const id = newId().replaceAll("-", "");
  const prefix = `MCP_${body.name.replaceAll("-", "_")}_${id}`;
  const secrets = new Map<string, string>();
  const values = new Map<string, string>();

  for (const header of headers) {
    const name = header.name.trim();
    const value = header.value.trim();

    if (header.secret || requiresVault(name)) {
      const key = `${prefix}_${secrets.size + 1}`;

      secrets.set(key, value);
      values.set(name, `${header.prefix ?? ""}\${vault:${key}}`);
    } else {
      values.set(name, value);
    }
  }

  const keys = [...secrets.keys()];

  async function removeSecrets() {
    const removed = await Promise.allSettled(
      keys.map((name) =>
        deleteVault({
          path,
          query: { name, scope },
          throwOnError: true,
        }),
      ),
    );

    return removed.flatMap((result, i) => (result.status === "rejected" ? [keys[i]!] : []));
  }

  // Wait for every PUT before cleanup. A PUT can succeed even if the response is lost.
  const writes = await Promise.allSettled(
    [...secrets].map(([name, value]) => putVault({ path, body: { scope, name, value }, throwOnError: true })),
  );

  const failed = writes.find((result) => result.status === "rejected");

  if (failed) {
    const remaining = await removeSecrets();

    throw new Error(
      `Could not store the header secrets. ${errorText(failed.reason)}${remaining.length ? ` Check Secrets for ${remaining.join(", ")}.` : ""}`,
      { cause: failed.reason },
    );
  }

  if (values.size) body.headers = Object.fromEntries(values);

  const result = botId ? await createMcpServer({ path, body }) : await createSharedMcpServer({ body });

  if (result.response?.status === 201) return;

  if (!keys.length) throw result.error;

  if ([400, 401, 403, 404, 409, 413].includes(result.response?.status ?? 0)) {
    const remaining = await removeSecrets();

    if (!remaining.length) throw result.error;

    throw new Error(`${errorText(result.error)} Check Secrets for ${remaining.join(", ")}.`);
  }

  // A lost response can hide a saved declaration. Keep its credentials until the owner checks.
  throw new Error(
    `${errorText(result.error)} Check Integrations and Secrets (${keys.join(", ")}) before trying again.`,
  );
}
