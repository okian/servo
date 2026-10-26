#!/usr/bin/env python3
"""Find links to other pages of the docs site whose text wraps a line.

GitHub Pages builds docs/ with jekyll-relative-links, which turns a link to
another page's .md file into a link to the .html it is published as — but
its pattern for the link text stops at a line break, so a link whose text
wraps onto the next line is published with its .md target unchanged, and
the site serves no such file. GitHub's own file view follows the link
either way, which is why nothing else notices: an offline link check sees
a file that exists.

Usage: wrapped-links.py FILE...   Prints each such link and exits 1 if
there is one. Fenced code blocks are skipped; they hold no links.
"""

import re
import sys

FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
# The text holds a line break and no brackets; the target is relative
# (no scheme, not just a #fragment) and names a .md file.
LINK = re.compile(r"\[([^\[\]]*\n[^\[\]]*)\]\(([^()\s]+)\)")
SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")


def without_fences(text):
    """The text with every fenced code block blanked, line breaks kept."""
    out, fence = [], None
    for line in text.splitlines(keepends=True):
        match = FENCE.match(line)
        if fence is None and match:
            fence = match.group(1)[0]
            out.append("\n")
        elif fence is not None:
            if match and match.group(1)[0] == fence:
                fence = None
            out.append("\n")
        else:
            out.append(line)
    return "".join(out)


def main(paths):
    found = 0
    for path in paths:
        with open(path, encoding="utf-8") as f:
            text = without_fences(f.read())
        for m in LINK.finditer(text):
            target = m.group(2)
            if SCHEME.match(target) or target.startswith("#"):
                continue
            if not re.search(r"\.md(#|$)", target):
                continue
            line = text.count("\n", 0, m.start()) + 1
            label = " ".join(m.group(1).split())
            print(f"{path}:{line}: link text wraps a line, so the site keeps its .md target: [{label}]({target})")
            found += 1
    if found:
        print(f"{found} wrapped link(s): keep each link's text on one line.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
