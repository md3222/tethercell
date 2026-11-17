#!/usr/bin/env python3
"""Render docs/TetherCell-reference.md -> HTML -> PDF (chromium headless)."""

import os
import subprocess
import sys

import mistune

HERE = os.path.dirname(os.path.abspath(__file__))
MD = os.path.join(HERE, "TetherCell-reference.md")
HTML = os.path.join(HERE, "TetherCell-reference.html")
PDF = os.path.join(HERE, "TetherCell-reference.pdf")

CSS = """
@page { size: A4; margin: 15mm 14mm 16mm 14mm; }
* { box-sizing: border-box; }
body { font-family: "Noto Sans", "DejaVu Sans", sans-serif; font-size: 9.6pt;
       line-height: 1.5; color: #16212a; margin: 0; }
h1 { font-size: 22pt; margin: 0 0 2mm 0; letter-spacing: -0.3px; color: #0d1a20; }
h1 + p { color: #5b6f7c; font-size: 9pt; border-bottom: 2px solid #2ee6a8;
         padding-bottom: 4mm; margin-bottom: 6mm; }
h2 { font-size: 13pt; margin: 8mm 0 3mm 0; color: #0d1a20;
     border-left: 4px solid #2ee6a8; padding-left: 3mm; }
h3 { font-size: 10.5pt; margin: 5mm 0 2mm 0; color: #1d3b4a; }
p { margin: 2mm 0; }
a { color: #0c7a5e; text-decoration: none; }
code { font-family: "DejaVu Sans Mono", monospace; font-size: 8.6pt;
       background: #eef5f3; border: 1px solid #dbe8e4; border-radius: 3px;
       padding: 0.4mm 1.2mm; }
pre { font-family: "DejaVu Sans Mono", monospace; font-size: 8.4pt;
      background: #0e1a20; color: #d8eee7; padding: 3.5mm 4mm; border-radius: 4px;
      line-height: 1.45; white-space: pre-wrap; }
pre code { background: none; border: none; color: inherit; padding: 0; }
table { border-collapse: collapse; width: 100%; margin: 3mm 0 5mm 0; font-size: 8.6pt; }
tr { page-break-inside: avoid; }
td, th { overflow-wrap: anywhere; }
th { background: #0e1a20; color: #d8eee7; text-align: left; font-weight: 600;
     padding: 2mm 2.5mm; }
td { padding: 1.8mm 2.5mm; border-bottom: 1px solid #dde8e4; vertical-align: top; }
tr:nth-child(even) td { background: #f5faf8; }
th:first-child { white-space: nowrap; }
hr { border: none; border-top: 1px solid #dde8e4; margin: 7mm 0; }
strong { color: #0d1a20; }
li { margin: 1mm 0; }
"""

HTML_DOC = """<!doctype html>
<html><head><meta charset="utf-8"><title>TetherCell reference</title>
<style>{css}</style></head><body>
{body}
</body></html>
"""


def main():
    with open(MD, encoding="utf-8") as f:
        text = f.read()

    try:
        md = mistune.create_markdown(escape=False, plugins=["table", "strikethrough"])
        body = md(text)
    except TypeError:  # mistune 2.x
        body = mistune.html(text)

    with open(HTML, "w", encoding="utf-8") as f:
        f.write(HTML_DOC.format(css=CSS, body=body))

    cmd = ["chromium", "--headless", "--disable-gpu", "--no-sandbox",
           "--no-pdf-header-footer", "--virtual-time-budget=3000",
           f"--print-to-pdf={PDF}", "file://" + HTML]
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0 or not os.path.exists(PDF):
        sys.exit(f"chromium failed ({r.returncode}): {r.stderr[-800:]}")
    print(PDF, os.path.getsize(PDF), "bytes")


if __name__ == "__main__":
    main()
