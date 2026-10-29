// Isolated browser fault injection: no production data, network, or credentials.
import { chromium } from 'playwright';
import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';

const browser = await chromium.launch({ args: ['--no-sandbox'] });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
  page.setDefaultTimeout(6000);
  await page.clock.install();
  let requests = 0, replies = 0, mode = 'healthy', authenticated = true;
  const state = {
    generated_at: new Date().toISOString(),
    conversations: [
      { id: 'conversation-one', people: [{ ID: 'a', Name: 'Visitor' }, { ID: 'b', Name: 'Core operator' }], messages: [{ id: 1, sender: 'Visitor', text: '<img src=x onerror="window.attacked=true"> Hello from the entrance.', sent_at: new Date().toISOString() }] },
      { id: 'conversation-two', people: [{ ID: 'c', Name: 'Reporter' }, { ID: 'd', Name: 'Core operator' }], messages: [] },
    ],
    relays: [
      { node: 'relay-1', url: 'https://192.0.2.1:443/', status: 'active', conversation: 'conversation-one', participant: 'Visitor', expires_at: new Date(Date.now() + 600000).toISOString() },
      { node: 'relay-2', status: 'free' },
      { node: 'relay-3', status: 'closing', conversation: 'conversation-two', participant: 'Reporter' },
    ],
    events: [{ time: new Date().toISOString(), event: 'entry_created', node: 'relay-1', participant: 'Visitor', detail: '<script>not code</script>' }],
  };
  await page.route('https://operator.invalid/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path.startsWith('/operator/api/')) {
      assert.equal(request.headers().authorization, 'Bearer fixture-operator-key');
      assert.equal(request.headers().cookie, undefined);
      if (!authenticated) return route.fulfill({ status: 401 });
      if (path.endsWith('/state')) {
        requests++;
        if (mode === 'offline' || mode === 'ack-then-offline') return route.fulfill({ status: 503 });
        return route.fulfill({ json: state });
      }
      if (path.endsWith('/reply')) {
        replies++;
        const body = request.postDataJSON();
        assert.equal(body.conversation, 'conversation-one');
        if (mode === 'uncertain') return route.abort('failed');
        const message = { id: 2 + replies, sender: 'Core operator', text: body.text, sent_at: new Date().toISOString() };
        state.conversations[0].messages.push(message);
        return route.fulfill({ status: 201, json: message });
      }
      throw new Error(`Unexpected route ${path}`);
    }
    const asset = path === '/operator/' ? 'operator.html' : path.split('/').at(-1);
    assert(['operator.html', 'operator.js', 'operator.css'].includes(asset));
    return route.fulfill({ contentType: asset.endsWith('.html') ? 'text/html' : asset.endsWith('.js') ? 'text/javascript' : 'text/css', body: await readFile(`/web/${asset}`, 'utf8') });
  });
  async function login() {
    await page.locator('#operator-key').fill('fixture-operator-key');
    await page.locator('#sign-in').click();
    await page.locator('#dashboard').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#operator-key').inputValue(), '');
  }
  await page.goto('https://operator.invalid/operator/');
  assert.equal(requests, 0, 'No private fetch before sign-in');
  await login();
  assert.equal(await page.locator('#messages img').count(), 0);
  assert.match(await page.locator('#messages').innerText(), /<img src=x/);
  assert.equal(await page.evaluate(() => window.attacked), undefined);
  assert.equal(await page.locator('.relay.selected').count(), 1);
  await page.locator('#reply').fill('draft survives polling');
  const before = requests;
  await page.clock.fastForward(5000);
  await page.waitForFunction(() => document.getElementById('connection').classList.contains('live'));
  await page.waitForTimeout(50);
  assert.ok(requests > before);
  assert.equal(await page.locator('#reply').inputValue(), 'draft survives polling');
  await page.locator('.conversation').nth(1).click();
  await page.locator('#reply').fill('second draft');
  await page.locator('.conversation').nth(0).click();
  assert.equal(await page.locator('#reply').inputValue(), 'draft survives polling');
  await page.locator('#send-reply').click();
  await page.getByText('Reply stored by the core.', { exact: true }).waitFor();
  assert.equal(await page.locator('#reply').inputValue(), '');
  assert.match(await page.locator('#messages').innerText(), /draft survives polling/);
  mode = 'ack-then-offline';
  await page.locator('#reply').fill('confirmed once');
  await page.locator('#send-reply').click();
  await page.getByText(/Reply stored by the core. The view could not refresh/).waitFor();
  assert.equal(await page.locator('#reply').inputValue(), '');
  assert.match(await page.locator('#connection').innerText(), /Disconnected/);
  mode = 'uncertain';
  await page.locator('#reply').fill('uncertain draft');
  await page.locator('#send-reply').click();
  await page.getByText(/Reply not confirmed/).waitFor();
  assert.equal(await page.locator('#reply').inputValue(), 'uncertain draft');
  assert.equal(replies, 3, 'No automatic retries');
  mode = 'healthy';
  await page.clock.fastForward(5000);
  await page.locator('details').evaluate(element => { element.open = true; });
  assert.match(await page.locator('#events').innerText(), /<script>not code<\/script>/);
  assert.equal(await page.locator('#events script').count(), 0);
  await page.screenshot({ path: '/evidence/operator-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: '/evidence/operator-mobile.png', fullPage: true });
  await page.locator('#logout').click();
  assert.equal(await page.locator('#dashboard').isVisible(), false);
  assert.equal(await page.locator('#reply').inputValue(), '');
  assert.equal(await page.locator('#messages li').count(), 0);
  assert.equal(await page.locator('#conversations button').count(), 0);
  assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
  const signedOutRequests = requests;
  await page.clock.fastForward(10000);
  assert.equal(requests, signedOutRequests);
  await login();
  await page.locator('#reply').fill('secret draft');
  authenticated = false;
  await page.clock.fastForward(5000);
  await page.locator('#dashboard').waitFor({ state: 'hidden' });
  assert.equal(await page.locator('#reply').inputValue(), '');
  assert.equal(await page.locator('#messages li').count(), 0);
  authenticated = true;
  await login();
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide')));
  assert.equal(await page.locator('#dashboard').isVisible(), false);
  assert.equal(await page.locator('#messages li').count(), 0);
  console.log(JSON.stringify({ passed: true, checks: ['no private fetch before login', 'memory-only key cleared after login', 'message and event markup remain literal', 'polling preserves selected conversation and per-conversation drafts', 'confirmed reply remains confirmed after refresh failure', 'uncertain reply preserves draft without retry', 'responsive dashboard', 'logout stops polling and clears private data', '401 clears credentials and private data', 'pagehide clears private data'] }));
} finally { await browser.close(); }
