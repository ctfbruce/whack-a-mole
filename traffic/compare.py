#!/usr/bin/env python3
"""Render recorded application-body traffic; never infer network anonymity."""

import argparse
import html
import json
import math
from pathlib import Path


def read(path):
    if path.stat().st_size > 8 * 1024 * 1024:
        raise ValueError("trace exceeds 8 MiB")
    rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    if len(rows) > 50000:
        raise ValueError("too many trace records")
    for row in rows:
        if (row["direction"] not in ("tx", "rx") or type(row["app_bytes"]) is not int
                or not 0 <= row["app_bytes"] <= 8 * 1024 * 1024
                or not math.isfinite(row["elapsed_s"]) or not 0 <= row["elapsed_s"] <= 7200):
            raise ValueError("invalid trace record")
    return rows


def chart(title, rows, duration, ceiling):
    svg = [f'<h2>{html.escape(title)}</h2><svg viewBox="0 0 900 265" role="img" aria-label="{html.escape(title)}">']
    for tick in range(5):
        y = 210 - tick * 45
        svg.append(f'<path d="M65 {y}H875" stroke="#e2e8f0"/><text x="55" y="{y+4}" text-anchor="end">{ceiling*tick/4/1024:.1f}</text>')
    for direction, color in (("tx", "#2463ac"), ("rx", "#d47b22")):
        bins = {}
        for row in rows:
            if row["direction"] == direction:
                index = int(row["elapsed_s"])
                bins[index] = bins.get(index, 0) + row["app_bytes"]
        for index, size in sorted(bins.items()):
            width = max(.4, min(18, 360 / duration))
            x, height = 65 + index / duration * 810 + (width if direction == "rx" else 0), size / ceiling * 180
            svg.append(f'<rect x="{x:.2f}" y="{210-height:.2f}" width="{width:.2f}" height="{height:.2f}" fill="{color}" opacity=".85"/>')
    for tick in range(5):
        svg.append(f'<text x="{65+tick*202.5}" y="235" text-anchor="middle">{duration*tick/4:.0f}s</text>')
    svg.append('<text x="65" y="15">Application KiB per one-second bin</text></svg>')
    return "".join(svg)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--cover", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    baseline, cover = read(args.baseline), read(args.cover)
    combined = baseline + cover
    duration = max([1] + [math.ceil(row["elapsed_s"] + 1) for row in combined])
    bins = {}
    for row in combined:
        key = row["direction"], int(row["elapsed_s"])
        bins[key] = bins.get(key, 0) + row["app_bytes"]
    ceiling = max([1024] + list(bins.values()))
    content = '<!doctype html><meta charset="utf-8"><title>Mailbox traffic rehearsal</title><style>body{font:17px system-ui;color:#172b40;max-width:1000px;margin:45px auto;padding:0 24px}p{line-height:1.6;color:#4e6376}svg{width:100%;font-size:12px}h2{margin-top:36px}.tx{color:#2463ac}.rx{color:#d47b22}</style><h1>What the generated traffic changes</h1><p><span class="tx">Blue: offered request bodies</span> · <span class="rx">Orange: received response bodies</span>. Both charts share their axes.</p>'
    content += chart("Recorded mailbox traffic", baseline, duration, ceiling)
    content += chart("The same mailbox traffic plus recorded background exchanges", combined, duration, ceiling)
    content += '<p>These are components of the recorded rehearsal, not independent trials. Counts exclude TLS, TCP/IP headers and retransmissions. Failed transfers may undercount partial received bodies. This picture does not establish resemblance to ordinary browsing, anonymity or resistance to traffic analysis.</p>'
    with args.out.open("x") as output:
        output.write(content)
    print(f"Wrote comparison from {len(baseline)} mailbox and {len(cover)} background records.")


if __name__ == "__main__":
    main()
