// Isolated browser UI fault injection; no real mailbox or TLS claims.
import { chromium } from 'playwright';
import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';

const browser = await chromium.launch({ args: ['--no-sandbox'] });
try {
  for (const outcome of ['acknowledged', 'lost response', 'refused']) {
    const context = await browser.newContext();
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const expires = new Date(Date.now() + 600000).toISOString();
    let closeRequests = 0;
    await page.route('https://mailbox.invalid/**', async route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/api/login') return route.fulfill({ json: { token: 'test-token', name: 'Reader', expires_at: expires } });
      if (path === '/api/messages') return route.fulfill({ json: { messages: [{ sender: 'Reporter', text: 'Private fixture message', sent_at: new Date().toISOString() }] } });
      if (path === '/api/next') return route.fulfill({ json: { url: 'https://[fd00::2]:8443/', passphrase: 'next-test-passphrase', expires_at: expires } });
      if (path === '/api/close') {
        closeRequests++;
        assert.equal(route.request().headers().authorization, 'Bearer test-token');
        if (outcome === 'lost response') return route.abort('failed');
        return route.fulfill({ status: outcome === 'acknowledged' ? 204 : 503 });
      }
      const asset = path === '/' ? 'index.html' : path.slice(1);
      assert(['index.html', 'app.js', 'style.css'].includes(asset));
      await route.fulfill({ contentType: asset.endsWith('.html') ? 'text/html' : asset.endsWith('.js') ? 'text/javascript' : 'text/css', body: await readFile(`/web/${asset}`, 'utf8') });
    });
    await page.goto('https://mailbox.invalid/');
    await page.locator('#passphrase').fill('test-passphrase');
    await page.getByRole('button', { name: 'Open mailbox' }).click();
    await page.locator('#messages li').waitFor();
    await page.locator('#message').fill('Private draft');
    await page.getByRole('button', { name: 'Prepare next session' }).click();
    await page.locator('#next-panel').waitFor({ state: 'visible' });
    page.on('dialog', dialog => dialog.accept());
    await page.getByRole('button', { name: 'Close this session', exact: true }).click();
    await page.locator('#mailbox').waitFor({ state: 'hidden' });
    assert.equal(closeRequests, 1);
    assert.equal(await page.locator('#message').inputValue(), '');
    assert.equal(await page.locator('#messages li').count(), 0);
    assert.equal(await page.locator('#next-panel').isVisible(), true);
    assert.equal(await page.locator('#next-passphrase').inputValue(), 'next-test-passphrase');
    const notice = await page.locator('#notice').innerText();
    if (outcome === 'acknowledged') assert.match(notice, /^This entrance is closed\./);
    else {
      assert.match(notice, /^Close requested, but confirmation was not received\./);
      assert.match(notice, /may remain available until/);
      assert.doesNotMatch(notice, /entrance is closed/);
    }
    await context.close();
  }
  console.log(JSON.stringify({ passed: true, checks: ['acknowledged close clears current credentials', 'lost response clears page without claiming retirement', 'refusal clears page without claiming retirement', 'prepared successor survives every close outcome'] }));
} finally { await browser.close(); }
