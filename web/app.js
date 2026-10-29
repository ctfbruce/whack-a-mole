(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  let token = '', name = '', expires = 0, generation = 0, successor = false;
  const active = new Set();
  const notice = (text = '', error = false) => { $('notice').textContent = text; $('notice').classList.toggle('error', error); };
  function forget(message) {
    token = ''; generation++;
    for (const controller of active) controller.abort();
    $('mailbox').hidden = $('session-actions').hidden = true;
    $('message').value = $('passphrase').value = '';
    $('messages').replaceChildren();
    notice(message);
  }
  async function request(path, body, authenticated = true) {
    const controller = new AbortController(), epoch = generation;
    active.add(controller);
    const timer = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch(path, {
        method: body === undefined ? 'GET' : 'POST', cache: 'no-store', credentials: 'omit',
        headers: { 'Content-Type': 'application/json', ...(authenticated ? { Authorization: `Bearer ${token}` } : {}) },
        body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal,
      });
      if (epoch !== generation) throw new Error('This session has ended.');
      if (!response.ok) {
        if (authenticated && (response.status === 401 || response.status === 403)) forget('This session is no longer available.');
        const explanation = { 401: 'The passphrase is incorrect or this session has expired.', 413: 'The message is too large.', 429: 'Too many requests. Wait briefly before trying again.' };
        throw new Error(explanation[response.status] || `Request refused (${response.status}). Nothing has been confirmed.`);
      }
      const data = response.status === 204 ? null : await response.json();
      if (epoch !== generation) throw new Error('This session has ended.');
      return data;
    } catch (error) {
      if (error instanceof TypeError || error.name === 'AbortError') throw new Error('Could not confirm the request. Check your connection and refresh messages before sending again.');
      throw error;
    } finally { clearTimeout(timer); active.delete(controller); }
  }
  async function busy(button, action) {
    button.disabled = true; notice();
    try { await action(); } catch (error) { notice(error.message, true); }
    finally { button.disabled = false; }
  }
  function tick() {
    if (!token) return;
    const seconds = Math.max(0, Math.ceil((expires - Date.now()) / 1000));
    $('remaining').textContent = `closes in ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
    if (!seconds) forget('This entrance has expired. Use your next address and passphrase if you prepared one.');
  }
  async function refresh() {
    const data = await request('/api/messages');
    const items = data.messages.map(message => {
      const item = document.createElement('li'), sender = document.createElement('strong');
      const time = document.createElement('time'), text = document.createElement('p');
      sender.textContent = message.sender; text.textContent = message.text;
      time.dateTime = message.sent_at; time.textContent = new Date(message.sent_at).toLocaleString();
      item.classList.toggle('mine', message.sender === name); item.append(sender, time, text); return item;
    });
    $('messages').replaceChildren(...items); $('empty').hidden = items.length > 0;
  }
  $('login-form').addEventListener('submit', event => {
    event.preventDefault();
    busy(event.submitter, async () => {
      const data = await request('/api/login', { passphrase: $('passphrase').value }, false);
      token = data.token; name = data.name; expires = Date.parse(data.expires_at);
      $('passphrase').value = ''; $('identity').textContent = name;
      $('login-panel').hidden = true; $('mailbox').hidden = $('session-actions').hidden = $('clear').hidden = false;
      tick(); if (token) await refresh();
    });
  });
  $('refresh').onclick = () => busy($('refresh'), refresh);
  $('message-form').addEventListener('submit', event => {
    event.preventDefault();
    busy(event.submitter, async () => {
      const text = $('message').value;
      if (new TextEncoder().encode(text).length > 4096) throw new Error('Keep the message within 4 KiB; non-English characters may use more than one byte.');
      await request('/api/messages', { text });
      if ($('message').value === text) $('message').value = '';
      notice('Message stored by the core.');
      try { await refresh(); } catch { notice('Message stored by the core, but the conversation could not refresh. Do not resend; try Refresh.', true); }
    });
  });
  $('prepare').onclick = () => busy($('prepare'), async () => {
    const data = await request('/api/next', {});
    $('next-url').value = data.url; $('next-passphrase').value = data.passphrase;
    $('next-expiry').textContent = `Already running. Expires ${new Date(data.expires_at).toLocaleString()}.`;
    $('next-panel').hidden = false; successor = true; $('prepare').textContent = 'Show next session again';
  });
  for (const field of ['url', 'passphrase']) $('copy-' + field).onclick = async () => {
    const input = $('next-' + field);
    try { await navigator.clipboard.writeText(input.value); notice(`Next ${field === 'url' ? 'address' : 'passphrase'} copied.`); }
    catch { input.focus(); input.select(); notice('Select and copy this value manually.'); }
  };
  $('close').onclick = () => busy($('close'), async () => {
    if (!confirm(successor ? 'Close this entrance now? Make sure you have saved your next address and passphrase.' : 'No next entrance is prepared. Closing this one requires a new invitation to return. Continue?')) return;
    await request('/api/close', {});
    forget('This entrance is closed. Your next entrance, if prepared, keeps its original expiry.');
  });
  $('clear').onclick = () => {
    forget('Page cleared. An open entrance still expires at its scheduled time. Clearing this page does not erase browser or network records.');
    $('next-url').value = $('next-passphrase').value = ''; $('next-panel').hidden = $('clear').hidden = true;
  };
  window.addEventListener('pagehide', () => { forget(''); $('next-url').value = $('next-passphrase').value = ''; });
  window.addEventListener('pageshow', event => {
    if (event.persisted) { $('login-panel').hidden = false; notice('Sign in again to reopen this conversation.'); }
  });
  setInterval(tick, 1000);
})();
