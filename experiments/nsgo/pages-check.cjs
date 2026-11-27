// Usage: mise exec -- node experiments/nsgo/pages-check.cjs PORT OUT
// PORT is NSGo; React uses PORT - 1. Use a new OUT for each run.
// --create-session=react|nsgo enables one UI create on the disposable 19544/19545 pair.
// PAGES_QA_SESSION_ID and PAGES_QA_REPLY select a seeded Session and its exact reply.
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const fs = require('node:fs/promises');
const { stripVTControlCharacters } = require('node:util');
const path = require('node:path');
const { chromium } = require('../../web/node_modules/playwright-core');

const [, , rawPort, out, option, ...extra] = process.argv;
const port = Number(rawPort);
const writeClient = option?.match(/^--create-session=(react|nsgo)$/)?.[1];
const timeout = 3500;
const viewports = { desktop: { width: 1280, height: 800 }, phone: { width: 390, height: 820 } };
const label = (value) => new RegExp(`^${escapeRegExp(value)}(?:,.*)?$`);
const botPath = (id) => `/b/${encodeURIComponent(id)}`;
const apiBot = (id) => `/api/bots/${encodeURIComponent(id)}`;
const routeOf = (page) => { const u = new URL(page.url()); return u.pathname + u.search; };
const getOrigin = (client) => `http://127.0.0.1:${client === 'react' ? port - 1 : port}`;
const sha256 = (value) => createHash('sha256').update(value).digest('hex');
const readPaths = [
  /^\/api\/(?:health|bots|archived-bots|models|settings|settings\/(?:runners|sources|provider-models)|directories|integrations|board\/attention|sources\/held-events|automations\/results)$/,
  /^\/api\/tasks(?:\/events|\/executions|\/T-\d+)?$/,
  /^\/api\/bots\/[^/]+(?:\/(?:channels|notification-targets|vault|artifacts|automations|runners|integrations|projects|sessions)(?:\/[^/]+(?:\/(?:runs|history|events|settings|monitors|outbox|project))?)?)?$/,
];

async function json(context, origin, route) {
  assert(readPaths.some((pattern) => pattern.test(new URL(route, origin).pathname)), `Read not allowed: ${route}`);
  const response = await context.request.get(origin + route, { timeout, maxRedirects: 0 });
  assert(response.ok(), `GET ${route}: ${response.status()}`);
  return response.json();
}

async function snapshot(context, origin, bot) {
  const api = apiBot(bot);
  const [roster, profile, defaults, main, sessions, artifacts, automations, tasks, integrations, archived] = await Promise.all([
    '/api/bots', api, apiBot('default'), `${api}/sessions/main`, `${api}/sessions`, `${api}/artifacts`,
    `${api}/automations`, '/api/tasks', '/api/integrations', '/api/archived-bots',
  ].map((url) => json(context, origin, url)));
  const pick = (rows, keys) => (rows || []).map((row) => Object.fromEntries(keys.map((key) => [key, row[key] ?? null])));
  return {
    bots: pick(roster.bots, ['id', 'name']),
    profile: pick([profile, defaults], ['id', 'name', 'modelId', 'provider', 'thinkingLevel', 'runnerId']),
    main: main.id,
    sessions: pick(sessions.sessions, ['id', 'title', 'archivedAt']),
    artifacts: pick(artifacts.artifacts, ['name']),
    automations: pick(automations.automations, ['id', 'name', 'enabled']),
    tasks: pick(tasks.tasks, ['key', 'title', 'status']),
    skills: pick(integrations.skills, ['name']),
    servers: pick(integrations.servers, ['name']),
    archived: pick(archived.bots, ['id', 'name']),
  };
}

async function browserContext(browser, client, size, journal, createBot) {
  const origin = getOrigin(client);
  const context = await browser.newContext({ viewport: viewports[size], deviceScaleFactor: 1,
    colorScheme: 'light', locale: 'en-US', timezoneId: 'UTC', reducedMotion: 'reduce', serviceWorkers: 'block' });
  let attempted = false;
  let projectAttempted = false;
  const createResponse = createBot && context.waitForEvent('response', {
    predicate: (res) => res.url() === `${origin}${apiBot(createBot)}/sessions` && res.request().method() === 'POST',
    timeout: 15000,
  }).then(async (res) => {
    journal.push({ method: 'POST', pathname: `${apiBot(createBot)}/sessions`, status: res.status(), allowed: true });
    return { status: res.status(), id: res.ok() ? (await res.json()).id : null };
  }).catch(() => ({ status: null, id: null }));
  await context.route('**/*', async (route) => {
    const request = route.request();
    const { origin: seenOrigin, pathname } = new URL(request.url());
    const method = request.method();
    let allowed = seenOrigin === origin && ['GET', 'HEAD'].includes(method) &&
      (!pathname.startsWith('/api/') || readPaths.some((pattern) => pattern.test(pathname)));
    if (seenOrigin === origin && createBot && method === 'POST' && pathname === `${apiBot(createBot)}/sessions` && !attempted) {
      attempted = true;
      return route.continue();
    }
    if (seenOrigin === origin && createBot && attempted && !projectAttempted && method === 'PUT' &&
      pathname.startsWith(`${apiBot(createBot)}/sessions/`) && pathname.endsWith('/project')) {
      const { id } = await createResponse;
      if (!projectAttempted && Number.isInteger(id) && pathname === `${apiBot(createBot)}/sessions/${id}/project`) {
        projectAttempted = true;
        allowed = true;
      }
    }
    if (!allowed || !['GET', 'HEAD'].includes(method)) journal.push({ method, pathname: seenOrigin === origin ? pathname : '<external>', allowed });
    return allowed ? route.continue() : route.abort('blockedbyclient');
  });
  await context.routeWebSocket('**/*', (socket) => socket.close());
  return { context, origin, createResponse };
}

function controls(page, react, phone) {
  const get = (id) => page.getByTestId(id).filter({ visible: true });
  const role = (kind, name, scope = page) => scope.getByRole(kind, { name, exact: typeof name === 'string' });
  const text = (name, scope = page) => scope.getByText(name, { exact: typeof name === 'string' }).filter({ visible: true }).first();
  const side = react ? page.locator('aside[aria-label="Sidebar"]:visible') : get('sidebar');
  const header = react ? page.locator('#main header:visible').first() : get('workspace-header');
  const nav = react ? role('navigation', 'Bot navigation', header) : header;
  const menu = react ? role('button', /^Bot menu:/, header) : get('bot-menu');
  const botAction = (id, name, kind = 'link') => react
    ? role(phone ? 'menuitem' : kind, label(name), phone ? page : nav) : get(`nav-${id}`);
  const artifacts = react ? role('button', 'Artifacts', header) : get('nav-artifacts');
  const drawer = react ? role('button', /^Open navigation/, header) : get('workspace-menu');
  const title = (name, scope = page) => react ? role('heading', name, scope).last() :
    scope.getByText(name, { exact: true }).and(page.locator(':visible:not([data-testid="workspace-header"] *, [data-testid="sidebar"] *)')).first();
  const field = (kind, name, id) => (react ? role(kind, name) : get(id)).first();
  return { get, role, text, side, header, menu, botAction, artifacts, drawer, title, field };
}

async function ready(locator) { await locator.waitFor({ timeout }); return locator; }
async function click(locator) { await locator.click({ timeout }); }
async function trial(locator) { await locator.click({ trial: true, timeout }); }
async function at(page, target) {
  try { await page.waitForURL((u) => u.pathname + u.search === target, { timeout, waitUntil: 'commit' }); }
  catch { throw new Error(`Expected route ${target}; observed ${routeOf(page)}`); }
}
async function goto(page, origin, target) {
  const response = await page.goto(origin + target, { waitUntil: 'domcontentloaded', timeout: 12000 });
  assert(response?.ok(), `Direct load ${target}: ${response?.status()}`);
}
const visibleBox = async (locator) => await locator.first().isVisible() ? locator.first().boundingBox() : null;

async function measure(page, c, title, action) {
  const [pane, heading, primary, sidebar] = await Promise.all([c.header, title, action, c.side].map((loc) => loc ? visibleBox(loc) : null));
  const overflow = await page.evaluate(() => Math.max(0, document.documentElement.scrollWidth - innerWidth));
  return { pane, heading, primary, sidebar, overflow };
}

async function runPage(env, name) {
  const { context, origin, client, size, fixture, bot, results, journal } = env;
  const page = await context.newPage();
  const react = client === 'react';
  const phone = size === 'phone';
  const c = controls(page, react, phone);
  const item = { client, size, page: name, checks: [], images: {}, measurements: {} };
  results.push(item);
  const check = async (key, fn) => {
    try {
      await fn();
      item.checks.push({ key, pass: true });
      console.log(`GREEN ${client}/${size}/${name} ${key}`);
      return true;
    } catch (error) {
      const reason = stripVTControlCharacters(error.message).split('\n').slice(0, 4).join(' ').replaceAll(origin, '<preview>');
      item.checks.push({ key, pass: false, reason });
      console.log(`RED   ${client}/${size}/${name} ${key}: ${reason}`);
      return false;
    }
  };
  const capture = async (stage) => {
    const filename = `${client}-${size}-${name}-${stage}.png`;
    await check(`screenshot:${stage}`, async () => {
      await page.screenshot({ path: path.join(out, 'screenshots', filename), animations: 'disabled', timeout: 5000 });
      item.images[stage] = { file: `screenshots/${filename}`, route: routeOf(page) };
    });
  };
  const bp = botPath(bot);
  const sessions = fixture.sessions.filter((s) => !s.archivedAt);
  const session = sessions.find((row) => String(row.id) === process.env.PAGES_QA_SESSION_ID) ||
    sessions.find((row) => row.title === 'QA Pages — Session') || sessions.find((row) => row.title) || sessions[0];
  const sessionRoute = session && `${bp}/s/${session.id}`;
  const task = fixture.tasks.find((row) => ['ready', 'in_progress', 'in_review', 'backlog'].includes(row.status));
  const automation = fixture.automations.find((row) => row.name === 'QA Pages — Paused automation') || fixture.automations[0];
  const exactReply = process.env.PAGES_QA_REPLY || (session?.title === 'QA Pages — Session' ? 'echo: QA Pages — hello' : '');
  const expected = (rows, empty, scope = page) => async () => {
    if (!rows.length) await ready(c.text(empty, scope));
    else for (const value of rows) await ready(c.text(value, scope));
  };
  const botEntry = (id, name) => async () => {
    if (phone) await click(c.menu);
    await click(c.botAction(id, name));
  };
  const globalEntry = (id, name) => async () => {
    if (phone) {
      await click(c.drawer);
      if (id === 'board') {
        await ready(c.side);
        await capture('drawer');
        await check('drawer width and footer order', async () => {
          const box = await c.side.boundingBox();
          const entries = await Promise.all([['board', 'Board'], ['integrations', 'Integrations'], ['global-settings', 'Settings']].map(([key, title]) =>
            visibleBox(react ? c.role('link', label(title), c.side) : c.get(`nav-${key}`))));
          item.measurements.drawer = { box, entries };
          assert(box && box.x >= -1 && box.x <= 1 && box.width <= viewports[size].width * 0.85 + 1, 'Drawer must open from the left and stay within 85% of the viewport');
          assert(entries.every(Boolean) && entries[0].y < entries[1].y && entries[1].y < entries[2].y, 'Wrong workspace footer order');
        });
      }
    }
    const entry = react ? c.role('link', label(name), c.side) : c.get(`nav-${id}`);
    await click(entry);
    if (phone) await c.side.waitFor({ state: 'hidden', timeout });
  };
  const artifactPane = react ? c.role('dialog', 'Artifacts') : c.get('artifacts-panel');
  const artifactClose = c.role('button', 'Close', artifactPane);
  const sessionTitle = (s) => s?.title || 'New session';
  const boardTitle = c.title('Board');
  const boardAction = c.field('textbox', 'Task title', 'board-task-title');
  const taskTitle = task && c.title(task.title);
  const settingsTitle = c.title('General');
  const archivedLink = c.field('link', 'Archived', 'global-settings-tab-archived');
  const addMcp = c.role('button', 'Add MCP server');
  const mcpDialog = c.role('dialog', 'Add MCP server');
  const newAutomation = c.role('button', 'New automation');
  const model = c.role('button', fixture.profile[0].modelId ? 'Change model' : 'Choose model').first();
  const sessionComposer = react ? page.locator('#main textarea:visible') : c.get('chat-composer').locator('textarea:visible');
  const verifySession = async () => {
    await ready(c.artifacts);
    await ready(sessionComposer);
    if (exactReply && session) await ready(c.text(exactReply));
  };
  const scenarios = {
    artifacts: {
      start: sessionRoute || bp, title: c.title('Artifacts', artifactPane), action: artifactClose,
      entry: async () => { await verifySession(); await click(c.artifacts); }, route: sessionRoute || bp,
      data: expected(fixture.artifacts.map((row) => row.name), 'No artifacts yet.', artifactPane),
      verify: () => ready(artifactPane),
      secondary: async () => {
        await click(artifactClose);
        await at(page, sessionRoute || bp);
        await artifactPane.waitFor({ state: 'hidden', timeout });
        await verifySession();
        if (session?.title) await ready(c.text(session.title, c.header));
      },
    },
    'new-session': {
      action: c.botAction('new-session', 'New session', 'button'), actionLabel: 'New session', route: bp,
      entry: async () => { if (phone) await click(c.menu); await trial(c.botAction('new-session', 'New session', 'button')); },
    },
    sessions: {
      title: c.title('Sessions'), action: c.field('combobox', 'Kind', 'sessions-kind'), actionLabel: 'Kind',
      entry: botEntry('sessions', 'Sessions'), route: `${bp}/sessions`, deep: `${bp}/sessions`,
      data: expected(sessions.map(sessionTitle), 'No sessions yet'),
      secondary: async () => {
        assert(session, 'Fixture has no active Session to open');
        await click(react ? c.role('link', session.title ? new RegExp(escapeRegExp(session.title)) : /New session/).first() : c.get(`sessions-open-${session.id}`));
        await at(page, sessionRoute);
        await verifySession();
      }, secondaryDeep: () => sessionRoute, secondaryVerify: verifySession,
    },
    automations: {
      title: c.title('Automations'), action: newAutomation, entry: botEntry('automations', 'Automations'),
      route: `${bp}/automations`, deep: `${bp}/automations`,
      data: async () => {
        await expected(fixture.automations.map((row) => row.name), 'No automations yet')();
        for (const row of fixture.automations) {
          const toggle = await ready(c.role('switch', `Enable ${row.name}`));
          assert.equal(await toggle.getAttribute('aria-checked'), String(row.enabled), 'Automation state differs from the API');
        }
      },
      secondary: async () => {
        await click(newAutomation);
        await ready(c.role('dialog', 'New automation'));
      },
      extra: async () => {
        const target = `${bp}/automations/${encodeURIComponent(automation.id)}`;
        const entry = react ? c.role('link', new RegExp(`^${escapeRegExp(automation.name)}(?:\\s|$)`)) : c.get(`automations-open-${automation.id}`);
        await click(entry);
        await at(page, target);
        await ready(c.title(automation.name));
        await goto(page, origin, target);
        await ready(c.title(automation.name));
      },
    },
    'bot-settings': {
      title: c.title('General'), action: model,
      entry: botEntry('settings', 'Bot settings'), route: `${bp}/bot`, deep: `${bp}/bot`,
      data: async () => {
        await ready(c.text('Profile'));
        await ready(c.text('Instructions'));
        const { provider, modelId } = fixture.profile[0];
        if (provider && modelId) await ready(c.text(new RegExp(`^${escapeRegExp(provider)}\\s*/\\s*${escapeRegExp(modelId)}$`)));
      },
      secondary: async () => {
        await click(react ? c.role('link', 'Integrations', page.locator('#main')) : c.get('bot-settings-tab-integrations'));
        await at(page, `${bp}/integrations`);
        await ready(c.title('Integrations'));
      }, secondaryDeep: () => `${bp}/integrations`, secondaryVerify: () => ready(c.title('Integrations')),
    },
    board: {
      title: boardTitle, action: boardAction, actionLabel: 'Task title', global: true, entry: globalEntry('board', 'Board'), route: '/board', deep: '/board',
      data: expected(fixture.tasks.filter((row) => ['ready', 'in_progress', 'in_review', 'backlog'].includes(row.status)).map((row) => row.title), 'All caught up.'),
      secondary: async () => {
        assert(task, 'Fixture has no active Task');
        await click(react ? c.role('link', new RegExp(escapeRegExp(task.key))).first() : c.get(`board-open-${task.key}`));
        await at(page, `/board/${task.key}`);
        await ready(taskTitle);
      }, secondaryDeep: () => task && `/board/${task.key}`, secondaryVerify: () => ready(taskTitle),
      extra: async () => {
        await goto(page, origin, '/board');
        await ready(boardTitle);
        await trial(boardAction);
      },
    },
    integrations: {
      title: c.title('Integrations'), action: addMcp, global: true,
      entry: globalEntry('integrations', 'Integrations'), route: '/integrations', deep: '/integrations',
      data: async () => {
        await expected(fixture.skills.map((row) => row.name), 'No shared skills yet.')();
        await expected(fixture.servers.map((row) => row.name), 'No shared MCP servers yet.')();
      },
      secondary: async () => { await click(addMcp); await ready(mcpDialog); },
      secondaryDeep: () => `/integrations?add=mcp&target=${encodeURIComponent(bot)}`,
      secondaryVerify: () => ready(mcpDialog),
    },
    settings: {
      title: settingsTitle, action: c.field('button', 'Default model', 'global-default-model'), actionLabel: 'Default model',
      global: true, entry: globalEntry('global-settings', 'Settings'),
      route: '/settings/general', deep: '/settings/general',
      data: async () => {
        await ready(c.text('Bot defaults'));
        const { provider, modelId } = fixture.profile[1];
        if (provider && modelId) await ready(c.text(new RegExp(`${escapeRegExp(provider)}\\s*/\\s*${escapeRegExp(modelId)}`)));
      },
      secondary: async () => {
        await click(archivedLink);
        await at(page, '/settings/archived');
        await ready(c.title('Archived bots'));
      }, secondaryDeep: () => '/settings/archived', secondaryVerify: async () => {
        await ready(c.title('Archived bots'));
        await expected(fixture.archived.map((row) => row.name), 'No archived bots')();
      },
    },
  };
  const s = scenarios[name];
  const writesStart = journal.length;
  try {
    await check('actual entry', async () => {
      await goto(page, origin, s.start || bp);
      await s.entry();
      await at(page, s.route);
    });
    if (s.title) await check('title', () => ready(s.title));
    if (s.verify) await check('opens a panel over the conversation', s.verify);
    if (s.action) await check('main control visible', async () => {
      await ready(s.action);
      if (s.actionLabel) {
        const named = c.role('button', s.actionLabel).or(c.role('textbox', s.actionLabel)).or(c.role('combobox', s.actionLabel)).or(c.role('menuitem', s.actionLabel));
        const aria = `[aria-label="${s.actionLabel}"]`;
        await ready(s.action.and(named.or(page.locator(aria))).or(s.action.locator(aria)).first());
      }
    });
    if (s.data) await check('visible API data or expected empty state', s.data);
    if (s.global) await check('no Bot actions on global page', async () => {
      assert(!await c.menu.isVisible() && !await c.artifacts.isVisible() && !await c.botAction('new-session', 'New session', 'button').isVisible());
    });
    await check('layout within viewport', async () => {
      const m = await measure(page, c, s.title, s.action);
      item.measurements.entry = m;
      item.anchors = [...(s.title ? ['heading'] : []), ...(s.action ? ['primary'] : [])];
      const { width, height } = viewports[size];
      assert(m.pane, 'Missing workspace header');
      assert(Math.abs(m.pane.height - 48) <= 1, `Header height ${m.pane.height}px; expected 48px`);
      assert(m.overflow <= 1, `Horizontal document overflow ${m.overflow}px`);
      if (!phone && name !== 'artifacts') assert(m.sidebar && Math.abs(m.sidebar.width - 260) <= 1, 'Desktop sidebar width must be 260px');
      for (const [kind, box] of [['title', m.heading], ['main control', m.primary]]) {
        if (!box) continue;
        assert(box.width > 0 && box.height > 0 && box.x >= -1 && box.x + box.width <= width + 1 && box.y >= -1 && box.y + box.height <= height + 1,
          `${kind} extends outside viewport: ${JSON.stringify(box)}`);
      }
    });
    await capture('entry');
    if (s.secondary) {
      await check(name === 'artifacts' ? 'close returns to the same conversation' : 'secondary action without saving', s.secondary);
      await capture('action');
    }
    if (s.deep) {
      await check('direct page load', async () => {
        await goto(page, origin, s.deep);
        await at(page, s.deep);
        await ready(s.title);
        if (s.data) await s.data();
      });
      await capture('deep');
    }
    if (s.secondaryDeep) {
      await check('direct detail load', async () => {
        const target = s.secondaryDeep();
        assert(target, 'No matching fixture record for the detail URL');
        await goto(page, origin, target);
        await at(page, target);
        await s.secondaryVerify();
      });
      if (s.secondaryDeep()) await capture('detail');
    }
    if (s.extra && (name !== 'automations' || automation)) {
      await check('additional state', s.extra);
      await capture('extra');
    } else if (name === 'automations') {
      item.skipped = ['No saved Automation for a detail deep link'];
      console.log(`SKIP  ${client}/${size}/${name} ${item.skipped[0]}`);
    }
    await check('no attempted write or browser egress', () => {
      // React uses POST for a schedule calculation. Do not forward it in read-only runs.
      const unexpected = journal.slice(writesStart).filter((row) => row.pathname === '<external>' ||
        (!['GET', 'HEAD'].includes(row.method) && !(row.method === 'POST' && row.pathname === '/api/automations/preview' && !row.allowed)));
      assert(!unexpected.length, JSON.stringify(unexpected));
    });
  } finally {
    await page.close();
  }
}

function escapeRegExp(text) { return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }

async function createSession(browser, bot, journal) {
  const { context, origin, createResponse } = await browserContext(browser, writeClient, 'desktop', journal, bot);
  const page = await context.newPage();
  try {
    const bp = botPath(bot);
    const api = apiBot(bot);
    const main = await json(context, origin, `${api}/sessions/main`);
    const profile = await json(context, origin, api);
    assert.equal(profile.provider, 'faux', 'Opt-in creation requires the faux provider');
    assert(!main.runnerId || main.runnerId === 'local', 'Opt-in creation requires the Local Runner');
    assert(!main.cwd, 'Opt-in creation requires an empty Main project');
    await goto(page, origin, bp);
    await click(controls(page, writeClient === 'react', false).botAction('new-session', 'New session', 'button'));
    const { id, status } = await createResponse;
    assert(status >= 200 && status < 300, `New session POST returned ${status ?? 'no response'}`);
    assert(Number.isInteger(id) && id !== main.id, 'Creation must return a non-Main Session ID');
    await at(page, `${bp}/s/${id}`);
    const picker = writeClient === 'react' ? page.getByRole('dialog', { name: 'Project directory' }) : page.getByTestId('new-session-project-dialog');
    await ready(picker);
    await page.screenshot({ path: path.join(out, 'screenshots', `${writeClient}-desktop-project-picker.png`), animations: 'disabled', timeout: 5000 });
    await click(picker.getByRole('button', { name: 'Cancel', exact: true }));
    await picker.waitFor({ state: 'hidden', timeout });
    assert.equal(routeOf(page), `${bp}/s/${id}`);
    const detail = await json(context, origin, `${api}/sessions/${id}`);
    const after = await json(context, origin, `${api}/sessions`);
    assert.equal(detail.id, id);
    assert(detail.channel === 'web' && detail.readOnly === false && detail.main === false, 'Expected a writable Web Session outside Main');
    assert.equal(detail.cwd ?? '', main.cwd ?? '', 'New Session must inherit the displayed project');
    assert(!after.sessions.some((row) => row.id === id), 'An empty Web Session must stay out of the Sessions list until the first message');
    assert.equal((await json(context, origin, `${api}/sessions/main`)).id, main.id, 'Main must not change');
    await goto(page, origin, `${bp}/s/${id}`);
    await ready(controls(page, writeClient === 'react', false).artifacts);
    return { client: writeClient, id, status, listed: false };
  } finally {
    try {
      await page.screenshot({ path: path.join(out, 'screenshots', `${writeClient}-desktop-create-session.png`), animations: 'disabled', timeout: 5000 });
    } finally { await context.close(); }
  }
}

function compare(results) {
  const delta = [];
  for (const item of results.filter((entry) => entry.client === 'react')) {
    const peer = results.find((entry) => entry.client === 'nsgo' && entry.size === item.size && entry.page === item.page);
    const reference = item.measurements.entry;
    const actual = peer?.measurements.entry;
    for (const key of item.anchors || []) {
      for (const axis of ['x', 'y']) {
        const limit = 1;
        const difference = reference?.[key] && actual?.[key] ? actual[key][axis] - reference[key][axis] : null;
        const row = { size: item.size, page: item.page, region: `${key}.${axis}`, difference, limit,
          pass: difference !== null && Math.abs(difference) <= limit };
        delta.push(row);
        const value = difference === null ? 'UNMEASURED (anchor missing)' : `${difference.toFixed(3)}px (limit ${limit}px)`;
        console.log(`${row.pass ? 'GREEN' : 'RED  '} anchor/${row.size}/${row.page} ${row.region}: ${value}`);
      }
    }
  }
  return delta;
}

async function main() {
  assert(Number.isInteger(port) && port > 1025 && port < 65536 && out && !extra.length && (!option || writeClient),
    'Usage: node pages-check.cjs PORT OUT [--create-session=react|nsgo]');
  assert(!writeClient || port === 19545, 'Writes require the dedicated 19544/19545 pair');
  const bot = process.env.PAGES_QA_BOT || 'default';
  await fs.mkdir(path.dirname(out), { recursive: true, mode: 0o700 });
  await fs.mkdir(out, { mode: 0o700 });
  await fs.mkdir(path.join(out, 'screenshots'), { mode: 0o700 });
  const report = { port, viewports, browser: 'Chrome', scale: 1, locale: 'en-US', theme: 'light', bot,
    scriptSHA256: sha256(await fs.readFile(__filename)), builds: {},
    visualScope: 'CSS geometry only; PNGs require visual review. No pixel or native sign-off.', results: [], fixtures: {}, network: {}, setup: [] };
  // The route gate also prevents same-origin API calls that proxy to external services.
  const browser = await chromium.launch({ channel: 'chrome', args: ['--disable-background-networking', '--proxy-server=http://127.0.0.1:1'] });
  report.chromeVersion = browser.version();
  const setup = async (key, fn) => {
    try { await fn(); report.setup.push({ key, pass: true }); console.log(`GREEN ${key}`); }
    catch (error) { report.setup.push({ key, pass: false, reason: error.message }); console.log(`RED   ${key}: ${error.message}`); }
  };
  try {
    if (writeClient) await setup('opt-in New session UI create', async () => {
      report.network.create = [];
      report.create = await createSession(browser, bot, report.network.create);
      assert(!report.network.create.some((row) => !row.allowed && (!['GET', 'HEAD'].includes(row.method) || row.pathname === '<external>')),
        'Creation attempted a request outside the allowed scope');
    });
    else console.log('SKIP  New session submit (read-only); tests use a trial click');
    for (const client of ['react', 'nsgo']) {
      await setup(`local fixture ${client}`, async () => {
        const { context, origin } = await browserContext(browser, client, 'desktop', []);
        try {
          const response = await context.request.get(origin, { timeout, maxRedirects: 0 });
          assert(response.ok());
          const html = await response.text();
          assert(html.includes(reactTitle(client)), `Unexpected ${client} server on ${new URL(origin).port}`);
          report.builds[client] = { indexSHA256: sha256(html), scripts: Array.from(html.matchAll(/<script[^>]+src="([^"]+)"/g), (match) => match[1]) };
          const data = await snapshot(context, origin, bot);
          assert(data.bots.some((row) => row.id === bot), 'Selected Bot is absent');
          report.fixtures[client] = data;
        } finally { await context.close(); }
      });
    }
    await setup('same API fixture on both clients', async () => {
      assert(report.fixtures.react && report.fixtures.nsgo, 'Fixture read missing');
      const differences = Object.keys(report.fixtures.react).filter((key) => JSON.stringify(report.fixtures.react[key]) !== JSON.stringify(report.fixtures.nsgo[key]));
      assert(!differences.length, `Fixture differs: ${differences.join(', ')}`);
    });
    await Promise.all(['react', 'nsgo'].flatMap((client) => Object.keys(viewports).map(async (size) => {
      const fixture = report.fixtures[client];
      if (!fixture) return;
      const journal = report.network[`${client}/${size}`] = [];
      const { context, origin } = await browserContext(browser, client, size, journal);
      try {
        for (const name of ['artifacts', 'new-session', 'sessions', 'automations', 'bot-settings', 'board', 'integrations', 'settings']) {
          await runPage({ client, size, fixture, context, origin, bot, results: report.results, journal }, name);
        }
      } finally { await context.close(); }
    })));
    await setup('server builds stayed unchanged', async () => {
      for (const client of ['react', 'nsgo']) {
        const origin = getOrigin(client);
        const response = await fetch(origin, { redirect: 'error', signal: AbortSignal.timeout(timeout) });
        assert(response.ok && sha256(await response.text()) === report.builds[client]?.indexSHA256, `${client} index changed during the run`);
      }
    });
    report.comparison = compare(report.results);
    const checks = [...report.setup, ...report.results.flatMap((item) => item.checks), ...report.comparison];
    const failed = checks.filter((check) => !check.pass).length;
    console.log(`${failed ? 'RED' : 'GREEN'}   ${checks.length - failed} passed, ${failed} failed; evidence: ${out}`);
    if (failed) process.exitCode = 1;
  } finally {
    await browser.close();
    await fs.writeFile(path.join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
  }
}

function reactTitle(client) { return client === 'react' ? '<title>dex' : '<title>Dex Workspace'; }
main().catch((error) => { console.error(error); process.exitCode = 1; });
