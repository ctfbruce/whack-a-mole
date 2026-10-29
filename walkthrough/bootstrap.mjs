import { chromium } from 'playwright';
import { readFile, writeFile, readdir, mkdir } from 'node:fs/promises';
import assert from 'node:assert/strict';

const output = '/evidence', origin = process.env.WELCOME_URL;
assert(origin && new URL(origin).protocol === 'https:');
await mkdir(output, { recursive: true });
await writeFile(`${output}/bootstrap.json`, '', { mode: 0o600, flag: 'wx' });
const browser = await chromium.launch({ args: ['--no-sandbox', '--no-proxy-server', '--disable-background-networking'] });
const contexts = [], credentials = [], views = [], checks = [], retired = [];
async function page() {
  const context = await browser.newContext({ viewport: { width: 1000, height: 950 } });
  context.setDefaultTimeout(80000); contexts.push(context); return context.newPage();
}
async function eventually(check, milliseconds = 10000) {
  const end = Date.now() + milliseconds;
  while (true) {
    try { return await check(); } catch (error) {
      if (Date.now() >= end) throw error;
      await new Promise(resolve => setTimeout(resolve, 150));
    }
  }
}
async function login(invitation) {
  credentials.push(invitation.passphrase);
  const view = await page(), response = await view.goto(invitation.url, { waitUntil: 'networkidle' });
  assert.equal(response.status(), 200); assert((await response.securityDetails())?.protocol.includes('TLS'));
  await view.locator('#passphrase').fill(invitation.passphrase);
  await view.getByRole('button', { name: 'Open mailbox' }).click();
  await view.locator('#mailbox').waitFor({ state: 'visible' }); views.push(view); return view;
}
async function send(view, message) {
  await view.locator('#message').fill(message);
  await view.getByRole('button', { name: 'Send message' }).click();
  await eventually(async () => assert.match(await view.locator('#notice').innerText(), /stored by the core/));
}
try {
  const welcome = await page(), response = await welcome.goto(origin, { waitUntil: 'networkidle' });
  assert.equal(response.status(), 200); assert((await response.securityDetails())?.protocol.includes('TLS'));
  const ca = await welcome.request.get(new URL('/ca.pem', origin).href);
  assert.equal(ca.status(), 200); assert((await ca.body()).equals(await readFile('/inputs/ca.pem')));
  checks.push('Public welcome uses verified TLS and serves the matching demo CA');
  await welcome.screenshot({ path: `${output}/welcome.png`, fullPage: true });
  const enrollment = welcome.waitForResponse(result => new URL(result.url()).pathname === '/api/enroll');
  await welcome.getByRole('button', { name: 'Create my entrance' }).click();
  const enrolled = await enrollment; assert.equal(enrolled.status(), 201);
  const visitor = await enrolled.json(); credentials.push(visitor.passphrase);
  await welcome.locator('#invitation').waitFor({ state: 'visible' });
  assert.equal(await welcome.locator('#passphrase').inputValue(), visitor.passphrase);
  await welcome.screenshot({ path: `${output}/invitation.png`, fullPage: true, mask: [welcome.locator('#passphrase')], maskColor: '#d9e3ea' });
  let record;
  for (const file of await readdir('/core-state/enrollments')) {
    if (!file.endsWith('.json')) continue;
    const entry = JSON.parse(await readFile(`/core-state/enrollments/${file}`, 'utf8'));
    if (entry.invitations[0].participant === visitor.participant) record = entry;
  }
  assert(record && record.invitations.length === 2, 'Private operator invitation must be retained');
  assert.equal(record.invitations[0].url, visitor.url);
  checks.push('Create button issues a real entrance and keeps the operator invitation private');
  const source = await login(visitor), operator = await login(record.invitations[1]);
  const message = 'First-use demo: I arrived through the welcome page and can send a message.';
  await send(source, message);
  const state = JSON.parse(await readFile('/core-state/mailboxes.json', 'utf8'));
  assert(state[record.conversation].messages.some(item => item.text === message));
  await operator.getByRole('button', { name: 'Refresh', exact: true }).click();
  await eventually(async () => assert((await operator.locator('#messages').innerText()).includes(message)));
  await send(operator, 'Core operator received your first-use message. This reply uses the same conversation.');
  await source.getByRole('button', { name: 'Refresh', exact: true }).click();
  await eventually(async () => assert.match(await source.locator('#messages').innerText(), /Core operator received/));
  checks.push('Visitor signs in through relay; core file stores message; operator reads and replies; visitor receives');
  await source.screenshot({ path: `${output}/visitor-conversation.png`, fullPage: true });
  await operator.screenshot({ path: `${output}/operator-conversation.png`, fullPage: true });
  for (const view of views) {
    const address = new URL(view.url()).host, closedAfter = new Date().toISOString();
    view.once('dialog', dialog => dialog.accept());
    await view.getByRole('button', { name: 'Close this session', exact: true }).click();
    await eventually(async () => assert.equal(await view.locator('#mailbox').isVisible(), false), 20000);
    const notice = await view.locator('#notice').innerText();
    const event = await eventually(async () => {
      const events = (await readFile('/inputs/events.jsonl', 'utf8')).trim().split('\n').map(line => JSON.parse(line));
      const found = events.find(item => item.event === 'entry_closed' && item.detail === 'explicit' && item.address === address && item.time >= closedAfter);
      assert(found, 'Core must confirm this entrance was explicitly retired'); return found;
    }, 30000);
    const probe = await page();
    let outcome;
    try { await probe.goto(`https://${address}`, { timeout: 12000 }); }
    catch (error) {
      assert(!String(error).includes('ERR_CERT'), 'A TLS failure cannot establish retirement');
      assert(/ERR_CONNECTION|ERR_EMPTY_RESPONSE|ERR_TIMED_OUT|Timeout/.test(String(error)), String(error));
      outcome = error.name === 'TimeoutError' ? 'navigation_timeout' : 'connection_failure';
    }
    assert(outcome, 'Retired entrance must no longer serve a page');
    retired.push({ address, closed_after: closedAfter, core_closed_at: event.time, outcome, notice });
    await probe.close();
  }
  checks.push('Both test entrances cleared locally, explicitly retired in core events and became unreachable');
  await writeFile(`${output}/bootstrap.json`, JSON.stringify({ passed: true, time: new Date().toISOString(), checks,
    retired_entrances: retired, public_welcome: origin, visitor_address: new URL(visitor.url).host, operator_address: new URL(record.invitations[1].url).host }, null, 2));
  console.log(JSON.stringify({ passed: true, checks }));
} catch (error) {
  let message = String(error.stack || error);
  for (const value of credentials) if (value) message = message.replaceAll(value, '[redacted]');
  await writeFile(`${output}/bootstrap.json`, JSON.stringify({ passed: false, checks, error: message }, null, 2));
  console.error(message); process.exitCode = 1;
} finally { await Promise.all(contexts.map(context => context.close())); await browser.close(); }
