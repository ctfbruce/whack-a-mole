import { chromium } from 'playwright';
import { readFile, writeFile, appendFile, mkdir, unlink } from 'node:fs/promises';
import assert from 'node:assert/strict';

const inputs = process.env.INPUT_DIR || '/inputs', output = process.env.EVIDENCE_DIR || '/evidence';
const { invitations } = JSON.parse(await readFile(`${inputs}/invitations.json`, 'utf8'));
const started = Number(process.env.STARTED_AT || Date.now() / 1000), hops = Number(process.env.HOPS || 8);
const hold = Number(process.env.COVER_HOLD_SECONDS || 0), sessionFile = process.env.SESSION_FILE;
assert(invitations.length === 2 && Number.isInteger(hops) && hops >= 1 && hops <= 12);
assert(Number.isFinite(hold) && hold >= 0 && hold <= 60 && (!hold || sessionFile));
await mkdir(output, { recursive: true });
await writeFile(`${output}/message-trace.jsonl`, '', { mode: 0o600, flag: 'wx' });
await writeFile(`${output}/walkthrough.json`, '', { mode: 0o600, flag: 'wx' });
const browser = await chromium.launch({ args: ['--no-sandbox', '--no-proxy-server', '--disable-background-networking'] });
const contexts = [], steps = [], retiredEntrances = [], credentials = invitations.map(i => i.passphrase), hosts = new Set();
const pendingResponses = new Set();
let trace = Promise.resolve(), requestID = 0, sessionOwned = false;
let source, current;
const requests = new WeakMap();
function record(row) {
  trace = trace.then(() => appendFile(`${output}/message-trace.jsonl`, JSON.stringify({ profile: 'mailbox', kind: 'message', ...row }) + '\n'));
}
async function page() {
  const context = await browser.newContext({ viewport: { width: 1100, height: 950 } });
  context.setDefaultTimeout(45000); contexts.push(context);
  context.on('request', request => {
    if (!new URL(request.url()).pathname.startsWith('/api/')) return;
    const data = { request_id: ++requestID, at: Date.now(), tx: request.postDataBuffer()?.length || 0 };
    requests.set(request, data);
    record({ elapsed_s: (data.at / 1000) - started, direction: 'tx', app_bytes: data.tx, latency_ms: 0, outcome: 'offered', request_id: data.request_id });
  });
  context.on('response', response => {
    const data = requests.get(response.request());
    if (!data) return;
    const pending = (async () => {
      let size = 0, outcome = `http_${response.status()}`;
      try { size = (await response.body()).length; } catch { outcome = 'body_unavailable'; }
      record({ elapsed_s: Date.now() / 1000 - started,
        direction: 'rx', app_bytes: size, latency_ms: Date.now() - data.at, outcome,
        request_id: data.request_id });
    })();
    pendingResponses.add(pending); pending.finally(() => pendingResponses.delete(pending));
  });
  return context.newPage();
}
async function until(check) {
  const deadline = Date.now() + 12000;
  while (true) {
    try { await check(); return; } catch (error) {
      if (Date.now() >= deadline) throw error;
      await new Promise(resolve => setTimeout(resolve, 150));
    }
  }
}
async function snapshot(view, title, url) {
  const file = `step-${String(steps.length + 1).padStart(2, '0')}.png`;
  await view.screenshot({ path: `${output}/${file}`, fullPage: true, mask: [view.locator('#next-passphrase')] });
  steps.push({ time: new Date().toISOString(), title, address: new URL(url).host, image: file });
}
async function login(view, invitation) {
  const response = await view.goto(invitation.url, { waitUntil: 'networkidle' });
  assert.equal(response.status(), 200);
  assert((await response.securityDetails())?.protocol.includes('TLS'), 'Verified TLS required');
  hosts.add(new URL(invitation.url).hostname);
  await view.locator('#passphrase').fill(invitation.passphrase);
  const responsePromise = view.waitForResponse(response => new URL(response.url()).pathname === '/api/login');
  await view.getByRole('button', { name: 'Open mailbox' }).click();
  const session = await (await responsePromise).json();
  await view.locator('#mailbox').waitFor({ state: 'visible' });
  assert.equal(await view.locator('#passphrase').inputValue(), '');
  return session;
}
async function send(view, text) {
  await view.locator('#message').fill(text);
  await view.getByRole('button', { name: 'Send message' }).click();
  await until(async () => assert.match(await view.locator('#notice').innerText(), /stored by the core/));
}
try {
  source = await page(); current = invitations[0];
  const reporter = await page();
  await source.goto(current.url); await snapshot(source, '1 · Open the supplied address', current.url);
  const firstSession = await login(source, current); await login(reporter, invitations[1]);
  await send(source, 'Demo message: can we meet tomorrow at 10?');
  await reporter.getByRole('button', { name: 'Refresh', exact: true }).click();
  await until(async () => assert.match(await reporter.locator('#messages').innerText(), /tomorrow at 10/));
  await send(reporter, 'Demo reply: confirmed. The conversation will follow your next entrance.');
  await source.getByRole('button', { name: 'Refresh', exact: true }).click();
  await until(async () => assert.match(await source.locator('#messages').innerText(), /Demo reply: confirmed/));
  await snapshot(source, '2 · Exchange messages', current.url);
  if (hold) {
    credentials.push(firstSession.token);
    await writeFile(sessionFile, JSON.stringify({ url: current.url, token: firstSession.token,
      expires_at: firstSession.expires_at, started_at: started }), { mode: 0o600, flag: 'wx' });
    sessionOwned = true;
    const holdStarted = Date.now();
    await new Promise(resolve => setTimeout(resolve, Math.min(8, hold / 2) * 1000));
    await send(source, 'Demo during background traffic: the mailbox remains usable.');
    await reporter.getByRole('button', { name: 'Refresh', exact: true }).click();
    await until(async () => assert.match(await reporter.locator('#messages').innerText(), /Demo during background traffic/));
    await new Promise(resolve => setTimeout(resolve, Math.max(0, hold * 1000 - (Date.now() - holdStarted))));
    await unlink(sessionFile); sessionOwned = false;
  }
  for (let index = 0; index < hops; index++) {
    await source.getByRole('button', { name: 'Prepare next session' }).click();
    await source.locator('#next-panel').waitFor({ state: 'visible' });
    const next = { url: await source.locator('#next-url').inputValue(), passphrase: await source.locator('#next-passphrase').inputValue() };
    credentials.push(next.passphrase);
    const nextHost = new URL(next.url).hostname;
    assert(nextHost !== new URL(current.url).hostname && nextHost !== new URL(invitations[1].url).hostname,
      'Successor must differ from both live entrances; retired free nodes may be reused');
    if (index === 0) await snapshot(source, '3 · Save the next entrance (passphrase hidden here)', current.url);
    source.once('dialog', dialog => dialog.accept());
    const closedAfter = new Date().toISOString();
    await source.getByRole('button', { name: 'Close this session', exact: true }).click();
    await until(async () => assert.equal(await source.locator('#mailbox').isVisible(), false));
    const retired = current.url;
    source = await page(); current = next; await login(source, current);
    await until(async () => assert.match(await source.locator('#messages').innerText(), /Demo reply: confirmed/));
    await send(source, `Demo continuity check ${index + 1}: same conversation, different entrance.`);
    await snapshot(source, `Handoff ${index + 1} · History survives at the next pod`, current.url);
    const probe = await page();
    let failure;
    try { await probe.goto(retired, { timeout: 8000 }); } catch (error) { failure = String(error); }
    assert(failure && /ERR_CONNECTION_|ERR_TIMED_OUT|Timeout/.test(failure), 'Old entrance must refuse or time out, not fail TLS verification');
    retiredEntrances.push({ address: new URL(retired).host, closed_after: closedAfter, time: new Date().toISOString(),
      outcome: /Timeout|TIMED_OUT/.test(failure) ? 'navigation_timeout' : 'connection_failure' });
    await probe.close();
  }
  await reporter.getByRole('button', { name: 'Refresh', exact: true }).click();
  await until(async () => assert.match(await reporter.locator('#messages').innerText(), new RegExp(`continuity check ${hops}:`)));
  await snapshot(reporter, 'Reporter · Messages received across every handoff', invitations[1].url);
  const result = { passed: true, started_at: started, distinct_addresses: hosts.size, handoffs: hops, steps, retired_entrances: retiredEntrances,
    checks: ['Verified browser TLS', 'Two-person exchange', 'Successor differs from both live entrances; free nodes may be reused', 'History retained', 'Old entrance navigation fails at check time; core retirement checked separately'],
    limitation: 'Application body byte counts, not encrypted wire bytes. This does not establish traffic indistinguishability.' };
  await Promise.all(pendingResponses); await trace;
  await writeFile(`${output}/walkthrough.json`, JSON.stringify(result, null, 2));
  console.log(JSON.stringify({ passed: true, distinct_addresses: hosts.size, handoffs: hops }));
} catch (error) {
  let message = String(error.stack || error);
  for (const credential of credentials) if (credential) message = message.replaceAll(credential, '[redacted]');
  let notice = '';
  if (source && !source.isClosed()) {
    notice = await source.locator('#notice').innerText().catch(() => '');
    await snapshot(source, 'Stopped · The next entrance could not be prepared', current.url).catch(() => {});
  }
  for (const credential of credentials) if (credential) notice = notice.replaceAll(credential, '[redacted]');
  await writeFile(`${output}/walkthrough.json`, JSON.stringify({ passed: false, started_at: started,
    error: message, notice, distinct_addresses: hosts.size, steps, retired_entrances: retiredEntrances }, null, 2));
  console.error(message); process.exitCode = 1;
} finally {
  if (sessionOwned) await unlink(sessionFile).catch(() => {});
  await Promise.all(contexts.map(context => context.close())); await browser.close();
  await Promise.all(pendingResponses); await trace;
}
