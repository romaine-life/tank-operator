#!/usr/bin/env python3
"""Regenerate the GitHub egress deny CIDRs in the restricted-git egress NetworkPolicy.

Scope (Stage 2b): deny the CIDRs that serve github.com + api.github.com + codeload +
raw/objects.githubusercontent.com, forcing ALL of them through the wall (each is
hostAlias-pinned for restricted sessions and TLS-terminated by the wall). The set is
the union of api.github.com/meta's `git` and `api` ranges, minus the KEEP exclusions
(empty since Stage 2b — see KEEP).

The CIDRs are spliced into k8s/templates/restricted-git-egress-policy.yaml between
`# BEGIN/END github-egress-cidrs-v{4,6}` markers, preserving the surrounding Helm
template. A stale list fails OPEN, so this is run on a schedule
(.github/workflows/refresh-github-egress-cidrs.yml) and opens a PR when GitHub's
published ranges drift.

Usage: python3 scripts/gen-github-egress-cidrs.py [--check]
  --check exits non-zero (and writes nothing) if the file is out of date, for CI.
"""
from __future__ import annotations

import ipaddress
import json
import os
import re
import sys
import urllib.request

TEMPLATE = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "k8s",
    "templates",
    "restricted-git-egress-policy.yaml",
)
META_URL = "https://api.github.com/meta"

# Stage 2b: the wall now fronts codeload + raw/objects.githubusercontent.com
# (agent-egress-proxy.yaml), so githubusercontent's CIDR is DENIED too — a restricted
# session reaches it only via the wall. Empty exclusion set => deny the full git ∪ api
# range. NOTE: the Fastly /22 (185.199.108.0/22) also serves GitHub Pages (*.github.io),
# which the wall does NOT front, so denying it blocks direct Pages fetches from
# restricted sessions (rare for dev work); front Pages in a follow-up if that becomes a
# real need. Kept as an (empty) set so a future host can be re-excluded in one place.
KEEP: set[str] = set()


def fetch_meta() -> dict:
    req = urllib.request.Request(META_URL, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310 (trusted URL)
        return json.load(resp)


def deny_cidrs(meta: dict) -> tuple[list[str], list[str]]:
    deny: set[str] = set()
    for key in ("git", "api"):
        deny |= set(meta.get(key, []))
    deny -= KEEP
    v4 = sorted((c for c in deny if ":" not in c), key=lambda c: ipaddress.ip_network(c))
    v6 = sorted((c for c in deny if ":" in c), key=lambda c: ipaddress.ip_network(c))
    return v4, v6


def splice(text: str, tag: str, items: list[str]) -> str:
    begin = f"# BEGIN {tag}"
    end = f"# END {tag}"
    m = re.search(r"^([ \t]*)" + re.escape(begin), text, re.M)
    if not m:
        sys.exit(f"marker '{begin}' not found in {TEMPLATE}")
    indent = m.group(1)
    block_lines = [f"{indent}{begin} (generated; do not edit by hand)"]
    block_lines += [f"{indent}- {c}" for c in items]
    block_lines.append(f"{indent}{end}")
    block = "\n".join(block_lines)
    pattern = re.escape(indent + begin) + r".*?" + re.escape(indent + end)
    return re.sub(pattern, lambda _: block, text, flags=re.S)


def main() -> int:
    check = "--check" in sys.argv[1:]
    meta = fetch_meta()
    v4, v6 = deny_cidrs(meta)
    if not v4:
        sys.exit("refusing to write an empty IPv4 deny set (meta fetch looks wrong)")
    with open(TEMPLATE, encoding="utf-8") as fh:
        original = fh.read()
    updated = splice(original, "github-egress-cidrs-v4", v4)
    updated = splice(updated, "github-egress-cidrs-v6", v6)
    if check:
        if updated != original:
            print(f"{TEMPLATE} is out of date — run scripts/gen-github-egress-cidrs.py")
            return 1
        print("up to date")
        return 0
    if updated != original:
        with open(TEMPLATE, "w", encoding="utf-8") as fh:
            fh.write(updated)
        print(f"updated {TEMPLATE}: {len(v4)} IPv4 + {len(v6)} IPv6 deny CIDRs")
    else:
        print(f"{TEMPLATE} already current: {len(v4)} IPv4 + {len(v6)} IPv6 deny CIDRs")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
