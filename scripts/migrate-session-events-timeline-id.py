#!/usr/bin/env python3
"""Rewrite legacy session-events documents from item_id to timeline_id.

This is an offline migration, not a runtime compatibility path. It removes the
old item_id field after deriving Tank-owned timeline_id values and preserving
raw provider ids as provider_item_id metadata.

Required environment:
  AZURE_TENANT_ID
  AZURE_CLIENT_ID
  AZURE_FEDERATED_TOKEN_FILE
  COSMOS_ENDPOINT
  COSMOS_DATABASE
  COSMOS_SESSION_EVENTS_CONTAINER

Use --dry-run first. Without --partition, the script scans the container
cross-partition for documents that still have item_id or renderable events
missing timeline_id.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


RENDERABLE_TYPES = {
    "user_message.created",
    "item.started",
    "item.delta",
    "item.completed",
    "item.failed",
    "tool.approval_requested",
    "tool.approval_resolved",
}


def stable_id_part(value: str) -> str:
    safe = re.sub(r"[^A-Za-z0-9_.:-]+", "-", value.strip())
    safe = re.sub(r"-+", "-", safe).strip("-")
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:12]
    if 6 <= len(safe) <= 80:
        return safe
    if len(safe) > 80:
        return f"{safe[:64]}-{digest}"
    return digest


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
        body = json.load(resp)
    return body["access_token"]


def required_env(name: str) -> str:
    value = os.environ.get(name)
    if not value:
        raise SystemExit(f"missing required env {name}")
    return value


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

    def query(self, query: str, parameters: list[dict[str, object]], partition: str | None) -> list[dict[str, object]]:
        headers = self.headers(
            {
                "Content-Type": "application/query+json",
                "x-ms-documentdb-isquery": "true",
                "x-ms-max-item-count": "100",
            }
        )
        if partition:
            headers["x-ms-documentdb-partitionkey"] = json.dumps([partition])
        else:
            headers["x-ms-documentdb-query-enablecrosspartition"] = "true"
        payload = json.dumps({"query": query, "parameters": parameters}).encode()
        docs: list[dict[str, object]] = []
        while True:
            req = urllib.request.Request(self.docs_url, data=payload, headers=headers, method="POST")
            with urlopen_with_retry(req, timeout=60) as resp:
                body = json.load(resp)
                docs.extend(body.get("Documents", []))
                continuation = resp.headers.get("x-ms-continuation")
            if not continuation:
                return docs
            headers["x-ms-continuation"] = continuation

    def replace(self, doc: dict[str, object]) -> None:
        doc_id = str(doc.get("id") or "")
        partition = str(doc.get("tank_session_id") or "")
        if not doc_id or not partition:
            raise RuntimeError("document must have id and tank_session_id")
        url = f"{self.docs_url}/{urllib.parse.quote(doc_id, safe='')}"
        headers = self.headers(
            {
                "Content-Type": "application/json",
                "x-ms-documentdb-partitionkey": json.dumps([partition]),
            }
        )
        req = urllib.request.Request(url, data=json.dumps(doc).encode(), headers=headers, method="PUT")
        with urlopen_with_retry(req, timeout=60):
            return


def urlopen_with_retry(req: urllib.request.Request, timeout: int):
    for attempt in range(8):
        try:
            return urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as err:
            if err.code not in {429, 449, 503} or attempt == 7:
                raise
            retry_after_ms = err.headers.get("x-ms-retry-after-ms")
            if retry_after_ms and retry_after_ms.isdigit():
                delay = max(float(retry_after_ms) / 1000.0, 0.1)
            else:
                delay = min(2 ** attempt, 30)
            time.sleep(delay)


def migrate_doc(doc: dict[str, object]) -> tuple[dict[str, object], bool]:
    event_type = str(doc.get("type") or "")
    if event_type not in RENDERABLE_TYPES:
        if "item_id" not in doc:
            return doc, False
        out = dict(doc)
        out.pop("item_id", None)
        return out, True

    turn_id = str(doc.get("turn_id") or "")
    old_item_id = str(doc.get("item_id") or "")
    current_timeline_id = str(doc.get("timeline_id") or "")
    if not turn_id:
        raise RuntimeError(f"renderable event {doc.get('id') or doc.get('event_id')} lacks turn_id")

    out = dict(doc)
    if event_type == "user_message.created":
        out["timeline_id"] = current_timeline_id or f"{turn_id}:user"
    else:
        provider_item_id = str(out.get("provider_item_id") or old_item_id)
        if not provider_item_id:
            raise RuntimeError(f"renderable event {doc.get('id') or doc.get('event_id')} lacks provider item identity")
        out["provider_item_id"] = provider_item_id
        out["timeline_id"] = current_timeline_id or f"{turn_id}:item:{stable_id_part(provider_item_id)}"
    out.pop("item_id", None)
    return out, out != doc


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--partition", action="append", default=[], help="tank_session_id partition to migrate; may repeat")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    cosmos = Cosmos(aad_token())
    partitions = args.partition or [None]
    changed = 0
    scanned = 0
    for partition in partitions:
        query = (
            "SELECT * FROM c WHERE IS_DEFINED(c.item_id) OR "
            "(ARRAY_CONTAINS(@types, c.type) AND IS_DEFINED(c.actor) "
            "AND IS_DEFINED(c.source) AND NOT IS_DEFINED(c.timeline_id))"
        )
        params = [{"name": "@types", "value": sorted(RENDERABLE_TYPES)}]
        for doc in cosmos.query(query, params, partition):
            scanned += 1
            migrated, did_change = migrate_doc(doc)
            if not did_change:
                continue
            changed += 1
            label = migrated.get("id") or migrated.get("event_id")
            print(f"{'would update' if args.dry_run else 'updating'} {label}", file=sys.stderr)
            if not args.dry_run:
                cosmos.replace(migrated)
    print(json.dumps({"scanned": scanned, "changed": changed, "dry_run": args.dry_run}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
