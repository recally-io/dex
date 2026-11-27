// Run against a loopback preview and its paired Dex instance. This check makes no API writes.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium } = require("../../web/node_modules/playwright-core");

async function main() {
  const port = Number(process.argv[2]);
  const output = process.argv[3];
  assert(Number.isInteger(port) && port > 1025 && port < 65536 && output, "Usage: node sidebar-check.cjs PORT SCREENSHOT_DIR");
  await fs.mkdir(output, { recursive: true, mode: 0o700 });
  const origin = (n) => `http://127.0.0.1:${n}`;
  const browser = await chromium.launch({ channel: "chrome" });
  try {
    for (const original of [true, false]) {
      const name = original ? "react" : "nsgo";
      const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 });
      const base = origin(port - Number(original));
      const response = await page.request.get(base + "/api/bots");
      assert(response.ok(), "Cannot read the preview roster");
      const { bots } = await response.json();
      const get = (id) => page.getByTestId(id);
      const screenshot = (size) => page.screenshot({ path: path.join(output, `${name}-${size}.png`), animations: "disabled" });
      await page.goto(base);
      const sidebar = () => original ? page.locator('aside[aria-label="Sidebar"]:visible') : get("sidebar");
      const botRow = (bot) => original ? sidebar().getByRole("link", { name: bot.name, exact: true }) : sidebar().getByTestId("nav-bot-" + bot.id);
      async function checkSidebar() {
        const side = sidebar();
        await side.waitFor();
        await (original ? side.getByRole("link").first() : side.locator('[data-testid^="nav-bot-"]').first()).waitFor();
        const rows = original ? side.locator('nav[aria-label="Bots"] a') : side.locator('[data-testid^="nav-bot-"]');
        await rows.nth(bots.length - 1).waitFor();
        const labels = await rows.evaluateAll((nodes) => nodes.map((e) => e.getAttribute("aria-label") || e.innerText));
        assert(labels.length === bots.length && bots.every((b, i) => labels[i] === b.name || labels[i].startsWith(b.name + ",")), "Bot names or order differ from the roster");
        const box = await side.boundingBox();
        assert.equal(Math.round(box.width), 260);
        const text = await side.innerText();
        for (const label of ["Search", "New bot", "Board", "Integrations", "Settings"]) assert(text.includes(label));
        for (const label of ["主对话", "Bot 设置", "Automations", "Sessions"]) assert(!text.includes(label));
        const footer = original ? side.getByRole("link", { name: "Settings" }) : side.getByTestId("nav-global-settings");
        const footerBox = await footer.boundingBox();
        assert(box.y + box.height - (footerBox.y + footerBox.height) < 16, "Workspace links must stay at the bottom");
        assert(box.x < 2, "The navigation must open from the left edge");
      }
      await checkSidebar();
      await screenshot("desktop");
      if (!original) {
        await get("nav-integrations").click(); await get("page-integrations").waitFor();
        assert.equal(new URL(page.url()).pathname, "/integrations");
        await get("nav-global-settings").click(); await get("page-global-settings").waitFor();
        assert.equal(new URL(page.url()).pathname, "/settings/general");
        const chosen = bots.find((b) => b.id !== "default") || bots[0];
        await botRow(chosen).click(); await get("page-main").waitFor();
        assert.equal(new URL(page.url()).pathname, "/b/" + encodeURIComponent(chosen.id));
        await get("nav-search").click(); await get("workspace-search").waitFor(); await get("workspace-search-close").click();
        await get("nav-new-bot").click(); await get("bots-create-name").waitFor(); await get("bots-create-cancel").click();
        await get("workspace-collapse").click(); await get("workspace-expand").click();
        await botRow(bots.find((b) => b.id === "default")).click();
      }
      await page.setViewportSize({ width: 390, height: 820 });
      const opener = original ? page.getByRole("button", { name: /Open navigation/ }) : get("workspace-menu");
      await opener.click();
      if (original) await page.getByRole("dialog", { name: "Navigation" }).evaluate(async (e) => { await Promise.all(e.getAnimations().map((a) => a.finished)); });
      await checkSidebar(); await screenshot("drawer");
      if (!original) {
        await get("nav-board").click(); await get("page-board").waitFor(); assert(!await sidebar().isVisible());
        await opener.click(); await get("nav-bot-default").click(); await get("bot-menu").click();
        await get("nav-sessions").click(); await get("sessions-list").waitFor();
      }
      console.log(`${name}: ${bots.length} bot rows; desktop and drawer passed`);
      await page.close();
    }
  } finally { await browser.close(); }
}
main().catch((err) => { console.error(err); process.exitCode = 1; });
