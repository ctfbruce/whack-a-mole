import { chromium } from 'playwright';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import assert from 'node:assert/strict';

const { invitations } = JSON.parse(await readFile('/inputs/invitations.json', 'utf8'));
assert(invitations.length >= 2, 'Two participant invitations are required');
const output = process.env.EVIDENCE_DIR || '/evidence';
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ args: ['--no-sandbox', '--no-proxy-server', '--disable-background-networking'] });
const checks = [], contexts = [];
const context = async () => {
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 900 } });
  contexts.push(ctx); return ctx;
};
async function eventually(assertion) {
  const until = Date.now() + 5000;
  while (true) {
    try { return await assertion(); } catch (error) {
      if (Date.now() >= until) throw error;
      await new Promise(resolve => setTimeout(resolve, 100));
    }
  }
}
async function login(page, invitation) {
  const response = await page.goto(invitation.url, { waitUntil: 'networkidle' });
  assert.equal(response.status(), 200);
  const tls = await response.securityDetails();
  assert(tls?.protocol.includes('TLS'), 'Browser must negotiate verified TLS');
  await page.locator('#passphrase').fill(invitation.passphrase);
  await page.getByRole('button', { name: 'Open mailbox' }).click();
  await page.locator('#mailbox').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#passphrase').inputValue(), '');
}
try {
  const aliceContext = await context(), bobContext = await context();
  const alice = await aliceContext.newPage(), bob = await bobContext.newPage();
  await alice.goto(invitations[0].url);
  await alice.locator('#passphrase').fill('incorrect');
  await alice.getByRole('button', { name: 'Open mailbox' }).click();
  await eventually(async () => assert.match(await alice.locator('#notice').innerText(), /incorrect|expired/));
  assert.equal(await alice.locator('#mailbox').isVisible(), false);
  checks.push('incorrect passphrase rejected');
  await login(alice, invitations[0]); await login(bob, invitations[1]);
  checks.push('two identities use IPv6 and verified TLS');
  assert(new URL(invitations[0].url).hostname.includes(':'), 'Invitation must use IPv6');
  const text = '<img src=x onerror="window.unexpectedScript=true"> browser exchange';
  await alice.locator('#message').fill(text);
  await alice.getByRole('button', { name: 'Send message' }).click();
  await eventually(async () => assert.match(await alice.locator('#notice').innerText(), /stored by the core/));
  await bob.getByRole('button', { name: 'Refresh', exact: true }).click();
  await eventually(async () => assert((await bob.locator('#messages').innerText()).includes(text)));
  assert.equal(await bob.locator('#messages img').count(), 0);
  assert.equal(await bob.evaluate(() => window.unexpectedScript), undefined);
  checks.push('message exchange and literal HTML display');
  await bob.locator('#message').fill('Reply from the other identity');
  await bob.getByRole('button', { name: 'Send message' }).click();
  await eventually(async () => assert.match(await bob.locator('#notice').innerText(), /stored by the core/));
  await alice.getByRole('button', { name: 'Refresh', exact: true }).click();
  await eventually(async () => assert.match(await alice.locator('#messages').innerText(), /Reply from/));
  await alice.screenshot({ path: `${output}/mailbox-desktop.png`, fullPage: true });
  await alice.setViewportSize({ width: 390, height: 844 });
  assert(await alice.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Mobile page must not overflow');
  await alice.screenshot({ path: `${output}/mailbox-mobile.png`, fullPage: true });
  checks.push('desktop and mobile conversation');
  const before = await alice.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookie: document.cookie, url: location.href }));
  assert.deepEqual(before, { local: 0, session: 0, cookie: '', url: invitations[0].url + (invitations[0].url.endsWith('/') ? '' : '/') });
  assert.equal((await aliceContext.cookies()).length, 0);
  checks.push('no cookies or browser storage used');
  await alice.getByRole('button', { name: 'Prepare next session' }).click();
  await alice.locator('#next-panel').waitFor({ state: 'visible' });
  const next = { url: await alice.locator('#next-url').inputValue(), passphrase: await alice.locator('#next-passphrase').inputValue() };
  assert.notEqual(next.url, invitations[0].url);
  await alice.getByRole('button', { name: 'Show next session again' }).click();
  await eventually(async () => assert.equal(await alice.locator('#prepare').isDisabled(), false));
  assert.equal(await alice.locator('#next-url').inputValue(), next.url);
  assert.equal(await alice.locator('#next-passphrase').inputValue(), next.passphrase);
  checks.push('successor is distinct and preparation is idempotent');
  alice.on('dialog', dialog => dialog.accept());
  await alice.getByRole('button', { name: 'Close this session', exact: true }).click();
  await eventually(async () => assert.equal(await alice.locator('#mailbox').isVisible(), false));
  const nextPage = await (await context()).newPage();
  await login(nextPage, next);
  await eventually(async () => assert.match(await nextPage.locator('#messages').innerText(), /Reply from/));
  let retired = false;
  try { const response = await alice.goto(invitations[0].url, { timeout: 10000, waitUntil: 'commit' }); retired = response.status() >= 400; }
  catch (error) { retired = /net::ERR_(CONNECTION_REFUSED|CONNECTION_CLOSED|CONNECTION_RESET|EMPTY_RESPONSE|ADDRESS_UNREACHABLE)/.test(error.message); }
  assert(retired, 'Closed entrance must not remain available');
  checks.push('closed entrance unavailable; successor retains conversation');
  await nextPage.route('**/api/messages', route => route.request().method() === 'GET' ? route.abort('failed') : route.continue());
  await nextPage.locator('#message').fill('Acknowledged despite a later refresh failure');
  await nextPage.getByRole('button', { name: 'Send message' }).click();
  await eventually(async () => assert.match(await nextPage.locator('#notice').innerText(), /Message stored.*Do not resend/));
  assert.equal(await nextPage.locator('#message').inputValue(), '');
  checks.push('UI fault injection: refresh failure preserves acknowledged send');
  await nextPage.unroute('**/api/messages');
  let getNumber = 0, releaseOld;
  const held = new Promise(resolve => { releaseOld = resolve; });
  await nextPage.route('**/api/messages', async route => {
    if (route.request().method() !== 'GET') return route.continue();
    const number = ++getNumber;
    if (number === 1) await held;
    await route.fulfill({ json: { messages: [{ id: number, sender: 'Fixture', text: number === 1 ? 'stale snapshot' : 'latest snapshot', sent_at: new Date().toISOString() }] } });
  });
  await nextPage.getByRole('button', { name: 'Refresh', exact: true }).click();
  await eventually(() => assert.equal(getNumber, 1));
  await nextPage.locator('#message').fill('Concurrent refresh regression');
  await nextPage.getByRole('button', { name: 'Send message' }).click();
  await eventually(async () => assert.match(await nextPage.locator('#messages').innerText(), /latest snapshot/));
  releaseOld();
  await eventually(async () => assert.equal(await nextPage.locator('#refresh').isDisabled(), false));
  assert.match(await nextPage.locator('#messages').innerText(), /latest snapshot/);
  checks.push('UI fault injection: older refresh cannot replace newer results');
  await nextPage.unroute('**/api/messages');
  await nextPage.route('**/api/messages', route => route.abort('failed'));
  await nextPage.locator('#message').fill('Unconfirmed message');
  await nextPage.getByRole('button', { name: 'Send message' }).click();
  await eventually(async () => assert.match(await nextPage.locator('#notice').innerText(), /Could not confirm/));
  assert.equal(await nextPage.locator('#message').inputValue(), 'Unconfirmed message');
  checks.push('transport failure preserves draft and never claims delivery');
  await nextPage.getByRole('button', { name: 'Clear this page' }).click();
  assert.equal(await nextPage.locator('#message').inputValue(), '');
  assert.equal(await nextPage.locator('#mailbox').isVisible(), false);
  checks.push('explicit page clearing removes conversation and draft');
  await nextPage.evaluate(() => dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  assert.equal(await nextPage.locator('#login-panel').isVisible(), true);
  checks.push('UI lifecycle simulation: restored page offers sign-in');
  await writeFile(`${output}/browser-summary.json`, JSON.stringify({ passed: true, checks }, null, 2));
  console.log(JSON.stringify({ passed: true, checks }));
} finally { await Promise.all(contexts.map(ctx => ctx.close())); await browser.close(); }
