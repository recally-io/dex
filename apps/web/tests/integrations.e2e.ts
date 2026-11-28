import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { once } from "node:events";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { basename, join, resolve } from "node:path";

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

async function createBot(dex: string, name: string, modelId = "echo") {
  const created = await fetch(`${dex}/api/bots`, {
    method: "POST",
    headers: contentType,
    body: JSON.stringify({ name, provider: "faux", modelId, thinkingLevel: "off" }),
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

  // When the owner opens Integrations from the footer, then it lists shared Skills and MCP servers.
  await footer.getByRole("link", "Integrations", { exact: true }).tap();
  await expect(browser).toHaveURL("/integrations");
  await expect(screen.getByRole("heading", "Integrations", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "Skills", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "MCP servers", { exact: true })).toBeVisible();
  await expect(screen.getByRole("combobox")).toHaveCount(0);
  await expect(screen.getByRole("heading", "Sources", { exact: true })).toHaveCount(0);

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

test("adds Bot-only and shared MCP servers and authorizes the shared server", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/");

  const dex = new URL(await browser.url()).origin;
  const { server, url } = await localOAuthServer(dex);
  const name = `Hub ${Date.now()}`;
  const id = await createBot(dex, name);
  const serverName = `hub_${Date.now() % 100000}`;
  // Holds the status read after a save. The finally block opens it even when a step fails.
  const statusGate = Promise.withResolvers<void>();
  const held = Promise.withResolvers<void>();

  try {
    // An old Bot parameter does not select a Bot. A target on the dialog does.
    await app.open(`/integrations?bot=${id}`);
    await expect(screen.getByRole("heading", "MCP servers", { exact: true })).toBeVisible();
    await expect(screen.getByRole("combobox")).toHaveCount(0);
    await screen.getByRole("button", "Add MCP server", { exact: true }).tap();

    let dialog = screen.getByRole("dialog", "Add MCP server");

    await expect(dialog.getByRole("option", "Shared (all Bots)", { exact: true })).toHaveCount(1);
    await expect(dialog.getByLabel("Target", { exact: true })).toHaveValue("");
    await dialog.getByLabel("Target", { exact: true }).selectOption({ value: id });
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
    await expect(screen.getByText(`${serverName}_open`, { exact: true })).toHaveCount(0);
    await app.open(`/b/${id}/integrations`);
    await expect(screen.getByText(`${serverName}_open`, { exact: true })).toBeVisible();
    await expect(screen.getByText("Not authorized", { exact: true })).toHaveCount(0);
    await expect(screen.getByRole("button", "Authorize", { exact: true })).toHaveCount(0);

    // The Bot's Add link opens the global dialog with that Bot selected. Switch to Shared for OAuth.
    await screen.getByRole("link", "Add MCP server", { exact: true }).tap();
    const addUrl = new URL(await browser.url());

    expect(addUrl.pathname).toBe("/integrations");
    expect(addUrl.searchParams.get("section")).toBe("mcp");
    expect(addUrl.searchParams.get("target")).toBe(id);
    expect(addUrl.searchParams.get("add")).toBe("mcp");
    dialog = screen.getByRole("dialog", "Add MCP server");
    await expect(dialog.getByLabel("Target", { exact: true })).toHaveValue(id);
    await dialog.getByLabel("Target", { exact: true }).selectOption({ value: "" });
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

    // While the status read after the save is slow, the dialog confirms the save and waits for
    // the read. It makes no claim from the list cached before the save.
    const statusPath = "**/api/integrations";
    let holdStatus = true;

    await browser.route(statusPath, async (route) => {
      if (holdStatus && route.request.method === "GET") await statusGate.promise;

      await route.continue();
    });
    await dialog.getByRole("button", "Add server", { exact: true }).tap();
    added = screen.getByRole("dialog", `Added ${serverName}`);
    await expect(
      added.getByText("Saved as shared. Bots inherit it unless they override or disable it.", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(added.getByText("Checking server status…", { exact: true })).toBeVisible();
    await expect(added.getByText(/is idle/)).toHaveCount(0);
    await expect(added.getByText("Not authorized", { exact: true })).toHaveCount(0);
    await expect(added.getByRole("button", "Authorize", { exact: true })).toHaveCount(0);
    holdStatus = false;
    statusGate.resolve();
    await browser.unroute(statusPath);

    // Then the read lists the stored server, and the next action is authorization. Saving does
    // not claim a connection.
    await expect(added.getByText("Checking server status…", { exact: true })).toHaveCount(0);
    await expect(added.getByText("Not authorized", { exact: true })).toBeVisible();
    await expect(added.getByText(/is idle/)).toHaveCount(0);
    await expect(added.getByText("Connected", { exact: true })).toHaveCount(0);

    // When the owner authorizes from the dialog while the start is slow, then the dialog stays
    // open: Done, Escape, the backdrop, and a second Authorize do nothing until it answers.
    const startPath = `**/api/integrations/mcp/${serverName}/oauth`;
    let starts = 0;

    await browser.route(startPath, async (route) => {
      starts += 1;
      await held.promise;
      await route.continue();
    });
    await added.getByRole("button", "Authorize", { exact: true }).tap();
    await expect(added.getByRole("button", "Starting…", { exact: true })).toBeDisabled();
    await expect(added.getByRole("button", "Done", { exact: true })).toBeDisabled();
    await added.press("Escape");
    await screen.tapAt({ x: 8, y: 8 });
    await expect(added).toBeVisible();
    await expect(added.getByRole("button", "Starting…", { exact: true })).toBeDisabled();

    // The shared callback returns to the global MCP section and reports the grant only.
    held.resolve();
    await expect(browser).toHaveURL("/integrations?section=mcp");
    const row = screen.getByRole("listitem").filter({ hasText: serverName });

    await expect(row.getByText("Authorized", { exact: true })).toBeVisible();
    await expect(row.getByText("Starts with the bot", { exact: true })).toHaveCount(0);
    await expect(row.getByText(/Connect(ed|ing)/)).toHaveCount(0);
    await expect(row.getByText("http", { exact: true })).toBeVisible();
    expect(starts).toBe(1);
    await browser.unroute(startPath);

    // A Bot shows the inherited server and links to the global section that manages it.
    await app.open(`/b/${id}/integrations`);

    const inherited = screen
      .getByRole("listitem")
      .filter({ hasText: serverName, hasNotText: `${serverName}_open` });

    await expect(inherited.getByText("Shared", { exact: true })).toBeVisible();
    await expect(inherited.getByText("Authorized", { exact: true })).toBeVisible();
    // Bot Settings does not change a shared entry. It links to the global page instead.
    await expect(inherited.getByRole("button", /authorize/i)).toHaveCount(0);
    await inherited.getByRole("link", "Manage in Integrations").tap();
    await expect(browser).toHaveURL("/integrations?section=mcp");
    await expect.poll(() => browser.evaluate(() => document.activeElement?.textContent)).toBe("MCP servers");
  } finally {
    held.resolve();
    statusGate.resolve();
    await fetch(`${dex}/api/bots/${id}`, { method: "DELETE" });
    server.close();
  }
});

test("refreshes shared skills when returning to Integrations", async ({ app, browser, screen }) => {
  // Given a cached empty inventory, leave Integrations without reloading the app.
  await app.open("/integrations");
  await expect(screen.getByText("No shared skills yet.", { exact: true })).toBeVisible();
  const { origin } = new URL(await browser.url());
  const dataDir = resolve(process.env.DEX_E2E_DATA_DIR || ".e2e/data");
  const health = await fetch(`${origin}/api/health`);
  const info: { dataDir: string } = await health.json();

  // Only write to the data directory configured for this test run.
  expect(info.dataDir).toBe(dataDir);
  const footer = screen.getByRole("navigation", "Workspace", { exact: true });

  await footer.getByRole("link", "Settings", { exact: true }).tap();
  await expect(browser).toHaveURL("/settings/general");
  const store = join(dataDir, ".agents", "skills");

  await mkdir(store, { recursive: true });
  const skill = await mkdtemp(join(store, "refresh-"));

  try {
    // When a skill appears in the shared store, then the sidebar return shows it.
    await writeFile(join(skill, "SKILL.md"), "---\ndescription: Shared skill added during the test\n---\n");
    await footer.getByRole("link", "Integrations", { exact: true }).tap();
    await expect(screen.getByText(basename(skill), { exact: true })).toBeVisible();
  } finally {
    await rm(skill, { recursive: true, force: true });
  }
});

test("does not redirect for an OAuth start after Back closes the add dialog", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/integrations");
  const dex = new URL(await browser.url()).origin;
  const { server, url } = await localOAuthServer(dex);
  const name = `back_${Date.now()}`;
  const startPath = `${dex}/api/integrations/mcp/${name}/oauth`;
  const held = Promise.withResolvers<void>();
  let upstreamStatus = 0;

  try {
    // Given a new shared declaration and a successful start response that the browser cannot read yet.
    await screen.getByRole("button", "Add MCP server", { exact: true }).tap();
    const dialog = screen.getByRole("dialog", "Add MCP server");

    await dialog.getByLabel("Name", { exact: true }).fill(name);
    await dialog.getByLabel("Server URL", { exact: true }).fill(url);
    await dialog.getByRole("button", "Browser OAuth", { exact: true }).tap();
    const created = browser.waitForResponse(`${dex}/api/integrations/mcp`);

    await dialog.getByRole("button", "Add server", { exact: true }).tap();
    expect((await created).status).toBe(201);
    await browser.route(startPath, async (route) => {
      const response = await route.fetch();
      const body = await response.body();

      upstreamStatus = response.status;
      await held.promise;
      await route.fulfill({ status: response.status, headers: response.headers, body });
    });
    const added = screen.getByRole("dialog", `Added ${name}`);

    await added.getByRole("button", "Authorize", { exact: true }).tap();
    await expect.poll(() => upstreamStatus).toBe(200);

    // When Back unmounts the dialog before the response arrives, then the URL stays unchanged.
    await browser.back();
    await expect(browser).toHaveURL("/integrations");
    await expect(added).toHaveCount(0);
    const delivered = browser.waitForResponse(startPath);

    held.resolve();
    await (await delivered).body();
    const deadline = Date.now() + 750;
    let unexpectedUrl: string | undefined;

    await expect
      .poll(async () => {
        const current = await browser.url();

        if (current !== `${dex}/integrations`) unexpectedUrl ??= current;

        return unexpectedUrl ?? (Date.now() >= deadline ? current : undefined);
      })
      .toBe(`${dex}/integrations`);
  } finally {
    held.resolve();
    server.close();
  }
});

test("blocks an unavailable target and installs a skill in a new Bot Session", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/integrations?add=mcp&target=missing-bot");
  const mcp = screen.getByRole("dialog", "Add MCP server");

  await mcp.getByLabel("Name", { exact: true }).fill("not-saved");
  await mcp.getByLabel("Server URL", { exact: true }).fill("http://127.0.0.1:9/mcp");
  await expect(mcp.getByText("This Bot is not available. Choose a target.")).toBeVisible();
  await expect(mcp.getByRole("button", "Add server", { exact: true })).toBeDisabled();

  const { origin } = new URL(await browser.url());
  const botId = await createBot(origin, `Skill review ${Date.now()}`);

  try {
    // Given a Bot with a model, follow its Add skill link and choose a catalog result.
    await app.open(`/b/${botId}/skills`);
    await expect(browser).toHaveURL(`/b/${botId}/integrations?section=skills`);
    await expect.poll(() => browser.evaluate(() => document.activeElement?.textContent)).toBe("Skills");
    await screen.getByRole("link", "Add skill", { exact: true }).tap();
    const search = screen.getByLabel("Search skill catalog", { exact: true });
    await expect
      .poll(() => browser.evaluate(() => document.activeElement?.getAttribute("id")))
      .toBe("skill-catalog-search");
    await search.fill("pdf");
    await screen.getByRole("button", "Load more skills", { exact: true }).tap();
    await expect(screen.getByText("PDF forms", { exact: true })).toBeVisible();
    await screen.getByRole("button", "Install PDF tools", { exact: true }).tap();
    const skill = screen.getByRole("dialog", "Install PDF tools");
    await expect(skill.getByLabel("Bot", { exact: true })).toHaveValue(botId);
    await expect(skill.getByLabel("Install level", { exact: true })).toHaveValue("agent");
    await skill.getByLabel("Directory name", { exact: true }).fill("pdf-reviewed");

    // When the Owner starts the install, then the new Session has the submitted review prompt.
    await skill.getByRole("button", "Start install", { exact: true }).tap();
    await expect(browser).toHaveURL(new RegExp(`/b/${botId}/s/\\d+$`));

    const sessionId = new URL(await browser.url()).pathname.split("/").at(-1);

    const readPrompt = async () => {
      const response = await fetch(
        `${origin}/api/bots/${botId}/sessions/${sessionId}/history?before=${Number.MAX_SAFE_INTEGER}`,
      );

      const history: { items?: { kind: string; text: string }[] } = await response.json();

      return history.items?.find((item) => item.kind === "user")?.text ?? "";
    };

    await expect.poll(readPrompt).toContain("pdf-reviewed");
    const prompt = await readPrompt();
    expect(prompt).toContain("https://github.com/anthropics/skills/tree/main/skills/pdf");
    expect(prompt).toContain("Owner confirms in this Session");
    expect(prompt).toContain('"level":"agent"');
  } finally {
    await fetch(`${origin}/api/bots/${botId}`, { method: "DELETE" });
  }
});

test("prefills an official MCP preset and adds a registry server with a vault header", async ({
  app,
  browser,
  screen,
}) => {
  // Given an official preset and a local registry with a server that requires a secret header.
  await app.open("/integrations?section=mcp");
  const dex = new URL(await browser.url()).origin;
  const botId = await createBot(dex, `Registry ${Date.now()}`);

  try {
    // When the owner chooses a preset, then its name, endpoint and OAuth option are ready.
    await screen.getByRole("button", "Add Notion", { exact: true }).tap();
    const dialog = screen.getByRole("dialog", "Add MCP server");
    await expect(dialog.getByLabel("Name", { exact: true })).toHaveValue("notion");
    await expect(dialog.getByLabel("Server URL", { exact: true })).toHaveValue("https://mcp.notion.com/mcp");
    await expect(dialog.getByRole("button", "Browser OAuth", { exact: true })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await dialog.getByLabel("Target", { exact: true }).selectOption({ value: botId });
    await dialog.getByRole("button", "Add server", { exact: true }).tap();
    const addedPreset = screen.getByRole("dialog", "Added notion");
    await expect(addedPreset.getByRole("button", "Authorize", { exact: true })).toBeVisible();
    await addedPreset.getByRole("button", "Done", { exact: true }).tap();

    // The Owner does not start OAuth. The only MCP endpoint contacted is local.
    await screen.getByLabel("Search MCP Registry", { exact: true }).fill("fixture");
    await screen.getByRole("button", "Load more MCP servers", { exact: true }).tap();
    await expect(screen.getByText("Local package fixture", { exact: true })).toBeVisible();
    await expect(screen.getByRole("button", "Add Local package fixture", { exact: true })).toHaveCount(0);
    await screen.getByRole("button", "Add Registry fixture", { exact: true }).tap();
    await expect(dialog.getByLabel("Name", { exact: true })).toHaveValue("fixture-auth");
    await expect(dialog.getByLabel("Header 1 name", { exact: true })).toHaveValue("Authorization");
    await expect(dialog.getByRole("switch", "Header 1 secret", { exact: true })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await dialog.getByLabel("Target", { exact: true }).selectOption({ value: botId });
    const secret = "registry-secret-value";
    await dialog.getByLabel("Header 1 value", { exact: true }).fill(`Bearer ${secret}`);
    await dialog.getByRole("button", "Add server", { exact: true }).tap();
    await expect(screen.getByRole("dialog", "Added fixture-auth")).toBeVisible();

    // Then the declaration refers to the Bot vault, which lists the name and omits the value.
    const root = `${dex}/api/bots/${botId}`;
    const inventory = await (await fetch(`${root}/integrations`)).json();
    const saved = inventory.servers.find((server: { name: string }) => server.name === "fixture-auth");
    const reference = saved?.vaultReferences?.headers?.Authorization;
    expect(reference).toMatch(/^\$\{vault:MCP_[A-Za-z0-9_]+\}$/);
    const vault = await (await fetch(`${root}/vault`)).text();
    const entries: { name: string; scope: string }[] = JSON.parse(vault);
    const entry = entries.find((item) => item.name === reference.slice("${vault:".length, -1));
    expect(entry?.scope).toBe("agent");
    expect(vault).not.toContain(secret);
    expect(JSON.stringify(inventory)).not.toContain(secret);
  } finally {
    await fetch(`${dex}/api/bots/${botId}`, { method: "DELETE" });
  }
});

test("adds Google MCP presets with a registered client and reuses its vault secret", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/integrations?section=mcp");
  const dex = new URL(await browser.url()).origin;
  const botId = await createBot(dex, `Google client ${Date.now()}`, "tool");
  const root = `${dex}/api/bots/${botId}`;
  const clientId = "fixture.apps.googleusercontent.com";
  const secret = "google-fixture-client-secret";

  const gmailScope = ["gmail.readonly", "gmail.compose", "gmail.modify"]
    .map((part) => `https://www.googleapis.com/auth/${part}`)
    .join(" ");

  async function storedOAuth(name: string) {
    const created = await fetch(`${root}/sessions`, { method: "POST" });
    expect(created.status).toBe(201);
    const { id }: { id: number } = await created.json();

    const sent = await fetch(`${root}/sessions/${id}/messages`, {
      method: "POST",
      headers: contentType,
      body: JSON.stringify({
        id: crypto.randomUUID(),
        text: `mcp_server_get ${JSON.stringify({ name, level: "agent" })}`,
      }),
    });

    expect(sent.ok).toBe(true);

    const read = async () => {
      const response = await fetch(`${root}/sessions/${id}/history?before=${Number.MAX_SAFE_INTEGER}`);
      const history: { items: { kind: string; text?: string }[] } = await response.json();

      return (
        history.items
          .find((item) => item.kind === "assistant" && item.text?.startsWith("tool: {"))
          ?.text?.slice(6) ?? ""
      );
    };

    await expect.poll(read).toContain('"clientId"');
    const declaration = await read();
    expect(declaration).not.toContain(secret);

    const saved: { Config?: { oauth?: { clientId: string; clientSecret: string; scope: string } } } =
      JSON.parse(declaration);

    if (!saved.Config?.oauth) throw new Error(`Saved declaration has no OAuth settings: ${declaration}`);

    return saved.Config.oauth;
  }

  try {
    // Given a Google preset, the Owner needs a Web application client and a scope.
    await screen.getByRole("button", "Add Gmail", { exact: true }).tap();
    const dialog = screen.getByRole("dialog", "Add MCP server");
    const add = dialog.getByRole("button", "Add server", { exact: true });
    await expect(dialog.getByLabel("Server URL", { exact: true })).toHaveValue(
      "https://gmailmcp.googleapis.com/mcp/v1",
    );
    await expect(dialog.getByText(`${dex}/mcp/oauth/callback`, { exact: true })).toBeVisible();
    await expect(dialog.getByLabel("Scope", { exact: true })).toHaveValue(gmailScope);
    await expect(add).toBeDisabled();

    // When the Owner adds the client, then Dex saves the scopes and a Bot vault reference.
    await dialog.getByLabel("Target", { exact: true }).selectOption({ value: botId });
    await dialog.getByLabel("Client ID", { exact: true }).fill(clientId);
    await dialog.getByLabel("Client secret", { exact: true }).fill(secret);
    await add.tap();
    const gmail = screen.getByRole("dialog", "Added gmail");
    await expect(gmail.getByRole("button", "Authorize", { exact: true })).toBeVisible();
    await gmail.getByRole("button", "Done", { exact: true }).tap();
    const oauth = await storedOAuth("gmail");
    expect(oauth.clientId).toBe(clientId);
    expect(oauth.clientSecret).toMatch(/^vault:MCP_OAUTH_CLIENT_[a-f0-9]+$/);
    expect(oauth.scope).toBe(gmailScope);
    const secretName = oauth.clientSecret.slice("vault:".length);

    // A second preset can use this name without saving another copy of the secret.
    await screen.getByRole("button", "Add Google Drive", { exact: true }).tap();
    await dialog.getByLabel("Target", { exact: true }).selectOption({ value: botId });
    await dialog.getByLabel("Client ID", { exact: true }).fill(clientId);
    await dialog.getByRole("button", "Use existing vault secret", { exact: true }).tap();
    await dialog.getByLabel("Client secret vault name", { exact: true }).selectOption({ value: secretName });
    await add.tap();
    await expect(screen.getByRole("dialog", "Added google-drive")).toBeVisible();
    const drive = await storedOAuth("google-drive");
    expect(drive.clientSecret).toBe(oauth.clientSecret);
    expect(drive.scope).toBe(
      "https://www.googleapis.com/auth/drive.readonly https://www.googleapis.com/auth/drive.file",
    );
    const vault = await (await fetch(`${root}/vault`)).text();
    const entries: { name: string; scope: string }[] = JSON.parse(vault);
    expect(entries.flatMap((entry) => (entry.scope === "agent" ? [entry.name] : []))).toEqual([secretName]);
    expect(vault).not.toContain(secret);
  } finally {
    await fetch(root, { method: "DELETE" });
  }
});

test("keeps a saved MCP server confirmed when its status cannot load", async ({ app, browser, screen }) => {
  await app.open("/");

  const dex = new URL(await browser.url()).origin;
  const name = `Status ${Date.now()}`;
  const id = await createBot(dex, name);
  const serverName = `status_${Date.now() % 100000}`;
  let creates = 0;
  let failStatus = false;

  try {
    // Given a Bot whose status read can be made to fail. The server is never contacted.
    await browser.route(`**/api/bots/${id}/integrations/mcp`, async (route) => {
      if (route.request.method === "POST") creates += 1;

      await route.continue();
    });
    await browser.route(`**/api/bots/${id}/integrations`, async (route) => {
      if (failStatus && route.request.method === "GET") {
        await route.fulfill({ status: 500, json: { error: "status read failed in the test" } });

        return;
      }

      await route.continue();
    });
    await app.open(`/integrations?section=mcp&target=${id}&add=mcp`);

    // When the owner adds an OAuth server, and the status read after the save fails.

    const dialog = screen.getByRole("dialog", "Add MCP server");

    await dialog.getByLabel("Name", { exact: true }).fill(serverName);
    await dialog.getByLabel("Server URL", { exact: true }).fill("http://127.0.0.1:9/mcp");
    await dialog.getByRole("button", "Browser OAuth", { exact: true }).tap();
    failStatus = true;
    await dialog.getByRole("button", "Add server", { exact: true }).tap();

    // Then the dialog still confirms the save, says that the status is unknown, and makes no
    // claim about the connection or the Bot being idle.
    const added = screen.getByRole("dialog", `Added ${serverName}`);

    await expect(
      added.getByText(`Saved for ${name}. Other Bots do not change.`, { exact: true }),
    ).toBeVisible();
    await expect(added.getByText("Could not load the server status", { exact: true })).toBeVisible();
    await expect(added.getByText(/is idle/)).toHaveCount(0);
    await expect(added.getByText("Starts with the bot", { exact: true })).toHaveCount(0);
    await expect(added.getByText("Not authorized", { exact: true })).toHaveCount(0);
    await expect(added.getByRole("button", "Authorize", { exact: true })).toHaveCount(0);
    // The way to authorize stays clear: the Bot's Integrations page lists the server.
    await expect(added.getByRole("link", `Open ${name} Integrations`, { exact: true })).toHaveAttribute(
      "href",
      `/b/${id}/integrations`,
    );

    // When the read works again and the owner retries, then the stored server and its next action
    // show, without a second save.
    failStatus = false;
    await added.getByRole("button", "Try again", { exact: true }).tap();
    await expect(added.getByText("Not authorized", { exact: true })).toBeVisible();
    await expect(added.getByRole("button", "Authorize", { exact: true })).toBeEnabled();
    expect(creates).toBe(1);
    await added.getByRole("button", "Done", { exact: true }).tap();
    await expect(screen.getByRole("dialog", `Added ${serverName}`)).toHaveCount(0);
    await app.open(`/b/${id}/integrations`);
    await expect(screen.getByText(serverName, { exact: true })).toBeVisible();
  } finally {
    await fetch(`${dex}/api/bots/${id}`, { method: "DELETE" });
  }
});
