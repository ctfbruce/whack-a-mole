(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  let pending, expires = 0;
  const notice = (text, error = false) => { $('notice').textContent = text; $('notice').classList.toggle('error', error); };
  $('create').onclick = async () => {
    $('create').disabled = true; notice('Preparing your entrance…');
    const controller = new AbortController(); pending = controller;
    const timeout = setTimeout(() => controller.abort(), 45000);
    try {
      const response = await fetch('/api/enroll', { method: 'POST', credentials: 'omit', cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: '{}', signal: controller.signal });
      if (!response.ok) throw new Error(`An entrance could not be prepared (${response.status}).`);
      const invitation = await response.json(), url = new URL(invitation.url);
      const deadline = Date.parse(invitation.expires_at);
      if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || typeof invitation.passphrase !== 'string' || !invitation.passphrase || !Number.isFinite(deadline)) throw new Error('The server returned an incomplete invitation.');
      if (controller.signal.aborted) return;
      $('address').value = url.href; $('passphrase').value = invitation.passphrase;
      $('open').href = url.href; expires = deadline;
      $('expiry').textContent = `Already running. Expires ${new Date(deadline).toLocaleString()}.`;
      $('welcome').hidden = true; $('invitation').hidden = false;
      notice('Your entrance is ready. Keep the address and passphrase together.');
    } catch (error) {
      const message = error.name === 'AbortError' || error instanceof TypeError ? 'Could not confirm creation. An entrance may already exist; nothing will be retried automatically.' : error.message;
      notice(message, true); $('create').disabled = false;
    } finally { clearTimeout(timeout); pending = null; }
  };
  for (const field of ['address', 'passphrase']) $('copy-' + field).onclick = async () => {
    const input = $(field);
    try { await navigator.clipboard.writeText(input.value); notice(`${field === 'address' ? 'Address' : 'Passphrase'} copied.`); }
    catch { input.focus(); input.select(); notice('Select and copy this value manually.'); }
  };
  function clear() {
    pending?.abort(); expires = 0;
    $('address').value = $('passphrase').value = ''; $('open').removeAttribute('href');
    $('invitation').hidden = true;
  }
  setInterval(() => {
    if (expires && Date.now() >= expires) {
      clear(); $('welcome').hidden = false; $('create').disabled = false;
      notice('This entrance has expired. Use a saved next invitation, or create a new mailbox.');
    }
  }, 1000);
  window.addEventListener('pagehide', clear);
  window.addEventListener('pageshow', event => {
    if (event.persisted) { $('welcome').hidden = false; $('create').disabled = false; notice('This page did not retain the previous invitation. Use a saved invitation, or create a new mailbox.'); }
  });
})();
