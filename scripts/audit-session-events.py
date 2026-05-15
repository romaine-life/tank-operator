#!/usr/bin/env python3
"""Audit the Cosmos session-events container for docs that fail Tank's
schema validation.

Pre-cutover check required by docs/migration-policy.md before removing the
silent skip-on-read at backend-go/internal/store/session_events.go. Once that
guard is gone, any malformed doc in the container will 500 timeline reads,
so this script must report zero failures before the cutover deploys.

Required environment (same as scripts/migrate-session-events-timeline-id.py):

  AZURE_TENANT_ID
  AZURE_CLIENT_ID
  AZURE_FEDERATED_TOKEN_FILE
  COSMOS_ENDPOINT
  COSMOS_DATABASE
  COSMOS_SESSION_EVENTS_CONTAINER

Outputs: prints per-doc failure reason for the first --max-print failures
plus an aggregate count table. Exits 0 if the container is clean, 1 if any
doc fails the audit. Run with --partition=<storage_key> to audit a single
session.
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
from typing import Any


VALID_ACTORS = {"user", "assistant", "system", "tool", "runner"}
VALID_SOURCES = {"tank", "claude", "codex"}
VALID_VISIBILITIES = {"durable", "live-only"}
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
SKILL_NAME = re.compile(r"^[A-Za-z0-9_-]{1,64}$")


def required_env(name: str) -> str:
    value = os.environ.get(name)
    if not value:
        raise SystemExit(f"missing required env {name}")
    return value


def aad_token() -> str:
    data = urllib.parse.urlencode(
        {
            "client_id": required_env("AZURE_CLIENT_ID"),
            "scope": "https://cosmos.azure.com/.default",
            "client_assertion": open(required_env("AZURE_FEDERATED_TOKEN_FILE"), encoding="utf-8").read().strip(),
            "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
            "grant_type": "client_credentials",
        }
    ).encode()
    req = urllib.request.Request(
        f"https://login.microsoftonline.com/{required_env('AZURE_TENANT_ID')}/oauth2/v2.0/token",
        data=data,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)["access_token"]


def urlopen_with_retry(req: urllib.request.Request, timeout: int):
    for attempt in range(8):
        try:
            return urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as err:
            if err.code not in {429, 449, 503} or attempt == 7:
                raise
            retry_after_ms = err.headers.get("x-ms-retry-after-ms")
            delay = max(float(retry_after_ms) / 1000.0, 0.1) if retry_after_ms and retry_after_ms.isdigit() else min(2 ** attempt, 30)
            time.sleep(delay)


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

    def iter_docs(self, partition: str | None) -> "Any":
        headers = self.headers(
            {
                "Content-Type": "application/query+json",
                "x-ms-documentdb-isquery": "true",
                "x-ms-max-item-count": "200",
            }
        )
        if partition:
            headers["x-ms-documentdb-partitionkey"] = json.dumps([partition])
        else:
            headers["x-ms-documentdb-query-enablecrosspartition"] = "true"
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


def string_field(doc: dict, key: str) -> str:
    value = doc.get(key)
    return value if isinstance(value, str) else ""


def validate_event(doc: dict) -> str | None:
    """Return None if the doc passes Tank's schema validation; otherwise the
    short failure reason (matched against the Go ValidateEventMap errors so
    the audit and the persister fail in the same way)."""
    for field in ("event_id", "session_id", "actor", "source", "type", "created_at", "visibility"):
        if not string_field(doc, field):
            return f"{field}_required"
    if not string_field(doc, "order_key"):
        return "order_key_required"
    if not RFC3339.match(string_field(doc, "created_at")):
        return "created_at_format"
    event_type = string_field(doc, "type")
    if event_type not in VALID_EVENT_TYPES:
        return f"unknown_type:{event_type}"
    if string_field(doc, "actor") not in VALID_ACTORS:
        return f"unknown_actor:{string_field(doc, 'actor')}"
    if string_field(doc, "source") not in VALID_SOURCES:
        return f"unknown_source:{string_field(doc, 'source')}"
    if string_field(doc, "visibility") not in VALID_VISIBILITIES:
        return f"unknown_visibility:{string_field(doc, 'visibility')}"
    payload = doc.get("payload")

    if event_type == "user_message.created":
        for field in ("turn_id", "timeline_id", "client_nonce"):
            if not string_field(doc, field):
                return f"{field}_required:{event_type}"
        if string_field(doc, "actor") != "user" or string_field(doc, "source") != "tank":
            return "user_message.created.actor_source"
        if not isinstance(payload, dict):
            return "payload_required:user_message.created"
        if not string_field(payload, "text"):
            return "payload.text_required"
        display = payload.get("display")
        if not isinstance(display, dict):
            return "payload.display_required"
        kind = string_field(display, "kind")
        if kind == "plain":
            return None
        if kind == "skill_invocation":
            if not SKILL_NAME.match(string_field(display, "skill_name")):
                return "skill_name_invalid"
            if "supplemental_text" in display and not isinstance(display.get("supplemental_text"), str):
                return "supplemental_text_type"
            return None
        return "payload.display.kind"

    if event_type == "turn.submitted":
        if not string_field(doc, "turn_id") or not string_field(doc, "client_nonce"):
            return "turn.submitted.fields"
        if string_field(doc, "actor") != "runner" or string_field(doc, "source") != "tank":
            return "turn.submitted.actor_source"
        if not isinstance(payload, dict) or not string_field(payload, "status"):
            return "payload.status_required"
        return None

    if event_type in {"turn.started", "turn.completed", "turn.failed", "turn.interrupted"}:
        if not string_field(doc, "turn_id"):
            return f"turn_id_required:{event_type}"
        if string_field(doc, "actor") != "runner":
            return f"{event_type}.actor"
        return None

    if event_type == "turn.command_failed":
        if not string_field(doc, "turn_id"):
            return "turn_id_required:turn.command_failed"
        if string_field(doc, "actor") != "system" or string_field(doc, "source") != "tank":
            return "turn.command_failed.actor_source"
        if not isinstance(payload, dict) or not string_field(payload, "reason"):
            return "payload.reason_required"
        return None

    if event_type in {"item.started", "item.delta", "item.completed", "item.failed"}:
        if not string_field(doc, "turn_id") or not string_field(doc, "timeline_id"):
            return f"item_fields_required:{event_type}"
        if not isinstance(payload, dict) or not string_field(payload, "kind"):
            return "payload.kind_required"
        return None

    if event_type in {"tool.approval_requested", "tool.approval_resolved"}:
        if not string_field(doc, "turn_id") or not string_field(doc, "timeline_id"):
            return f"approval_fields_required:{event_type}"
        if string_field(doc, "actor") != "tool":
            return f"{event_type}.actor"
        if not isinstance(payload, dict) or not string_field(payload, "kind"):
            return "payload.kind_required"
        return None

    return f"unhandled_type:{event_type}"


def doc_key(doc: dict) -> str:
    return (
        string_field(doc, "id")
        or string_field(doc, "uuid")
        or string_field(doc, "event_id")
        or "<no-id>"
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--partition", help="audit one session storage key only")
    parser.add_argument("--max-print", type=int, default=50,
                       help="print at most N failure details (default 50)")
    args = parser.parse_args()

    token = aad_token()
    cosmos = Cosmos(token)
    print(f"Auditing session-events (partition={args.partition or '*'})...", file=sys.stderr)

    total = 0
    failures: list[tuple[str, str]] = []
    reasons: Counter[str] = Counter()
    for doc in cosmos.iter_docs(args.partition):
        total += 1
        reason = validate_event(doc)
        if reason is None:
            continue
        failures.append((doc_key(doc), reason))
        reasons[reason] += 1

    print(f"Scanned {total} docs; {len(failures)} failed validation.")
    if not failures:
        return 0

    print("\nReason histogram:")
    for reason, count in reasons.most_common():
        print(f"  {count:>6}  {reason}")

    print(f"\nFirst {min(args.max_print, len(failures))} failures:")
    for doc_id, reason in failures[: args.max_print]:
        print(f"  {doc_id}: {reason}")

    return 1


if __name__ == "__main__":
    sys.exit(main())
