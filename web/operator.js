(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  let token = '', generation = 0, selected = '', snapshot = null, lastUpdate = 0, offline = false;
  let flight = null, controller = null, sending = false, threadVersion = '', threadConversation = '';
  const drafts = new Map();
  const el = (tag, text, className) => {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  };
  const say = (text = '', error = false) => { $('notice').textContent = text; $('notice').classList.toggle('error', error); };
  const time = value => { const date = new Date(value); return Number.isFinite(date.getTime()) ? date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : 'Unknown time'; };
  const conversationName = conversation => conversation.people.map(person => person.Name).join(' · ') || 'Conversation';
  function clear(message = '') {
    generation++; token = ''; selected = ''; snapshot = null; lastUpdate = 0; threadVersion = ''; threadConversation = ''; drafts.clear();
    controller?.abort(); $('operator-key').value = $('reply').value = '';
    for (const id of ['conversations', 'messages', 'relays', 'events']) $(id).replaceChildren();
    $('thread-title').textContent = $('thread-meta').textContent = '';
    for (const id of ['conversation-count', 'inbox-count', 'active-count', 'free-count']) $(id).textContent = '0';
    $('thread').hidden = $('dashboard').hidden = $('session').hidden = true;
    $('login-panel').hidden = false; $('thread-empty').hidden = false;
    say(message);
  }
  async function request(path, body) {
    const epoch = generation, credential = token, preceding = flight;
    if (preceding) await preceding.catch(() => {});
    if (!credential || epoch !== generation) throw new Error('Sign in again to continue.');
    const abort = new AbortController(); controller = abort;
    const timer = setTimeout(() => abort.abort(), 10000);
    const operation = (async () => {
      const response = await fetch(`/operator/api/${path}`, {
        method: body === undefined ? 'GET' : 'POST', credentials: 'omit', cache: 'no-store',
        headers: { Authorization: `Bearer ${credential}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
        body: body === undefined ? undefined : JSON.stringify(body), signal: abort.signal,
      });
      if (epoch !== generation) throw new Error('This operator session has ended.');
      if (response.status === 401 || response.status === 403) {
        clear('The operator key was refused. Sign in again.');
        throw new Error('The operator key was refused.');
      }
      if (!response.ok) throw new Error(`The core refused this request (${response.status}).`);
      if (body !== undefined) return response;
      const data = await response.json();
      if (epoch !== generation) throw new Error('This operator session has ended.');
      if (!Array.isArray(data.conversations) || !Array.isArray(data.relays) || !Array.isArray(data.events)) throw new Error('The core returned an unreadable snapshot.');
      return data;
    })();
    flight = operation;
    try { return await operation; }
    finally { clearTimeout(timer); if (flight === operation) { flight = null; controller = null; } }
  }
  function connection() {
    if (!token) return;
    const age = Math.max(0, Math.floor((Date.now() - lastUpdate) / 1000));
    const live = lastUpdate && !offline && age < 12;
    $('connection').textContent = live ? 'Live · refreshes every 5s' : lastUpdate ? `Disconnected · snapshot ${age}s old` : 'Connecting';
    $('connection').classList.toggle('live', Boolean(live));
    $('updated').textContent = lastUpdate ? `Last updated ${time(lastUpdate)}` : 'Waiting for a snapshot';
  }
  function renderThread() {
    const conversation = snapshot?.conversations.find(item => item.id === selected);
    $('thread').hidden = !conversation; $('thread-empty').hidden = Boolean(conversation);
    if (!conversation) return;
    $('thread-title').textContent = conversationName(conversation);
    $('thread-meta').textContent = `${conversation.messages.length} messages · ${conversation.id.slice(0, 12)}`;
    const version = JSON.stringify([conversation.id, conversation.messages]);
    if (version === threadVersion) return;
    const list = $('messages'), nearBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 70;
    const changed = threadConversation !== conversation.id;
    list.replaceChildren(...conversation.messages.map(message => {
      const item = el('li', undefined, `message${message.sender === 'Core operator' ? ' mine' : ''}`);
      const meta = el('div', undefined, 'message-meta'), timestamp = el('time', time(message.sent_at));
      timestamp.dateTime = message.sent_at;
      meta.append(el('strong', message.sender), timestamp); item.append(meta, el('p', message.text)); return item;
    }));
    threadVersion = version; threadConversation = conversation.id; $('no-messages').hidden = conversation.messages.length > 0;
    if (nearBottom || changed) list.scrollTop = list.scrollHeight;
  }
  function render() {
    const conversations = snapshot.conversations;
    if (!conversations.some(item => item.id === selected)) { selected = conversations[0]?.id || ''; $('reply').value = drafts.get(selected) || ''; }
    $('conversation-count').textContent = $('inbox-count').textContent = conversations.length;
    $('active-count').textContent = snapshot.relays.filter(relay => relay.status === 'active').length;
    $('free-count').textContent = snapshot.relays.filter(relay => relay.status === 'free').length;
    $('no-conversations').hidden = conversations.length > 0;
    $('conversations').replaceChildren(...conversations.map(conversation => {
      const button = el('button', undefined, 'conversation'), heading = el('span', undefined, 'conversation-heading');
      button.type = 'button'; button.setAttribute('aria-current', String(conversation.id === selected));
      heading.append(el('strong', conversationName(conversation)), el('small', String(conversation.messages.length)));
      button.append(heading, el('p', conversation.messages.at(-1)?.text || 'Waiting for a first message', 'preview'));
      button.onclick = () => { if (selected) drafts.set(selected, $('reply').value); selected = conversation.id; $('reply').value = drafts.get(selected) || ''; render(); };
      return button;
    }));
    renderThread();
    $('no-relays').hidden = snapshot.relays.length > 0;
    $('relays').replaceChildren(...snapshot.relays.map(relay => {
      const status = ['free', 'preparing', 'active', 'closing'].includes(relay.status) ? relay.status : 'unknown';
      const card = el('div', undefined, `relay${selected && relay.conversation === selected ? ' selected' : ''}`), top = el('div', undefined, 'relay-top');
      top.append(el('strong', relay.node), el('span', status, `relay-state ${status}`)); card.append(top);
      card.append(el('p', relay.participant || (status === 'free' ? 'Ready for a new entrance' : 'Preparing entrance')));
      if (relay.url) { let address = relay.url; try { address = new URL(relay.url).host; } catch {} card.append(el('p', address)); }
      if (relay.expires_at) card.append(el('p', `Expires ${time(relay.expires_at)}`));
      return card;
    }));
    $('event-count').textContent = `${snapshot.events.length} recent events`; $('no-events').hidden = snapshot.events.length > 0;
    $('events').replaceChildren(...[...snapshot.events].reverse().map(event => {
      const item = el('li'), timestamp = el('time', time(event.time)); timestamp.dateTime = event.time;
      const details = [event.node, event.participant, event.address, event.detail, event.bytes === undefined ? '' : `${event.bytes} bytes`].filter(Boolean).join(' · ');
      item.append(timestamp, el('strong', String(event.event || '').replaceAll('_', ' ')), el('span', details, 'event-detail')); return item;
    }));
  }
  async function refresh() {
    const epoch = generation;
    try {
      const data = await request('state');
      if (epoch !== generation) return;
      snapshot = data; lastUpdate = Date.now(); offline = false; render(); connection();
    } catch (error) { if (epoch === generation) { offline = true; connection(); } throw error; }
  }
  $('login-form').addEventListener('submit', async event => {
    event.preventDefault(); $('sign-in').disabled = true; say();
    token = $('operator-key').value.trim(); $('operator-key').value = ''; const epoch = generation;
    try {
      await refresh();
      if (epoch !== generation || !token) return;
      $('login-panel').hidden = true; $('dashboard').hidden = $('session').hidden = false; connection();
    } catch { if (epoch === generation) clear('Could not open the core. Check the operator key and connection.'); }
    finally { $('sign-in').disabled = false; }
  });
  $('reply').addEventListener('input', () => { if (selected) drafts.set(selected, $('reply').value); });
  $('reply-form').addEventListener('submit', async event => {
    event.preventDefault(); if (sending || !selected) return;
    const text = $('reply').value, conversation = selected, epoch = generation;
    if (new TextEncoder().encode(text).length > 4096) { say('Keep the reply within 4 KiB; some characters use more than one byte.', true); return; }
    sending = true; $('send-reply').disabled = true; say();
    try {
      const response = await request('reply', { conversation, text });
      if (epoch !== generation) return;
      if (response.status !== 201) throw new Error('No confirmation received.');
      if (drafts.get(conversation) === text) drafts.delete(conversation);
      if (selected === conversation && $('reply').value === text) $('reply').value = '';
      say('Reply stored by the core.');
      try { await refresh(); } catch { if (epoch === generation) say('Reply stored by the core. The view could not refresh; do not resend. It will refresh when the connection returns.', true); }
    } catch { if (epoch === generation) say('Reply not confirmed. Your draft is preserved. Check the conversation before sending again.', true); }
    finally { sending = false; $('send-reply').disabled = false; }
  });
  $('logout').onclick = () => clear('Signed out.');
  window.addEventListener('pagehide', () => clear());
  setInterval(() => { if (token && !flight && !sending) refresh().catch(() => {}); }, 5000);
  setInterval(connection, 1000);
})();
