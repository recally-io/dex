import { beforeEach, describe, test } from '@e2e-dev/web';
import { expect } from 'e2e';

describe('chat', () => {
  beforeEach(async ({ app, browser }) => {
    await app.open('/');
    const baseUrl = new URL(await browser.url()).origin;
    const res = await fetch(`${baseUrl}/api/agents/default`);
    const agent = await res.json();
    await fetch(`${baseUrl}/api/agents/default`, {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ ...agent, provider: 'faux', modelId: 'echo' }),
    });
    await app.open('/a/default');
  });

  test('sends a message and displays the assistant reply', async ({ screen }) => {
    const prompt = `Hello from e2e ${Date.now()}`;
    const composer = screen.getByRole('textbox', 'Message Dex');
    await composer.waitFor();

    await composer.fill(prompt);
    await screen.getByRole('button', 'Send message').tap();

    await expect(composer).toHaveValue('');
    await expect(screen.getByText(prompt, { exact: true })).toBeVisible();
    await expect(screen.getByText(`echo: ${prompt}`, { exact: true })).toBeVisible();
  });

  test('opens a new task session from the main timeline', async ({ screen, browser }) => {
    const newTask = screen.getByRole('button', 'New task');
    await newTask.waitFor();

    await newTask.tap();
    const closeDialog = screen.getByRole('button', 'Close');
    if (await closeDialog.isVisible().catch(() => false)) {
      await closeDialog.tap();
    }

    await expect(browser).toHaveURL(/\/a\/default\/c\/\d+/);
    await expect(screen.getByText(/This session has its own context/)).toBeVisible();
  });
});
