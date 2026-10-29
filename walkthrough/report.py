#!/usr/bin/env python3
"""Make a private, self-contained operator/user view from recorded evidence."""
import argparse
import html
import json
import shutil
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlparse

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--fleet', required=True, type=Path)
p.add_argument('--events', required=True, type=Path)
p.add_argument('--browser', required=True, type=Path)
p.add_argument('--traffic', type=Path, help='Optional recorded-traffic comparison HTML')
p.add_argument('--mailboxes', type=Path, help='Optional PRIVATE core state; includes plaintext demo messages')
p.add_argument('--bootstrap', type=Path, help='Optional separate first-visit browser evidence directory')
p.add_argument('--output', required=True, type=Path)
a = p.parse_args()
e = lambda value: html.escape(str(value), quote=True)
fleet = json.loads(a.fleet.read_text())
if isinstance(fleet, dict):
    fleet = fleet['nodes']
events = [json.loads(line) for line in a.events.read_text().splitlines() if line.strip()]
run = json.loads((a.browser / 'walkthrough.json').read_text())
begin = run.get('started_at')
if begin is None:
    begin = datetime.fromisoformat(run['steps'][0]['time'].replace('Z', '+00:00')).timestamp() if run.get('steps') else 0
end = (a.browser / 'walkthrough.json').stat().st_mtime
signed_in = {row.get('address') for row in events if row.get('event') == 'login'
             and begin <= datetime.fromisoformat(row['time'].replace('Z', '+00:00')).timestamp() <= end}
visited = run.get('distinct_addresses', len(signed_in))
handoffs = run.get('handoffs', sum(step['title'].startswith('Handoff ') for step in run.get('steps', [])))
a.output.parent.mkdir(parents=True, exist_ok=True)
assets = a.output.parent / 'assets'
assets.mkdir(exist_ok=True)
counts = Counter(row.get('event') for row in events)
labels = {'entry_starting': 'Starting relay', 'entry_ready': 'Entrance ready', 'login': 'Signed in',
          'message_stored': 'Message stored', 'next_prepared': 'Next entrance prepared',
          'entry_closed': 'Entrance retired', 'cover_response': 'Background response'}
nodes = ''
for index, node in enumerate(fleet):
    node_events = [row for row in events if row.get('node') == node['name']]
    ready = sum(row.get('event') == 'entry_ready' for row in node_events)
    nodes += f'<article class="node"><span class="number">{index + 1:02}</span><strong>{e(node["name"])}</strong><code>{e(urlparse(node["url"]).netloc)}</code><small>{ready} recorded entrance(s)</small></article>'
timeline = ''
for row in events:
    event = row.get('event', '')
    if event not in labels:
        continue
    detail = ''
    if event in ('message_stored', 'cover_response'):
        detail = f'{row.get("bytes", 0)} application bytes'
    elif event == 'entry_closed':
        detail = row.get('detail', row.get('reason', ''))
    elif event == 'next_prepared':
        detail = urlparse(row.get('detail', '')).netloc
    timeline += f'<tr><td>{e(row.get("time", ""))}</td><td>{e(labels[event])}</td><td>{e(row.get("node", ""))}</td><td><code>{e(row.get("hop", ""))}</code></td><td>{e(row.get("participant", ""))}</td><td>{e(detail)}</td></tr>'
buttons, screenshots = '', ''
for index, step in enumerate(run.get('steps', [])):
    source = a.browser / step['image']
    if source.parent.resolve() != a.browser.resolve() or source.suffix != '.png':
        raise ValueError('Unexpected screenshot path')
    shutil.copyfile(source, assets / source.name)
    buttons += f'<button class="step {"selected" if not index else ""}" data-step="{index}">{e(step["title"])}</button>'
    screenshots += f'<figure data-image="{index}" {"hidden" if index else ""}><figcaption><strong>{e(step["title"])}</strong><code>{e(step["address"])}</code></figcaption><img src="assets/{e(source.name)}" alt="{e(step["title"])}"></figure>'
traffic = '<p>No traffic comparison was supplied for this snapshot.</p>'
bootstrap = ''
if a.bootstrap:
    first = json.loads((a.bootstrap / 'bootstrap.json').read_text())
    first_checks = ''.join(f'<li>{e(check)}</li>' for check in first.get('checks', []))
    pictures = ''
    for filename, caption in [('welcome.png', 'Public welcome page'), ('visitor-conversation.png', 'Visitor receives the operator reply')]:
        source = a.bootstrap / filename
        if source.exists():
            target = 'bootstrap-' + filename
            shutil.copyfile(source, assets / target)
            pictures += f'<figure><figcaption>{e(caption)}</figcaption><img src="assets/{target}" alt="{e(caption)}"></figure>'
    bootstrap = '<section><h2>First visit: ' + ('verified' if first.get('passed') else 'incomplete') + '</h2><p>This separate browser check covers enrollment, a real message and reply, and retirement of its two test entrances.</p><ul>' + first_checks + '</ul><div class="walkthrough">' + pictures + '</div></section>'
mailboxes = ''
if a.mailboxes:
    rooms = json.loads(a.mailboxes.read_text())
    for room in rooms.values():
        names = ' ↔ '.join(person['Name'] for person in room['people'])
        messages = ''.join(f'<article class="message"><strong>{e(message["sender"])}</strong><small>{e(message["sent_at"])}</small><p>{e(message["text"])}</p></article>' for message in room['messages'])
        mailboxes += f'<h3>{e(names)}</h3>{messages or "<p>No messages stored.</p>"}'
    mailboxes = '<section><h2>Core mailbox</h2><p>Plaintext stored at the trusted core, included here by explicit request. This private snapshot contains demo conversations; lifecycle logs continue to exclude message contents.</p>' + mailboxes + '</section>'
if a.traffic:
    shutil.copyfile(a.traffic, a.output.parent / 'traffic.html')
    traffic = '<p><a href="traffic.html">Open the recorded traffic comparison →</a></p><iframe src="traffic.html" title="Observed application traffic comparison"></iframe>'
closed = [row for row in events if row.get('event') == 'entry_closed' and row.get('detail') == 'explicit']
retired = run.get('retired_entrances', [])
stamp = lambda value: datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp()
retirement_matches = len(retired) == run.get('handoffs') and all(any(
    event.get('address') == row['address'] and stamp(row.get('closed_after', row['time'])) <= stamp(event['time']) <= stamp(row['time'])
    for event in closed) for row in retired)
status = 'Verified rehearsal' if run.get('passed') and retirement_matches else 'Incomplete rehearsal'
status_class = 'status' if status == 'Verified rehearsal' else 'status incomplete'
failure = '' if run.get('passed') else '<p class="limits">Stopped before the complete tour: ' + e(run.get('notice') or run.get('error', 'Evidence incomplete').splitlines()[0]) + '</p>'
failed_nodes = sorted({row.get('node', 'Unknown node') for row in events if row.get('detail') == 'provision_failure'
                      and begin <= stamp(row['time']) <= end})
if failed_nodes:
    failure += '<p class="limits">Core evidence records a relay provisioning failure on ' + e(', '.join(failed_nodes)) + '. The tour did not skip this node or count an unsuccessful handoff.</p>'
checks = ''.join(f'<li>{e(check)}</li>' for check in run.get('checks', []))
checks += '<li>' + ('Every predecessor also has a matching explicit core retirement event.' if retirement_matches
                    else 'Core retirement evidence is incomplete for this browser run.') + '</li>'
document = f'''<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Ephemeral mailbox · Rehearsal</title><style>
:root{{color-scheme:light;font:16px/1.5 system-ui,sans-serif;color:#182b3b;background:#eef3f6}}
*{{box-sizing:border-box}}body{{margin:0}}main{{max-width:1280px;margin:auto;padding:48px 28px}}h1{{font-size:clamp(32px,4vw,56px);line-height:1.1;letter-spacing:-2px;margin:12px 0}}h2{{font-size:26px;margin:0 0 14px}}p{{max-width:850px;color:#506472}}small,.eyebrow{{color:#607788}}.eyebrow{{font-size:12px;letter-spacing:2px;text-transform:uppercase;font-weight:750}}
.hero{{margin-bottom:32px}}.status{{display:inline-block;background:#d8eee4;color:#166447;padding:6px 12px;border-radius:24px;font-weight:700}}.stats{{display:flex;gap:18px;margin:28px 0}}.stat{{background:white;border-radius:14px;padding:20px 28px;flex:1}}.stat strong{{display:block;font-size:32px;line-height:1.2}}.stat span{{font-size:13px;color:#607788}}
.status.incomplete{{background:#fff0d9;color:#825318}}
section{{background:white;border:1px solid #dce5eb;border-radius:18px;padding:28px;margin:22px 0}}.flow{{display:flex;align-items:center;gap:20px;margin:24px 0}}.flow div{{flex:1;padding:22px;background:#f1f6f9;border-radius:12px;text-align:center}}.flow b{{display:block}}.flow em{{font-style:normal;color:#456575;font-size:25px}}
.nodes{{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:12px}}.node{{border:1px solid #dce5eb;padding:15px;border-radius:12px;min-width:0}}.node strong,.node code,.node small{{display:block;overflow-wrap:anywhere}}.node strong{{font-size:12px;margin:10px 0}}.node code{{font-size:12px}}.node small{{font-size:11px;margin-top:8px}}.number{{background:#e4f0fb;color:#266a9b;border-radius:7px;padding:4px 8px;font-weight:750}}
.walkthrough{{display:grid;grid-template-columns:260px 1fr;gap:24px}}.steps{{display:flex;flex-direction:column;gap:8px}}button{{font:inherit;cursor:pointer}}.step{{border:1px solid #dde6eb;border-radius:9px;text-align:left;padding:11px 13px;background:white;font-size:13px;color:#526775}}.step.selected{{background:#e5f1fb;border-color:#5b96c5;color:#164e7e;font-weight:700}}figure{{margin:0}}figcaption{{display:flex;gap:12px;flex-wrap:wrap;justify-content:space-between;margin-bottom:14px;font-size:13px}}figcaption code{{color:#567286}}figure img{{display:block;width:100%;height:auto;border:1px solid #dce5eb;border-radius:12px}}[hidden]{{display:none!important}}
.scroll{{overflow:auto;max-height:520px}}table{{border-collapse:collapse;width:100%;font-size:12px}}td,th{{padding:10px 12px;border-bottom:1px solid #e7edf1;text-align:left;white-space:nowrap}}th{{background:#f4f7fa;position:sticky;top:0}}code{{font-family:ui-monospace,monospace}}a{{color:#1b689f}}iframe{{border:1px solid #e4ebf0;border-radius:12px;width:100%;height:680px}}.limits{{border-left:4px solid #c58c3e;background:#fffaf1;padding:16px 20px;border-radius:4px;font-size:14px}}footer{{color:#607788;font-size:12px;margin-top:30px}}ul{{padding-left:20px}}
.message{{background:#f3f7fa;border-radius:10px;padding:15px 18px;margin:10px 0}}.message small{{margin-left:15px}}.message p{{white-space:pre-wrap;margin-bottom:0}}
@media(max-width:800px){{main{{padding:24px 14px}}section{{padding:18px}}.nodes{{grid-template-columns:repeat(2,minmax(0,1fr))}}.walkthrough{{display:block}}.steps{{flex-direction:row;overflow:auto;margin-bottom:16px}}.step{{min-width:160px}}.stats{{gap:8px}}.stat{{padding:14px}}.stat strong{{font-size:24px}}.flow{{gap:8px}}.flow div{{padding:12px;font-size:12px}}}}
</style><main><header class="hero"><span class="eyebrow">Ephemeral mailbox · Private evidence</span><h1>One conversation.<br>Different entrances.</h1><p>A real browser follows temporary addresses across an owned fleet. Messages stay at the trusted core while individual Debuglet relays come and go.</p><span class="{status_class}">{e(status)}</span></header>
{failure}<div class="stats"><div class="stat"><strong>{visited}</strong><span>distinct addresses signed into</span></div><div class="stat"><strong>{handoffs}</strong><span>successful handoffs captured</span></div><div class="stat"><strong>{counts['message_stored']}</strong><span>recorded message writes</span></div></div>
<section><h2>The deployment</h2><div class="flow"><div><b>Trusted core</b>Identity, messages, TLS</div><em>↔</em><div><b>Ephemeral relay</b>Opaque bytes in WASM</div><em>↔</em><div><b>Browser</b>Address + passphrase</div></div><div class="nodes">{nodes}</div><p>These are the fleet's recorded public addresses. Separate IPv4 pod addresses stand in for distinct IPv6 entrances; this is not a public IPv6 test. Counts describe this captured rehearsal, not current availability.</p></section>
{bootstrap}
<section><h2>The user's view</h2><p>The same conversation follows the user. Successor credentials are deliberately masked in these screenshots.</p><div class="walkthrough"><nav class="steps">{buttons}</nav><div>{screenshots}</div></div></section>
<section><h2>The core's view</h2><p>Actual lifecycle and storage events. No passphrases, session tokens or message contents appear in this event view.</p><div class="scroll"><table><thead><tr><th>UTC time</th><th>Event</th><th>Node</th><th>Entrance</th><th>Participant</th><th>Detail</th></tr></thead><tbody>{timeline}</tbody></table></div></section>
{mailboxes}
<section><h2>Observed traffic</h2><p>Application body counts from recorded requests and responses. They omit TLS framing, headers, retransmissions and packets; a smoother chart is not evidence of anonymity or resistance to traffic analysis.</p>{traffic}</section>
<section><h2>What this establishes</h2><ul>{checks}</ul><div class="limits">The core is trusted with plaintext. An active entrance exposes mailbox software to a visitor. Distinct addresses and background traffic do not establish censorship resistance, unlinkability, or absence of browser/device/network records. Cover requests use only owned endpoints.</div></section>
<footer>Generated {e(datetime.now(timezone.utc).isoformat(timespec='seconds'))} from saved evidence. Keep this report private.</footer></main>
<script>document.querySelectorAll('[data-step]').forEach(button=>button.onclick=()=>{{document.querySelectorAll('[data-step]').forEach(item=>item.classList.toggle('selected',item===button));document.querySelectorAll('[data-image]').forEach(image=>image.hidden=image.dataset.image!==button.dataset.step)}});</script></html>'''
a.output.write_text(document)
print(f'Private report saved: {a.output}')
