import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { once } from "node:events";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

const contentType = { "content-type": "application/json" };

/** A local MCP server with an OAuth issuer that approves every request. No real service. */
async function localOAuthServer(dex: string): Promise<{ server: Server; url: string }> {
  const server = createServer((request, response) => {
    const origin = `http://${request.headers.host}`;
    const url = new URL(request.url ?? "/", origin);
    const json = (body: string) => response.writeHead(200, contentType).end(body);

    switch (url.pathname) {
      case "/mcp": {
        if (request.headers.authorization !== "Bearer hub-fixture-grant") {
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

          json(
            JSON.stringify({
              jsonrpc: "2.0",
              id: rpc.id,
              result:
                rpc.method === "initialize"
                  ? {
                      protocolVersion: "2025-11-25",
                      capabilities: { tools: {} },
                      serverInfo: { name: "hub fixture", version: "1" },
                    }
                  : { tools: [] },
            }),
          );
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
            registration_endpoint: `${origin}/clients`,
            response_types_supported: ["code"],
            code_challenge_methods_supported: ["S256"],
          }),
        );
        break;
      case "/clients":
        if (request.method !== "POST") {
          response.writeHead(405).end();
          break;
        }

        response.writeHead(201, contentType).end(
          JSON.stringify({
            client_id: "hub-client",
            redirect_uris: [dex + "/mcp/oauth/callback"],
            token_endpoint_auth_method: "none",
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
        json(JSON.stringify({ access_token: "hub-fixture-grant", token_type: "Bearer" }));
        break;
      default:
        response.writeHead(404).end();
    }
  });

  server.listen(0, "127.0.0.1");
  await once(server, "listening");

  // SAFETY: The TCP server already emitted listening, so Node returns a socket address.
  const { port } = server.address() as AddressInfo;

  return { server, url: `http://127.0.0.1:${port}/mcp` };
}

async function createBot(dex: string, name: string) {
  const created = await fetch(`${dex}/api/bots`, {
    method: "POST",
    headers: contentType,
    body: JSON.stringify({ name, provider: "faux", modelId: "echo", thinkingLevel: "off" }),
  });

  if (!created.ok) throw new Error(`Could not create a test bot (${created.status})`);

  const { id }: { id: string } = await created.json();

  return id;
}

test("keeps Board, Integrations, and Settings in a fixed navigation footer", async ({
  app,
  browser,
  screen,
}) => {
  // Given a short desktop window, so the Bots list must scroll.
  await browser.setViewport({ width: 1280, height: 360 });
  await app.open("/b/default");

  // Then the footer lists Board, Integrations, and Settings in this order, inside the window.
  const footer = screen.getByRole("navigation", "Workspace", { exact: true });

  await expect(footer.getByRole("link", /^Board/)).toBeVisible();

  const layout = await browser.evaluate(() => {
    const nav = document.querySelector('aside nav[aria-label="Workspace"]');
    const bots = document.querySelector('aside nav[aria-label="Bots"]');

    return {
      labels: [...(nav?.querySelectorAll("a") ?? [])].map((link) => link.textContent?.trim() ?? ""),
      bottom: nav?.getBoundingClientRect().bottom ?? Infinity,
      height: window.innerHeight,
      botsScroll: bots ? getComputedStyle(bots).overflowY : "",
    };
  });

  expect(layout.labels.map((label) => label.replace(/\d+$/, ""))).toEqual([
    "Board",
    "Integrations",
    "Settings",
  ]);
  expect(layout.bottom <= layout.height).toBe(true);
  expect(layout.botsScroll).toBe("auto");

  // When the owner opens Integrations from the footer, then the hub names the selected Bot.
  await footer.getByRole("link", "Integrations", { exact: true }).tap();
  await expect(browser).toHaveURL("/integrations?bot=default");
  await expect(screen.getByRole("heading", "Integrations", { exact: true })).toBeVisible();
  await expect(screen.getByRole("combobox", "Bot", { exact: true })).toHaveValue("default");
  await expect(
    screen.getByText("Channels and new MCP servers belong to this Bot. Inherited Dex servers are shared."),
  ).toBeVisible();

  // A global page has no Bot actions.
  await expect(screen.getByRole("navigation", "Bot navigation")).toHaveCount(0);
  await expect(screen.getByRole("button", /^Bot menu/)).toHaveCount(0);

  // When the window is narrow, then the drawer has the same footer.
  await browser.setViewport({ width: 390, height: 640 });
  await app.open("/b/default");
  await screen.getByRole("button", /^Open navigation/).tap();

  const drawer = screen.getByRole("dialog", "Navigation");

  await expect(drawer.getByRole("navigation", "Workspace").getByRole("link", /^Board/)).toBeVisible();
  await expect(drawer.getByRole("link", "Integrations", { exact: true })).toBeVisible();
  await expect(drawer.getByRole("link", "Settings", { exact: true })).toBeVisible();

  // On the narrow window, Add MCP server fits without clipped choices, even with Advanced options open.
  await app.open("/integrations?bot=default");
  await screen.getByRole("button", "Add MCP server", { exact: true }).tap();

  const dialog = screen.getByRole("dialog", "Add MCP server");

  await dialog.getByRole("button", "Browser OAuth", { exact: true }).tap();
  await dialog.getByRole("button", "Advanced options", { exact: true }).tap();
  await expect(dialog.getByLabel("Client ID", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", "No authentication", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", "Add server", { exact: true })).toBeVisible();

  const fit = await browser.evaluate(() => {
    const box = document.querySelector('[role="dialog"]');
    const segments = [...(box?.querySelectorAll("[aria-pressed]") ?? [])];

    return {
      overflow: box ? box.scrollWidth - box.clientWidth : -1,
      right: box?.getBoundingClientRect().right ?? Infinity,
      width: window.innerWidth,
      clipped: segments.filter((item) => item.scrollWidth > item.clientWidth).length,
    };
  });

  expect(fit).toEqual({ overflow: 0, right: fit.right, width: 390, clipped: 0 });
  expect(fit.right <= fit.width).toBe(true);
});

test("shows an unsaved instructions draft on the mobile Bot menu", async ({ app, browser, screen }) => {
  await app.open("/");

  const dex = new URL(await browser.url()).origin;
  const name = `Menu ${Date.now()}`;
  const id = await createBot(dex, name);

  try {
    // Given a Bot with unsaved instructions on a narrow window.
    await browser.setViewport({ width: 390, height: 720 });
    await app.open(`/b/${id}/bot`);
    await screen.getByRole("textbox", "Bot instructions").fill("Unsaved draft from the menu test");
    // A page load discards the draft, so open Main from the drawer.
    await screen.getByRole("button", /^Open navigation/).tap();
    await screen.getByRole("dialog", "Navigation").getByRole("link", `${name}, instructions draft`).tap();
    await expect(browser).toHaveURL(`/b/${id}`);

    // Then the Bot menu names the draft, and its Bot settings item keeps the Draft mark.
    const menu = screen.getByRole("button", `Bot menu: ${name}, unsaved instructions draft`, { exact: true });

    await expect(menu).toBeVisible();
    await expect(screen.getByRole("button", "Artifacts", { exact: true })).toBeVisible();
    await menu.tap();
    await expect(
      screen.getByRole("menuitem", "Bot settings, unsaved instructions draft", { exact: true }),
    ).toBeVisible();
  } finally {
    await fetch(`${dex}/api/bots/${id}`, { method: "DELETE" });
  }
});

test("adds a custom OAuth MCP server to the selected Bot only", async ({ app, browser, screen }) => {
  await app.open("/");

  const dex = new URL(await browser.url()).origin;
  const { server, url } = await localOAuthServer(dex);
  const name = `Hub ${Date.now()}`;
  const id = await createBot(dex, name);
  const serverName = `hub_${Date.now() % 100000}`;

  try {
    // Given the hub opened for an unknown Bot, then it asks for a Bot and shows no Bot settings.
    await app.open("/integrations?bot=missing-bot");
    await expect(screen.getByText("This Bot is not available. Choose a Bot.")).toBeVisible();
    await expect(screen.getByRole("button", "Add MCP server", { exact: true })).toHaveCount(0);
    await expect(screen.getByRole("heading", "Sources", { exact: true })).toBeVisible();

    // When the owner selects the new Bot.
    await screen.getByRole("combobox", "Bot", { exact: true }).selectOption({ value: id });
    await expect(browser).toHaveURL(`/integrations?bot=${id}`);
    await expect(screen.getByRole("heading", "Channels", { exact: true })).toBeVisible();
    await expect(screen.getByRole("heading", "Sources", { exact: true })).toBeVisible();

    // When the owner opens Add MCP server, then the form names the target Bot and asks only for a
    // name, a URL, and authentication. No authentication is the default.
    await screen.getByRole("button", "Add MCP server", { exact: true }).tap();

    let dialog = screen.getByRole("dialog", "Add MCP server");

    await expect(dialog.getByText(`Bot: ${name}`, { exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", "No authentication", { exact: true })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(dialog.getByRole("button", "Advanced options", { exact: true })).toHaveCount(0);
    await expect(dialog.getByLabel("Client ID", { exact: true })).toHaveCount(0);

    // When the owner types a pre-registered client under OAuth, and then chooses No authentication.
    await dialog.getByLabel("Name", { exact: true }).fill(`${serverName}_open`);
    await dialog.getByLabel("Server URL", { exact: true }).fill(url.replace(/\/mcp$/, "/open"));
    await dialog.getByRole("button", "Browser OAuth", { exact: true }).tap();

    let advanced = dialog.getByRole("button", "Advanced options", { exact: true });

    await expect(advanced).not.toBeExpanded();
    await expect(dialog.getByLabel("Client ID", { exact: true })).toHaveCount(0);
    await advanced.tap();
    await expect(advanced).toBeExpanded();
    await dialog.getByLabel("Client ID", { exact: true }).fill("typed-client");
    await dialog.getByRole("button", "No authentication", { exact: true }).tap();
    await dialog.getByRole("button", "Add server", { exact: true }).tap();

    // Then the dialog says what happens next. The server has no authorization and is not live yet.
    let added = screen.getByRole("dialog", `Added ${serverName}_open`);

    await expect(added.getByText(/^Dex connects when this Bot starts or reloads\./)).toBeVisible();
    await expect(added.getByText("Starts with the bot", { exact: true })).toBeVisible();
    await expect(added.getByRole("button", /^Authorize/)).toHaveCount(0);
    await added.getByRole("button", "Done", { exact: true }).tap();
    await expect(screen.getByText(`${serverName}_open`, { exact: true })).toBeVisible();
    await expect(screen.getByText("Not authorized", { exact: true })).toHaveCount(0);
    await expect(screen.getByRole("button", "Authorize", { exact: true })).toHaveCount(0);

    // When the owner adds a server with browser OAuth and keeps Advanced options closed.
    await screen.getByRole("button", "Add MCP server", { exact: true }).tap();
    dialog = screen.getByRole("dialog", "Add MCP server");
    await dialog.getByLabel("Name", { exact: true }).fill(serverName);
    await dialog.getByLabel("Server URL", { exact: true }).fill(url);
    await dialog.getByRole("button", "Browser OAuth", { exact: true }).tap();

    // An invalid vault name stays reported, and blocks saving, while Advanced options are closed.
    advanced = dialog.getByRole("button", "Advanced options", { exact: true });
    await advanced.tap();
    await dialog.getByLabel("Client secret vault name", { exact: true }).fill("not a name");
    await advanced.tap();
    await expect(
      dialog.getByText("Check Advanced options. Use a vault name: letters, digits, and _.", { exact: true }),
    ).toBeVisible();
    await expect(dialog.getByRole("button", "Add server", { exact: true })).toBeDisabled();
    await advanced.tap();
    await expect(dialog.getByLabel("Client secret vault name", { exact: true })).toHaveValue("not a name");
    await dialog.getByLabel("Client secret vault name", { exact: true }).fill("");
    await advanced.tap();
    await dialog.getByRole("button", "Add server", { exact: true }).tap();

    // Then the next action is authorization. Saving does not claim a connection.
    added = screen.getByRole("dialog", `Added ${serverName}`);
    await expect(added.getByText("Not authorized", { exact: true })).toBeVisible();
    await expect(added.getByText("Connected", { exact: true })).toHaveCount(0);

    // When the owner authorizes from the dialog, then the existing callback returns to Bot Integrations.
    await added.getByRole("button", "Authorize", { exact: true }).tap();
    await expect(screen.getByRole("button", "Reauthorize", { exact: true })).toBeVisible();
    expect(new URL(await browser.url()).pathname).toBe(`/b/${id}/integrations`);

    // A grant alone is not a live connection: this Bot has not started yet.
    const row = screen
      .getByRole("listitem")
      .filter({ hasText: serverName, hasNotText: `${serverName}_open` });

    await expect(row.getByText("Authorized", { exact: true })).toBeVisible();
    await expect(row.getByText("Starts with the bot", { exact: true })).toBeVisible();
    await expect(row.getByText("Connected", { exact: true })).toHaveCount(0);

    // When the owner reloads the Bot, then the authorized connection is live.
    await screen.getByRole("button", "Reload", { exact: true }).tap();
    await expect(row.getByText("Connected", { exact: true })).toBeVisible();
    await expect(row.getByText("Authorized", { exact: true })).toBeVisible();

    // And the default Bot does not get the declaration.
    await app.open("/integrations?bot=default");
    await expect(screen.getByRole("combobox", "Bot", { exact: true })).toHaveValue("default");
    await expect(
      screen.getByText("Channels and new MCP servers belong to this Bot. Inherited Dex servers are shared."),
    ).toBeVisible();
    await expect(screen.getByRole("heading", "MCP servers", { exact: true })).toBeVisible();
    await expect(screen.getByText(serverName, { exact: true })).toHaveCount(0);
  } finally {
    await fetch(`${dex}/api/bots/${id}`, { method: "DELETE" });
    server.close();
  }
});
