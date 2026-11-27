// This check writes only to a new Bot in a disposable local preview.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('../../web/node_modules/playwright-core');

async function main() {
  const port = Number(process.argv[2]);
  assert(process.env.DEX_NSGO_POC_TEST === '1' && Number.isInteger(port) && port > 1025 && port < 65536, 'Specify a disposable preview port and set DEX_NSGO_POC_TEST=1');
  const base = `http://127.0.0.1:${port}`;
  async function api(method, route, data) {
    const res = await fetch(base + '/api' + route, { method, redirect: 'error', headers: { 'content-type': 'application/json' }, body: data === undefined ? undefined : JSON.stringify(data) });
    assert(res.ok, `${method} ${route}: ${res.status}`);
    return res.status === 204 ? null : res.json();
  }
  const home = await fs.realpath((await api('GET', '/health')).dataDir);
  assert.match(path.relative(await fs.realpath(os.tmpdir()), home), /^tmp\.[^/]+\/data$/, 'Use the temporary service from poc:nsgo-preview');
  const gate = await fs.mkdtemp(path.join(os.tmpdir(), 'dex-composer-'));
  const browser = await chromium.launch({ channel: 'chrome' });
  try {
    const bot = await api('POST', '/bots', { name: 'Composer check' });
    const route = '/bots/' + encodeURIComponent(bot.id);
    const settings = await api('GET', route);
    await api('PUT', route, { ...settings, provider: 'faux', modelId: 'echo' });
    const page = await browser.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', err => console.error(err.message));
    page.on('console', message => { if (message.type() === 'error' || message.text().includes('panic:')) console.error(message.text().slice(0, 2200)); });
    const get = (id) => page.getByTestId(id);
    const draft = page.locator('textarea:visible');
    await page.goto(base + '/b/' + encodeURIComponent(bot.id));
    await draft.fill('第一行');
    await draft.press('Shift+Enter');
    await draft.press('End');
    await page.keyboard.insertText('第二行');
    const value = '第一行\n第二行';
    assert.equal(await draft.inputValue(), value);
    const posts = [];
    page.on('request', req => { if (req.method() === 'POST' && req.url().endsWith('/messages')) posts.push(req.postDataJSON()); });
    await draft.dispatchEvent('compositionstart', { data: '中' });
    await draft.dispatchEvent('keydown', { key: 'Enter', keyCode: 229, isComposing: true });
    await draft.dispatchEvent('compositionend', { data: '中' });
    assert.equal(await draft.inputValue(), value);
    await draft.press('Enter');
    await get('history').getByText('echo: ' + value, { exact: true }).waitFor();
    assert.deepEqual(posts.map(({ text, mode }) => [text, mode]), [[value, 'followUp']]);
    console.log('PASS: Shift+Enter, IME confirmation and one idle Enter submission');

    await draft.fill('/not-a-dex-command');
    await draft.press('Enter');
    await get('composer-command-hint').waitFor();
    assert.equal(await draft.inputValue(), '/not-a-dex-command');
    assert.equal(posts.length, 1, 'An unknown command must stay local');
    await draft.fill('/cl');
    await draft.press('Tab');
    assert.equal(await draft.inputValue(), '/clear ');
    await draft.fill('');
    const chooser = page.waitForEvent('filechooser');
    await get('composer-files-add').click();
    const uploaded = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/uploads'));
    await (await chooser).setFiles({ name: 'trial.txt', mimeType: 'text/plain', buffer: Buffer.from('fixture') });
    const upload = await uploaded;
    assert(upload.ok(), `Upload failed: ${upload.status()}`);
    const { id } = await upload.json();
    const attached = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/messages'));
    await get('send').click();
    assert((await attached).ok(), 'Dex must accept an attachment without text');
    assert.deepEqual(posts.at(-1).attachments, [id]);
    assert.equal(posts.at(-1).text, '');
    try { await get('composer-files').waitFor({ state: 'hidden' }); }
    catch (cause) { throw new Error('Attachment remains after POST: ' + await get('chat-action-status').textContent(), { cause }); }
    await get('composer-activity').click();
    await get('composer-activity-close').click();
    console.log('PASS: unknown command, completion, attachment-only message and Main activity');

    await api('PUT', route, { ...settings, provider: 'faux', modelId: 'bash' });
    await page.reload();
    // A bounded local command keeps the run open until the test removes the gate.
    const command = `for ((i=0;i<600;i++)); do test -d '${gate}' || break; sleep 0.1; done`;
    await draft.fill(command);
    await get('send').click();
    await get('chat-stop').waitFor();
    await draft.fill('printf queued');
    await get('chat-send-menu').click();
    await get('chat-mode-queue').click();
    await draft.press('Enter');
    await get('chat-stop').waitFor();
    assert.equal(posts.at(-1).mode, 'followUp');
    await draft.fill('printf steering');
    await get('chat-send-menu').click();
    await get('chat-mode-steer').click();
    await get('send').click();
    await get('chat-stop').waitFor();
    assert.equal(posts.at(-1).mode, 'steer');
    await draft.fill('保留草稿');
    await get('chat-send-menu').click();
    const stopped = page.waitForRequest(r => r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/abort'));
    await get('chat-stop').click();
    assert(new URL((await stopped).url()).searchParams.get('runTarget'), 'Stop must target the current Main run');
    assert.equal(await draft.inputValue(), '保留草稿');
    console.log('PASS: queue, steer and targeted Stop with the draft intact');
  } finally {
    await fs.rm(gate, { recursive: true, force: true });
    await browser.close();
    // The preview removes its database on exit. Dex has no admin API to delete a Bot.
  }
}
main().catch(err => { console.error(err); process.exitCode = 1; });
