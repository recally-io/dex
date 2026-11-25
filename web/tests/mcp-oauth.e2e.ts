import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { once } from "node:events";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

const contentType = { "content-type": "application/json" };

test("authorizes an MCP connection and follows the legacy callback to Integrations", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/");

  const dex = new URL(await browser.url()).origin;

  const server = createServer((request, response) => {
    const origin = `http://${request.headers.host}`;
    const url = new URL(request.url ?? "/", origin);
    const json = (body: string) => response.writeHead(200, contentType).end(body);

    switch (url.pathname) {
      case "/mcp": {
        if (request.headers.authorization !== "Bearer browser-fixture-grant") {
          response
            .writeHead(401, { "www-authenticate": `Bearer resource_metadata="${origin}/resource"` })
            .end();
          break;
        }

        if (request.method !== "POST") {
          response.writeHead(405).end();
          break;
        }

        request.setEncoding("utf8");
        let body = "";

        request.on("data", (chunk: string) => {
          body += chunk;
        });
        request.on("end", () => {
          const rpc: { id?: number; method: string } = JSON.parse(body);

          if (rpc.id === undefined) {
            response.writeHead(202).end();

            return;
          }

          const result =
            rpc.method === "initialize"
              ? {
                  protocolVersion: "2025-11-25",
                  capabilities: { tools: {} },
                  serverInfo: { name: "browser test", version: "1" },
                }
              : { tools: [] };

          json(JSON.stringify({ jsonrpc: "2.0", id: rpc.id, result }));
        });
        break;
      }

      case "/resource":
        json(JSON.stringify({ resource: `${origin}/mcp`, authorization_servers: [origin] }));
        break;
      case "/.well-known/oauth-authorization-server":
        json(
          JSON.stringify({
            issuer: origin,
            authorization_endpoint: `${origin}/authorize`,
            token_endpoint: `${origin}/token`,
            response_types_supported: ["code"],
            code_challenge_methods_supported: ["S256"],
          }),
        );
        break;
      case "/authorize": {
        const callback = new URL(url.searchParams.get("redirect_uri") ?? "/", dex);

        if (callback.origin !== dex) {
          response.writeHead(400).end();
          break;
        }

        callback.search = new URLSearchParams({
          state: url.searchParams.get("state") ?? "",
          code: "ok",
        }).toString();
        response.writeHead(302, { location: callback.href }).end();
        break;
      }

      case "/token":
        json(JSON.stringify({ access_token: "browser-fixture-grant", token_type: "Bearer" }));
        break;
      default:
        response.writeHead(404).end();
    }
  });

  try {
    server.listen(0, "127.0.0.1");
    await once(server, "listening");

    // SAFETY: The TCP server already emitted listening, so Node returns a socket address.
    const { port } = server.address() as AddressInfo;

    const name = `OAuth ${Date.now()}`;

    const created = await fetch(`${dex}/api/agents`, {
      method: "POST",
      headers: contentType,
      body: JSON.stringify({
        name,
        provider: "faux",
        modelId: "tool",
        thinkingLevel: "off",
      }),
    });

    if (!created.ok) throw new Error(`Could not create OAuth test bot (${created.status})`);

    const { id }: { id: string } = await created.json();
    const integrations = `/a/${id}/integrations`;

    try {
      await app.open(`/a/${id}`);
      await screen
        .getByRole("textbox", `Message ${name}`)
        .fill(
          `mcp_server_set ${JSON.stringify({ name: "browser_oauth", level: "agent", config: { type: "http", url: `http://127.0.0.1:${port}/mcp`, oauth: { clientId: "browser-test" } } })}`,
        );
      await screen.getByRole("button", "Send message").tap();
      await screen.getByText(/^tool:.*Saved/).waitFor();
      await app.open(integrations);
      await screen.getByRole("button", { name: "Authorize", exact: true }).tap();
      await expect(screen.getByRole("button", { name: "Reauthorize", exact: true })).toBeVisible();
      await expect(screen.getByText("Connected", { exact: true })).toBeVisible();
      expect(new URL(await browser.url()).pathname).toBe(integrations);
    } finally {
      await fetch(`${dex}/api/agents/${id}`, { method: "DELETE" });
    }
  } finally {
    server.close();
  }
});
