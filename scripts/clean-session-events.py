#!/usr/bin/env python3
"""Cleanup Cosmos session-events docs that fail Tank's schema validation.

Pairs with scripts/audit-session-events.py. The audit reports counts;
this script applies the cleanup so the read-side validation removal in
backend-go/internal/store/session_events.go can ship safely.

Cleanup rules (see docs/migration-policy.md — no compatibility layer):

  - Docs missing `event_id` are raw provider SDK records (type=system,
    type=assistant, type=result, etc.) that leaked into Cosmos through
    the runner's old producer-permissive dispatch. They were never valid
    Tank events; the frontend's isTankConversationEvent filter has been
    silently dropping them on /timeline. We DELETE them.

  - Docs of type=user_message.created with a payload but no
    payload.display are valid Tank events from a transitional schema
    period. We PATCH them with payload.display = {kind: "plain"} so the
    user's submitted text remains durable history.

  - Anything else that fails ValidateEventMap is reported and left in
    place; the operator decides per-case. (Expectation: zero.)

Run with --dry-run first (default) for a preview. Add --apply to commit
the cleanup. Same environment as audit-session-events.py.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter


VALID_EVENT_TYPES = {
    "user_message.created",
    "turn.submitted",
    "turn.started",
    "turn.completed",
    "turn.failed",
    "turn.command_failed",
    "turn.interrupted",
    "item.started",
    "item.delta",
    "item.completed",
    "item.failed",
    "tool.approval_requested",
    "tool.approval_resolved",
}
RFC3339 = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$")


def required_env(name: str) -> str:
    value = os.environ.get(name)
    if not value:
        raise SystemExit(f"missing required env {name}")
    return value


def aad_token() -> str:
    data = urllib.parse.urlencode({
        "client_id": required_env("AZURE_CLIENT_ID"),
        "scope": "https://cosmos.azure.com/.default",
        "client_assertion": open(required_env("AZURE_FEDERATED_TOKEN_FILE"), encoding="utf-8").read().strip(),
        "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
        "grant_type": "client_credentials",
    }).encode()
    req = urllib.request.Request(
        f"https://login.microsoftonline.com/{required_env('AZURE_TENANT_ID')}/oauth2/v2.0/token",
        data=data,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)["access_token"]


def urlopen_with_retry(req: urllib.request.Request, timeout: int):
    last_err: Exception | None = None
    for attempt in range(10):
        try:
            return urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as err:
            last_err = err
            if err.code not in {429, 449, 503} or attempt == 9:
                raise
            retry_after_ms = err.headers.get("x-ms-retry-after-ms")
            delay = max(float(retry_after_ms) / 1000.0, 0.1) if retry_after_ms and retry_after_ms.isdigit() else min(2 ** attempt, 30)
            time.sleep(delay)
        except (urllib.error.URLError, TimeoutError, ConnectionError) as err:
            # Transport-level failure (timed out, TCP reset, etc.).
            # Cosmos throttling sometimes manifests as a closed
            # connection rather than 429, especially on long-running
            # delete loops. Back off and try again.
            last_err = err
            if attempt == 9:
                raise
            time.sleep(min(2 ** attempt, 30))
    if last_err is not None:
        raise last_err


class Cosmos:
    def __init__(self, token: str) -> None:
        endpoint = required_env("COSMOS_ENDPOINT").rstrip("/")
        db = required_env("COSMOS_DATABASE")
        coll = required_env("COSMOS_SESSION_EVENTS_CONTAINER")
        self.docs_url = f"{endpoint}/dbs/{db}/colls/{coll}/docs"
        self.auth = urllib.parse.quote(f"type=aad&ver=1.0&sig={token}", safe="")

    def headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        out = {
            "Authorization": self.auth,
            "x-ms-date": dt.datetime.now(dt.UTC).strftime("%a, %d %b %Y %H:%M:%S GMT"),
            "x-ms-version": "2018-12-31",
        }
        if extra:
            out.update(extra)
        return out

    def iter_docs(self):
        headers = self.headers({
            "Content-Type": "application/query+json",
            "x-ms-documentdb-isquery": "true",
            "x-ms-max-item-count": "200",
            "x-ms-documentdb-query-enablecrosspartition": "true",
        })
        payload = json.dumps({"query": "SELECT * FROM c", "parameters": []}).encode()
        while True:
            req = urllib.request.Request(self.docs_url, data=payload, headers=headers, method="POST")
            with urlopen_with_retry(req, timeout=60) as resp:
                body = json.load(resp)
                for doc in body.get("Documents", []):
                    yield doc
                continuation = resp.headers.get("x-ms-continuation")
            if not continuation:
                return
            headers["x-ms-continuation"] = continuation

    def delete(self, doc_id: str, partition: str) -> None:
        url = f"{self.docs_url}/{urllib.parse.quote(doc_id, safe='')}"
        headers = self.headers({
            "x-ms-documentdb-partitionkey": json.dumps([partition]),
        })
        req = urllib.request.Request(url, headers=headers, method="DELETE")
        with urlopen_with_retry(req, timeout=60):
            return

    def replace(self, doc: dict) -> None:
        doc_id = str(doc.get("id") or "")
        partition = str(doc.get("tank_session_id") or "")
        if not doc_id or not partition:
            raise RuntimeError("document must have id and tank_session_id")
        url = f"{self.docs_url}/{urllib.parse.quote(doc_id, safe='')}"
        headers = self.headers({
            "Content-Type": "application/json",
            "x-ms-documentdb-partitionkey": json.dumps([partition]),
        })
        req = urllib.request.Request(url, data=json.dumps(doc).encode(), headers=headers, method="PUT")
        with urlopen_with_retry(req, timeout=60):
            return


def string_field(doc: dict, key: str) -> str:
    value = doc.get(key)
    return value if isinstance(value, str) else ""


def needs_cleanup(doc: dict) -> tuple[str, dict | None]:
    """Return (action, patched_doc) where action is 'delete', 'patch',
    'keep', or 'unknown'. patched_doc is non-None only for action='patch'.
    """
    event_id = string_field(doc, "event_id")
    if not event_id:
        return ("delete", None)

    if doc.get("type") == "user_message.created":
        payload = doc.get("payload") or {}
        display = payload.get("display") if isinstance(payload, dict) else None
        if not isinstance(display, dict):
            # Patch: synthesize default display so the user's submission
            # stays in the durable record.
            new_doc = dict(doc)
            new_payload = dict(payload) if isinstance(payload, dict) else {}
            new_payload["display"] = {"kind": "plain"}
            new_doc["payload"] = new_payload
            return ("patch", new_doc)

    # Full Tank validation surface (matches audit-session-events.py).
    for field in ("event_id", "session_id", "actor", "source", "type", "created_at", "visibility", "order_key"):
        if not string_field(doc, field):
            return ("unknown", None)
    if not RFC3339.match(string_field(doc, "created_at")):
        return ("unknown", None)
    if string_field(doc, "type") not in VALID_EVENT_TYPES:
        return ("unknown", None)
    return ("keep", None)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true",
                       help="execute the cleanup (default is dry-run with counts only)")
    parser.add_argument("--limit", type=int, default=0,
                       help="stop after acting on N docs (0 = no limit)")
    args = parser.parse_args()

    token = aad_token()
    cosmos = Cosmos(token)

    mode = "APPLY" if args.apply else "DRY-RUN"
    print(f"Cleanup mode: {mode}", file=sys.stderr)

    counts: Counter[str] = Counter()
    unknown_examples: list[tuple[str, str]] = []
    scanned = 0
    acted = 0

    for doc in cosmos.iter_docs():
        scanned += 1
        action, patched = needs_cleanup(doc)
        counts[action] += 1

        if action == "keep":
            continue

        if action == "unknown":
            if len(unknown_examples) < 20:
                doc_id = string_field(doc, "id") or string_field(doc, "event_id") or "<no-id>"
                event_type = string_field(doc, "type") or "<no-type>"
                unknown_examples.append((doc_id, event_type))
            continue

        if args.limit and acted >= args.limit:
            continue

        if action == "delete":
            if args.apply:
                cosmos.delete(string_field(doc, "id"), string_field(doc, "tank_session_id"))
            acted += 1
        elif action == "patch":
            if args.apply and patched is not None:
                cosmos.replace(patched)
            acted += 1

        if acted % 500 == 0 and acted > 0:
            print(f"  ... {acted} acted on (scanned {scanned})", file=sys.stderr)

    print(f"\nScanned {scanned} docs.")
    for action, count in sorted(counts.items()):
        print(f"  {action}: {count}")
    if args.apply:
        print(f"\nApplied: {acted} actions.")
    else:
        print(f"\nWould act on: {acted} docs (re-run with --apply to commit).")

    if unknown_examples:
        print("\nUnknown failures (manual review):")
        for doc_id, event_type in unknown_examples:
            print(f"  {doc_id}: type={event_type}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
