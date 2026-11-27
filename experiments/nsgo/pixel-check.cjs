// Run the same read-only synthetic Main against the two local preview origins.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium } = require("../../web/node_modules/playwright-core");

const usage = "Usage: node pixel-check.cjs NSGO_PORT OUTPUT_DIR (React listens on NSGO_PORT - 1)";
const edgeLimit = 1;
const colorLimit = 1;
const channelNoise = 16;
const regionLimit = 0.05;
const draftText = "Review the release notes";
const viewportCases = [
  { screen: "desktop", width: 1280, height: 800, open: true },
  { screen: "desktop", width: 1280, height: 800, open: false },
  { screen: "phone", width: 390, height: 820, open: false },
  { screen: "phone", width: 390, height: 820, open: true },
];

async function readResponse(origin, route) {
  const res = await fetch(origin + route, { redirect: "error", signal: AbortSignal.timeout(8_000) });
  assert(res.ok, `${origin}${route}: HTTP ${res.status}`);
  return res;
}

async function snapshot(origin, route) {
  const response = await readResponse(origin, `${route}/events`);
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let text = "";
  try {
    while (text.length < 1_000_000) {
      const { value, done } = await reader.read();
      if (done) break;
      text += value;
      const event = text.match(/^event: snapshot\r?\ndata: ([^\n]+)\r?\n\r?\n/m);
      if (event) return JSON.parse(event[1]);
    }
  } finally {
    await reader.cancel();
  }
  throw new Error(`${origin}: no complete Main snapshot`);
}

async function readFixture(origin) {
  const read = async (route) => (await readResponse(origin, route)).json();
  const [health, roster] = await Promise.all([read("/api/health"), read("/api/bots")]);
  const bot = roster.bots.find(({ id }) => id === "default");
  assert(bot?.provider === "faux" && bot.modelId === "echo", `${origin}: expected the synthetic default faux/echo Bot`);
  assert(health.startedAt && health.dataDir, `${origin}: missing service identity`);
  const main = await read("/api/bots/default/sessions/main");
  assert(main.main && main.readOnly === false && main.id > 0, `${origin}: expected a writable Main`);
  const snap = await snapshot(origin, `/api/bots/default/sessions/${main.id}`);
  const items = snap.items.filter(({ kind }) => kind === "user" || kind === "assistant");
  assert(!snap.running && items.length === 2 && items[0].kind === "user" && items[1].kind === "assistant", `${origin}: expected one completed user/assistant turn`);
  const user = items[0].text;
  const reply = items[1].text;
  assert(/^[ -~]{1,120}$/.test(user) && reply === `echo: ${user}`, `${origin}: expected a short English echo fixture`);
  return {
    instance: [health.startedAt, health.dataDir], main: main.id, user, reply, botName: bot.name,
    bots: roster.bots.map(({ id, name }) => ({ id, name })),
  };
}

function uiFor(page, target, fixture) {
  const web = target === "react";
  const test = (id) => page.getByTestId(id);
  const button = (name) => page.getByRole("button", { name, exact: typeof name === "string" });
  const sidebar = web ? page.locator('aside[aria-label="Sidebar"]:visible') : test("sidebar");
  const header = web ? page.locator("#main > section > header:visible") : test("workspace-header");
  const draft = web ? page.locator('textarea[data-slot="textarea"]:visible') : test("draft");
  const send = web ? page.locator('button[aria-label="Send message"]:visible') : test("send");
  const transcript = web ? page.locator("#main") : test("history");
  const botRow = ({ id, name }) => web ? sidebar.getByRole("link", { name, exact: true }) : sidebar.getByTestId(`nav-bot-${id}`);
  return {
    sidebar, header, draft, send, botRow,
    title: header.getByText(fixture.botName, { exact: true }),
    search: web ? sidebar.getByRole("button", { name: "Search bots and sessions" }) : test("nav-search"),
    newBot: web ? sidebar.getByRole("button", { name: "New bot" }) : test("nav-new-bot"),
    footer: [
      web ? sidebar.getByRole("link", { name: /^Board\b/ }) : test("nav-board"),
      web ? sidebar.getByRole("link", { name: "Integrations", exact: true }) : test("nav-integrations"),
      web ? sidebar.getByRole("link", { name: "Settings", exact: true }) : test("nav-global-settings"),
    ],
    collapse: web ? button("Hide sidebar") : test("workspace-collapse"),
    expand: web ? button(/^Show sidebar\b/) : test("workspace-expand"),
    openDrawer: web ? button(/^Open navigation\b/) : test("workspace-menu"),
    drawer: web ? page.getByRole("dialog", { name: "Navigation", exact: true }) : test("workspace-drawer"),
    closeDrawer: web ? button("Close navigation") : test("workspace-close"),
    artifacts: web ? page.locator('button[aria-label="Artifacts"]:visible') : test("nav-artifacts"),
    botMenu: web ? button(/^Bot menu:/) : test("bot-menu"),
    desktopActions: [
      web ? button("New session") : test("nav-new-session"),
      web ? page.getByRole("link", { name: "Sessions", exact: true }) : test("nav-sessions"),
      web ? page.getByRole("link", { name: /^Automations\b/ }) : test("nav-automations"),
      web ? page.getByRole("link", { name: /^Bot settings\b/ }) : test("nav-settings"),
    ],
    attach: web ? page.locator('button[aria-label="Attach files"]:visible') : test("composer-files-add"),
    activity: web ? page.locator('button[aria-label="Session activity"]:visible') : test("composer-activity"),
    user: web ? transcript.locator(".rounded-bubble").filter({ hasText: fixture.user }) : transcript.getByText(fixture.user, { exact: true }),
    reply: transcript.getByText(fixture.reply, { exact: true }),
  };
}

async function measure(page, ui, target, scenario, fixture) {
  const { screen, open } = scenario;
  const bounds = {};
  const unavailable = [];
  const add = async (name, locator) => {
    const selected = await locator.count();
    const box = selected === 1 ? await locator.boundingBox() : null;
    if (!box || box.width <= 0 || box.height <= 0) unavailable.push(`${name} (${selected} matching elements)`);
    else bounds[name] = [box.x, box.y, box.x + box.width, box.y + box.height];
  };
  const pairs = [];
  if (open) {
    pairs.push(["sidebar", ui.sidebar], ["search", ui.search], ["new-bot", ui.newBot]);
    pairs.push(...fixture.bots.map((bot, index) => [`bot-row-${index + 1}`, ui.botRow(bot)]));
    pairs.push(...ui.footer.map((locator, index) => [["board", "integrations", "settings"][index], locator]));
  }
  if (screen === "phone" && open) {
    pairs.push(["drawer", ui.drawer], ["close-navigation", ui.closeDrawer]);
  } else {
    pairs.push(["header", ui.header], ["header-title", ui.title], ["artifacts", ui.artifacts], ["input", ui.draft], ["send", ui.send],
      ["attach", ui.attach], ["activity", ui.activity], ["user-bubble", ui.user], ["reply-text", ui.reply]);
    if (screen === "phone") pairs.push(["open-navigation", ui.openDrawer], ["bot-menu", ui.botMenu]);
    else {
      pairs.push(["sidebar-toggle", open ? ui.collapse : ui.expand]);
      pairs.push(...ui.desktopActions.map((locator, index) => [["new-session", "sessions", "automations", "bot-settings"][index], locator]));
    }
  }
  await Promise.all(pairs.map(([name, locator]) => add(name, locator)));
  if (!(screen === "phone" && open) && bounds.input && bounds.send) {
    // The native input has its own wrapper. Find the card that holds both controls.
    const composer = await ui.draft.evaluate((node, target) => {
      let card = node.closest("form");
      if (target === "nsgo") {
        const send = document.querySelector('[data-testid="send"]');
        card = node;
        while (card && !card.contains(send)) card = card.parentElement;
      }
      if (!card) return null;
      const box = card.getBoundingClientRect();
      return [box.left, box.top, box.right, box.bottom];
    }, target);
    if (composer) bounds.composer = composer;
    else unavailable.push("composer card");
    if (target === "nsgo" && bounds["user-bubble"]) {
      const painted = await ui.user.evaluate((node) => {
        for (let element = node; element && element.dataset.testid !== "history"; element = element.parentElement) {
          if (parseFloat(getComputedStyle(element).borderTopLeftRadius) > 0) {
            const box = element.getBoundingClientRect();
            return [box.left, box.top, box.right, box.bottom];
          }
        }
        return null;
      });
      if (painted) bounds["user-bubble"] = painted;
      else unavailable.push("painted user bubble");
    }
  }
  return { bounds, unavailable };
}

async function capture(browser, target, origin, fixture, scenario, folder) {
  const { screen, open, colorScheme, width, height } = scenario;
  const context = await browser.newContext({
    viewport: { width, height }, deviceScaleFactor: 1, locale: "en-US", timezoneId: "UTC",
    colorScheme, reducedMotion: "reduce", hasTouch: screen === "phone", serviceWorkers: "block",
  });
  const page = await context.newPage();
  page.setDefaultTimeout(12_000);
  const take = () => page.screenshot({ path: path.join(folder, `${target}.png`), animations: "disabled", caret: "hide", timeout: 12_000 });
  let png;
  try {
    await context.route("**/*", (route) => {
      const request = route.request();
      return new URL(request.url()).origin === origin && ["GET", "HEAD"].includes(request.method()) ? route.continue() : route.abort();
    });
    await page.goto(`${origin}/b/default`, { waitUntil: "domcontentloaded", timeout: 12_000 });
    const ui = uiFor(page, target, fixture);
    await ui.reply.waitFor();
    await ui.draft.waitFor();
    assert.equal(await ui.draft.inputValue(), "", `${target}: expected an empty draft in a new context`);
    if (screen === "desktop" && !open) await ui.collapse.click();
    if (screen === "phone" && open) {
      await ui.openDrawer.click();
      await ui.drawer.waitFor();
    }
    await ui.sidebar.waitFor({ state: open ? "visible" : "hidden" });
    if (open) await Promise.all(fixture.bots.map((bot) => ui.botRow(bot).waitFor()));
    await page.mouse.move(width - 1, 0);
    await ui.draft.blur();
    await page.evaluate(async () => { await document.fonts.ready; });
    png = await take();
    const result = await measure(page, ui, target, scenario, fixture);
    if (!(screen === "phone" && open)) {
      try {
        await ui.draft.fill(draftText);
        await ui.send.click({ trial: true });
        await ui.draft.blur();
        await page.mouse.move(width - 1, 0);
        result.enabledSend = await page.screenshot({ path: path.join(folder, `${target}-enabled-send.png`),
          clip: await ui.send.boundingBox(), animations: "disabled", caret: "hide" });
      } catch (error) {
        result.error = `enabled Send capture: ${error.message}`;
      }
    }
    return { png, ...result };
  } catch (error) {
    return { png: png ?? await take().catch(() => null), bounds: {}, unavailable: [], error: error.message };
  } finally {
    await context.close();
  }
}

function union(a, b) {
  return [Math.floor(Math.min(a[0], b[0])), Math.floor(Math.min(a[1], b[1])),
    Math.ceil(Math.max(a[2], b[2])), Math.ceil(Math.max(a[3], b[3]))];
}

async function comparePixels(page, react, nsgo, regions) {
  return page.evaluate(async ({ images, regions, channelNoise }) => {
    async function readPNG(encoded) {
      const bytes = Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0));
      const bitmap = await createImageBitmap(new Blob([bytes], { type: "image/png" }));
      const canvas = new OffscreenCanvas(bitmap.width, bitmap.height);
      const ctx = canvas.getContext("2d", { willReadFrequently: true });
      ctx.drawImage(bitmap, 0, 0);
      bitmap.close();
      return ctx.getImageData(0, 0, canvas.width, canvas.height);
    }
    const [a, b] = await Promise.all(images.map(readPNG));
    const width = a.width;
    const height = a.height;
    if (b.width !== width || b.height !== height) throw new Error(`PNG size mismatch: ${width}x${height} vs ${b.width}x${b.height}`);
    const dist = (arr, x, other, y) => Math.max(Math.abs(arr[x] - other[y]), Math.abs(arr[x + 1] - other[y + 1]), Math.abs(arr[x + 2] - other[y + 2]), Math.abs(arr[x + 3] - other[y + 3]));
    const offset = (x, y) => 4 * (y * width + x);
    const pixels = width * height;
    const difference = new Uint8Array(pixels);
    let exact = 0;
    let strong = 0;
    let max = 0;
    for (let i = 0; i < pixels; i++) {
      const d = difference[i] = dist(a.data, 4 * i, b.data, 4 * i);
      if (d) exact++;
      if (d > channelNoise) strong++;
      max = Math.max(max, d);
    }
    function modal(data, left, top, right, bottom) {
      const histogram = new Map();
      let max = 0;
      let packed = 0;
      for (let y = top; y < bottom; y++) for (let x = left; x < right; x++) {
        const i = offset(x, y);
        const key = (data[i] << 24) | (data[i + 1] << 16) | (data[i + 2] << 8) | data[i + 3];
        const n = (histogram.get(key) ?? 0) + 1;
        histogram.set(key, n);
        if (n > max) { max = n; packed = key; }
      }
      return [packed >>> 24, (packed >>> 16) & 255, (packed >>> 8) & 255, packed & 255];
    }
    function near(from, index, to, x, y) {
      for (let ny = Math.max(0, y - 1); ny <= Math.min(height - 1, y + 1); ny++) {
        for (let nx = Math.max(0, x - 1); nx <= Math.min(width - 1, x + 1); nx++) {
          if (dist(from, index, to, offset(nx, ny)) <= channelNoise) return true;
        }
      }
      return false;
    }
    const stats = {};
    const marked = new Uint8Array(pixels);
    for (const [label, rect] of Object.entries(regions)) {
      const [l, t, r, d] = rect;
      const left = Math.max(0, l), top = Math.max(0, t), right = Math.min(width, r), bottom = Math.min(height, d);
      const area = (right - left) * (bottom - top);
      if (right <= left || bottom <= top) throw new Error(`Region outside screenshot: ${label}`);
      const baseA = modal(a.data, left, top, right, bottom);
      const baseB = modal(b.data, left, top, right, bottom);
      let active = 0;
      let exact = 0;
      let strong = 0;
      let unmatched = 0;
      for (let y = top; y < bottom; y++) for (let x = left; x < right; x++) {
        const i = y * width + x;
        const index = i * 4;
        const d = difference[i];
        if (d) exact++;
        if (d > channelNoise) strong++;
        // Exclude only pixels that are flat in both crops and do not differ.
        if (d > channelNoise || dist(a.data, index, baseA, 0) > channelNoise || dist(b.data, index, baseB, 0) > channelNoise) active++;
        if (d > channelNoise && (!near(a.data, index, b.data, x, y) || !near(b.data, index, a.data, x, y))) {
          unmatched++;
          marked[i] = 1;
        }
      }
      stats[label] = { area, exact, strong, active, unmatched, baseA, baseB, fillDelta: dist(baseA, 0, baseB, 0) };
    }
    const canvas = new OffscreenCanvas(width, height);
    const ctx = canvas.getContext("2d");
    const image = ctx.createImageData(width, height);
    const dest = image.data;
    for (let i = 0; i < pixels; i++) {
      const x = i * 4;
      const delta = difference[i];
      if (marked[i]) dest.set([255, 24, 88, 255], x);
      else if (delta > channelNoise) dest.set([255, 170, 45, 255], x);
      else if (delta > 0) dest.set([121, 179, 232, 255], x);
      else {
        const shade = Math.round(40 + (a.data[x] + a.data[x + 1] + a.data[x + 2]) / 12);
        dest.set([shade, shade, shade, 255], x);
      }
    }
    ctx.putImageData(image, 0, 0);
    const bytes = new Uint8Array(await (await canvas.convertToBlob({ type: "image/png" })).arrayBuffer());
    let binary = "";
    for (let start = 0; start < bytes.length; start += 16_384) binary += String.fromCharCode(...bytes.subarray(start, start + 16_384));
    return { whole: { width, height, pixels, exact, strong, max }, stats, png: btoa(binary) };
  }, { images: [react, nsgo].map((png) => png.toString("base64")), regions, channelNoise });
}

const number = (n) => n.toFixed(2);
const percent = (n, d) => `${number(d ? n * 100 / d : 0)}%`;

async function runCase(browser, canvas, origins, fixture, scenario, root) {
  const name = `${scenario.screen}-${scenario.open ? "open" : "closed"}-${scenario.colorScheme}`;
  const folder = path.join(root, name);
  await fs.mkdir(folder);
  const [react, nsgo] = await Promise.all(Object.entries(origins).map(([target, origin]) => capture(browser, target, origin, fixture, scenario, folder)));
  const failures = [];
  const lines = [];
  const regions = {};
  for (const [target, item] of Object.entries({ react, nsgo })) {
    if (item.error) failures.push(`${target} capture: ${item.error}`);
    for (const loc of item.unavailable) failures.push(`${target}: missing ${loc}`);
  }
  const names = [...new Set([...Object.keys(react.bounds), ...Object.keys(nsgo.bounds)])].sort();
  const edges = {};
  for (const label of names) {
    const a = react.bounds[label], b = nsgo.bounds[label];
    if (!a || !b) continue;
    const deltas = a.map((n, i) => b[i] - n);
    const max = Math.max(...deltas.map(Math.abs));
    edges[label] = { react: a, nsgo: b, deltas, max };
    const line = `${label} Δ[L,T,R,B]=[${deltas.map(number).join(", ")}] px`;
    lines.push(`  edges ${line}`);
    if (max > edgeLimit) failures.push(`edges ${line}`);
    if (!["sidebar", "drawer", "header"].includes(label)) regions[label] = union(a, b);
  }
  const { width, height, screen, open } = scenario;
  const drawerOpen = screen === "phone" && open;
  // Include nearby paint that does not belong to a single control, such as message metadata.
  if (regions.composer && regions["user-bubble"]) {
    regions["transcript-tail"] = [regions.composer[0], regions["user-bubble"][1], regions.composer[2], regions.composer[1]];
  }
  if (edges.header) regions["header-band"] = union(edges.header.react, edges.header.nsgo);
  if (drawerOpen && edges.drawer) {
    const start = Math.ceil(Math.max(edges.drawer.react[2], edges.drawer.nsgo[2]));
    if (start < width) {
      regions["drawer-exposed-header"] = [start, 0, width, 48];
      regions["drawer-exposed-tail"] = [start, height - 180, width, height];
    }
  }
  const patchX = drawerOpen ? width - 28 : Math.floor((react.bounds.header?.[0] ?? 0) + (width - (react.bounds.header?.[0] ?? 0)) / 2);
  const patches = { [drawerOpen ? "drawer-backdrop" : "page-background"]: [patchX, 200, patchX + 12, 212] };
  if (open && react.bounds["new-bot"]?.[3] < 336) patches["sidebar-background"] = [36, 350, 48, 362];
  Object.assign(regions, patches);
  for (const [name, [left, top, right, bottom]] of Object.entries(regions)) {
    if (Math.max(0, left) >= Math.min(width, right) || Math.max(0, top) >= Math.min(height, bottom)) {
      failures.push(`crop ${name}: outside screenshot or empty`);
      delete regions[name];
    }
  }
  let pixels;
  if (react.png && nsgo.png) {
    try {
      const { whole, stats, png } = await comparePixels(canvas, react.png, nsgo.png, regions);
      await fs.writeFile(path.join(folder, "diff.png"), Buffer.from(png, "base64"));
      pixels = { whole, stats };
      if (whole.width !== width || whole.height !== height) failures.push(`PNG size: expected ${width}x${height}, got ${whole.width}x${whole.height}`);
      lines.unshift(`  whole ${whole.width}x${whole.height}: ${whole.exact}/${whole.pixels} different (${percent(whole.exact, whole.pixels)}); ${whole.strong} above ${channelNoise}; max channel ${whole.max}`);
      for (const [label, s] of Object.entries(stats)) {
        if (label in patches) {
          lines.push(`  color ${label}: React ${s.baseA.join(",")} | NSGo ${s.baseB.join(",")} | Δ ${s.fillDelta}`);
          if (s.fillDelta > colorLimit) failures.push(`color ${label}: max channel Δ ${s.fillDelta}`);
        } else {
          const detail = `${label}: ${s.exact}/${s.area} different; ${s.unmatched}/${s.active} active pixels unmatched (${percent(s.unmatched, s.active)})`;
          lines.push(`  crop ${detail}`);
          if (s.active && s.unmatched / s.active > regionLimit) failures.push(`crop ${detail}`);
        }
      }
      if (react.enabledSend && nsgo.enabledSend) {
        // The enabled control can have different outer bounds. Sample the central side fill in each image.
        const sample = async (png) => canvas.evaluate(async (encoded) => {
          const bitmap = await createImageBitmap(new Blob([Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0))], { type: "image/png" }));
          const c = new OffscreenCanvas(bitmap.width, bitmap.height);
          const ctx = c.getContext("2d");
          ctx.drawImage(bitmap, 0, 0);
          bitmap.close();
          return [...ctx.getImageData(4, Math.floor(c.height / 2), 1, 1).data];
        }, png.toString("base64"));
        const [a, b] = await Promise.all([sample(react.enabledSend), sample(nsgo.enabledSend)]);
        const delta = Math.max(...a.map((n, i) => Math.abs(n - b[i])));
        lines.push(`  color enabled-send: React ${a.join(",")} | NSGo ${b.join(",")} | Δ ${delta}`);
        if (delta > colorLimit) failures.push(`color enabled-send: max channel Δ ${delta}`);
        pixels.enabledSend = { react: a, nsgo: b, delta };
      }
    } catch (err) {
      failures.push(`PNG comparison: ${err.message}`);
    }
  }
  const status = failures.length ? "FAIL" : "PASS";
  const report = [`${status} ${name}`, ...failures.map((error) => `  ! ${error}`), ...lines].join("\n");
  await fs.writeFile(path.join(folder, "metrics.json"), JSON.stringify({ name, status, failures, edges, pixels }, null, 2) + "\n");
  console.log(`${status} ${name}: ${failures.length} failures`);
  return { status, report };
}

async function main() {
  const [portArg, dir, ...extra] = process.argv.slice(2);
  if (portArg === "--help" && !dir) {
    console.log(usage);
    return;
  }
  const port = Number(portArg);
  assert(/^\d{1,5}$/.test(portArg) && port > 1025 && port < 65_536 && dir && !extra.length, usage);
  const origin = (n) => `http://127.0.0.1:${n}`;
  const origins = { react: origin(port - 1), nsgo: origin(port) };
  const output = path.resolve(dir);
  await fs.mkdir(output, { recursive: true, mode: 0o700 });
  const root = await fs.mkdtemp(path.join(output, "run-"));
  const report = [
    "Dex synthetic Main pixel comparison",
    `Run: ${new Date().toISOString()}`,
    `Inputs: React ${origins.react}; NSGo ${origins.nsgo}; CSS pixels at device scale 1`,
    `Artifacts: ${root}`,
    `Gate: each box edge ≤${edgeLimit} CSS px; solid fills ≤${colorLimit} channel; each non-flat crop ≤${percent(regionLimit, 1)} unmatched active pixels`,
    `Unmatched: channel Δ>${channelNoise} after a 1 CSS px neighborhood check in both images. Flat pixels do not dilute a crop. Whole-image exact RGBA counts never decide the result.`,
    "Diff legend: pink = unmatched in a checked crop, amber = other strong change, blue = smaller change.",
  ];
  let browser;
  try {
    const [fixture, actual] = await Promise.all(Object.values(origins).map(readFixture));
    assert.deepEqual(actual.instance, fixture.instance, "React and NSGo must use the same synthetic Dex instance");
    const { instance: _react, ...expected } = fixture;
    const { instance: _nsgo, ...observed } = actual;
    assert.deepEqual(observed, expected, "React and NSGo do not have the same fixture");
    report.push(`Case: /b/default; ${fixture.bots.length} Bots; one completed English echo; empty and enabled draft captures. No request writes.`, "");
    browser = await chromium.launch({ channel: "chrome", args: ["--disable-background-networking", "--disable-component-update"] });
    const canvas = await browser.newPage();
    for (const colorScheme of ["light", "dark"]) for (const spec of viewportCases) {
      const result = await runCase(browser, canvas, origins, fixture, { ...spec, colorScheme }, root);
      report.push(result.report, "");
      if (result.status === "FAIL") process.exitCode = 1;
    }
  } catch (error) {
    report.push(`ERROR ${error.message}`);
    console.error(error.message);
    process.exitCode = 1;
  } finally {
    await browser?.close();
    const text = report.join("\n") + "\n";
    await fs.writeFile(path.join(root, "metrics-report.txt"), text);
    await fs.writeFile(path.join(output, "metrics-report.txt"), text);
    console.log(path.join(output, "metrics-report.txt"));
  }
}

main().catch((err) => { console.error(err.message); process.exitCode = 1; });
