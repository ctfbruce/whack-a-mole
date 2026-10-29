import { chromium } from 'playwright';
import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';

const { invitations } = JSON.parse(await readFile('/inputs/invitations.json', 'utf8'));
const browser = await chromium.launch({ args: ['--no-sandbox', '--no-proxy-server'] });
try {
  const page = await browser.newPage();
  await assert.rejects(page.goto(invitations[0].url, { timeout: 10000 }), /ERR_CERT_AUTHORITY_INVALID/);
  console.log(JSON.stringify({ passed: true, check: 'browser rejects lab TLS before CA is trusted' }));
} finally { await browser.close(); }
