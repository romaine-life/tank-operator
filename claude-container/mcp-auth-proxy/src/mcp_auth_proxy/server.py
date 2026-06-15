"""Localhost reverse proxy that injects fresh bearer auth.

Sidecar to the claude-container in each session pod. Listens on per-MCP
localhost ports; .mcp.json points claude at these instead of at the
in-cluster MCP Services directly. Most MCPs receive the projected
ServiceAccount token; mcp-github and mcp-tank-operator receive an
auth.romaine.life-issued role=service JWT via the AuthRomaineService
exchange.

The bug this exists to fix: kubelet rotates the SA token file in-place
(eager renewal at ~50 min, well inside the default 1h TTL), but env
vars set from that file at pod start go stale. The previous wiring
exported MCP_*_BEARER in the session startup scripts, then substituted
them into .mcp.json's Authorization headers at harness startup â€” so any
MCP call past the 1h boundary 401'd until the session was recreated.
This proxy reads the file fresh on every request, so token rotation is
invisible to claude.

Same shape as api-proxy (the in-cluster header-injecting proxy for
api.anthropic.com), just localized to the pod because the SA token is
per-pod identity. Hardcoded LISTENERS map mirrors the entries in
k8s/session-config/mcp.json â€” keep them in sync; both sides describe the
same set of MCPs from opposite ends.

OAuth discovery short-circuit: Claude CLI's MCP SDK probes several
well-known paths to discover whether the server speaks OAuth:
`/.well-known/oauth-authorization-server` (RFC 8414),
`/.well-known/oauth-protected-resource` (RFC 9728),
`/.well-known/openid-configuration` (OIDC discovery), and
`POST /register` (RFC 7591 Dynamic Client Registration).
Our MCP servers don't speak OAuth. A plain-text upstream 404 crashes
the SDK's JSON parser
("Unexpected identifier 'Not'") and leaves the connection unrecoverable
across upstream pod rotations. We answer all of those paths locally
with a JSON-shaped 404 so the SDK falls through cleanly to the bearer-
auth POST that this proxy already injects.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import re
import stat
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import quote, urlencode
from uuid import uuid4

from aiohttp import ClientError, ClientSession, ClientTimeout, web

from .metrics import (
    record_auth_romaine_exchange,
    record_proxy_request,
    record_proxy_retry,
    record_sa_token_read,
    start_metrics_server,
    upstream_timer,
)

# Bounded retry budget for transient upstream failures (transport
# errors, 502/503/504). The unrecoverable failure mode this exists to
# prevent: aiohttp.ClientError or a 502 returned to the Claude agent
# SDK lands a plain-text body that crashes the SDK's JSON parser, the
# server gets marked "not connected", and no further calls are
# attempted until the session restarts. Mirrors the OAuth-discovery
# JSON-404 fix that already lives at the top of this file — same
# class of unrecoverable SDK state, different trigger. See
# romaine-life/tank-operator#... (this PR).
#
# Cap kept tight (3 attempts over ~1.3s total) so a genuinely dead
# upstream surfaces fast, while a normal pod-rotation window (typically
# 100-800ms) is invisible to the SDK.
_MAX_UPSTREAM_ATTEMPTS = 3
_RETRY_BACKOFF_SECONDS = (0.1, 0.3)
_TRANSIENT_UPSTREAM_STATUSES = (502, 503, 504)
TANK_OPERATOR_INTERNAL_URL = (
    os.environ.get("TANK_OPERATOR_INTERNAL_URL")
    or "http://tank-operator.tank-operator.svc.cluster.local"
).rstrip("/")
TANK_UI_HOST = (os.environ.get("TANK_UI_HOST") or "https://tank.romaine.life").rstrip("/")
WORKSPACE_ROOT = Path(os.environ.get("WORKSPACE", "/workspace")).resolve()
MCP_GITHUB_INTERNAL_URL = (
    os.environ.get("MCP_GITHUB_URL") or "http://mcp-github.mcp-github.svc:80"
).rstrip("/")

_GITHUB_PR_URL_RE = re.compile(r"https://github\.com/([^/\s]+)/([^/\s]+)/pull/([0-9]+)")
_GITHUB_COMMIT_URL_RE = re.compile(r"https://github\.com/([^/\s]+)/([^/\s]+)/commit/([0-9a-fA-F]{7,40})")
_GITHUB_REMOTE_RE = re.compile(r"github\.com[:/]([^/\s]+)/([^/\s]+?)(?:\.git)?(?:\s|$)")
_TANK_PUBLISH_TOOL = "publish_current_head"
_TANK_BREAK_GLASS_TOOL = "request_git_break_glass"
_TANK_AZURE_BREAK_GLASS_TOOL = "request_azure_break_glass"
_TANK_PR_LANE_TOOL = "request_pr_lane"
_TANK_CREATE_PR_LANE_TOOL = "create_pr_lane"
_TANK_MERGE_TOOL = "merge_current_session_pr"
_TANK_RENAME_PR_TOOL = "rename_current_session_pr"
_TANK_UPDATE_PR_BODY_TOOL = "update_current_session_pr_body"
_GLIMMUNG_HOT_SWAP_TOOL = "apply_test_slot_hot_swap"
_BREAK_GLASS_MCP_SERVER_NAME = "tank-git-break-glass"
_BREAK_GLASS_MCP_PORT = 9999
# azure-personal is absent from the default session .mcp.json (locked by
# default). On an approved azure break-glass grant the orchestrator enqueues an
# approval turn whose mcp_activate payload the pod-side runner uses to add the
# server + rebuild (claude-runner registerBreakGlassMcpFromRecord); the
# always-running 9991 listener below is the upstream that becomes reachable then.
# mcp-azure-personal still re-checks the grant on every call.
_BREAK_GLASS_MINT_TOKEN_TOOL = "mint_full_git_token"
_BREAK_GLASS_PUSH_HEAD_TOOL = "push_current_head"
_AUTH_ROMAINE_BREAK_GLASS_URL = os.environ.get("AUTH_ROMAINE_BREAK_GLASS_URL") or "https://auth.romaine.life/admin"
_GITHUB_WRITE_TOOL_DENYLIST = {
    "mint_clone_token",
    "create_pull_request",
    "commit_to_branch",
    "create_or_update_file",
    "merge_pull_request",
    "update_issue",
}
_CI_ENFORCEMENT_TEXT = (
    "Tank quality gate: watch the PR's current HEAD CI checks until they pass, "
    "confirm there are no merge conflicts, and do not hand work back as complete "
    "unless CI is green and mergeable. If checks fail or conflicts exist, call "
    "that out explicitly."
)


def _retry_delay(attempt_index: int) -> float:
    """Return the sleep duration before retry `attempt_index + 1`.
    attempt_index is 0-indexed; the first retry sleeps
    _RETRY_BACKOFF_SECONDS[0], the second sleeps [1], etc. Anything
    beyond the table length re-uses the last value."""
    if attempt_index < 0:
        return 0.0
    if attempt_index >= len(_RETRY_BACKOFF_SECONDS):
        return _RETRY_BACKOFF_SECONDS[-1]
    return _RETRY_BACKOFF_SECONDS[attempt_index]


def _json_objects_from_mcp_body(raw: bytes) -> list[dict]:
    text = raw.decode("utf-8", errors="replace").strip()
    if not text:
        return []
    if any(line.strip().startswith("data:") for line in text.splitlines()):
        out: list[dict] = []
        for line in text.splitlines():
            line = line.strip()
            if not line.startswith("data:"):
                continue
            payload = line[5:].strip()
            if not payload or payload == "[DONE]":
                continue
            try:
                value = json.loads(payload)
            except json.JSONDecodeError:
                continue
            if isinstance(value, dict):
                out.append(value)
        return out
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        return []
    return [value] if isinstance(value, dict) else []


def _walk_strings(value) -> list[str]:
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        out: list[str] = []
        for item in value:
            out.extend(_walk_strings(item))
        return out
    if isinstance(value, dict):
        out: list[str] = []
        for item in value.values():
            out.extend(_walk_strings(item))
        return out
    return []


def _first_pr_from_response(response_objects: list[dict]) -> tuple[str, str, str, int] | None:
    for text in _walk_strings(response_objects):
        match = _GITHUB_PR_URL_RE.search(text)
        if match:
            owner, name, number = match.groups()
            return owner, name.removesuffix(".git"), match.group(0), int(number)
    return None


def _first_commit_from_response(response_objects: list[dict], arguments: dict) -> tuple[str, str, str, str] | None:
    for text in _walk_strings(response_objects):
        match = _GITHUB_COMMIT_URL_RE.search(text)
        if match:
            owner, name, sha = match.groups()
            return owner, name.removesuffix(".git"), match.group(0), sha
    owner = str(arguments.get("owner") or "").strip()
    name = str(arguments.get("name") or "").strip()
    for key in ("commit_sha", "sha", "oid"):
        sha = str(arguments.get(key) or "").strip()
        if owner and name and re.fullmatch(r"[0-9a-fA-F]{7,40}", sha):
            return owner, name, f"https://github.com/{owner}/{name}/commit/{sha}", sha
    for obj in response_objects:
        structured = obj.get("result", {}).get("structuredContent")
        if isinstance(structured, dict):
            sha = str(structured.get("commit_sha") or structured.get("sha") or "").strip()
            if owner and name and re.fullmatch(r"[0-9a-fA-F]{7,40}", sha):
                return owner, name, f"https://github.com/{owner}/{name}/commit/{sha}", sha
    return None


def _parse_mcp_tool_call(body: bytes) -> tuple[str, dict] | None:
    try:
        payload = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None
    if not isinstance(payload, dict):
        return None
    params = payload.get("params")
    if not isinstance(params, dict) or payload.get("method") != "tools/call":
        return None
    name = params.get("name")
    arguments = params.get("arguments") or {}
    if not isinstance(name, str) or not isinstance(arguments, dict):
        return None
    return name, arguments


def _parse_mcp_method(body: bytes) -> tuple[str, dict, object] | None:
    try:
        payload = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None
    if not isinstance(payload, dict):
        return None
    method = payload.get("method")
    params = payload.get("params") or {}
    if not isinstance(method, str) or not isinstance(params, dict):
        return None
    return method, params, payload.get("id")


def _is_tank_session_branch(branch: str, repo_name: str) -> bool:
    if not ORIGIN_SESSION_ID:
        return False
    base = f"tank/session/{ORIGIN_SESSION_ID}/{repo_name}"
    return branch == base or branch.startswith(base + "/")


def _default_tank_session_branch(repo_name: str) -> str:
    return f"tank/session/{ORIGIN_SESSION_ID}/{repo_name}"


def _mcp_result_response(request_id: object, result: dict) -> web.Response:
    return web.json_response({"jsonrpc": "2.0", "id": request_id, "result": result})


def _mcp_error_response(request_id: object, code: int, message: str, data: dict | None = None, status: int = 200) -> web.Response:
    error: dict[str, object] = {"code": code, "message": message}
    if data:
        error["data"] = data
    return web.json_response({"jsonrpc": "2.0", "id": request_id, "error": error}, status=status)


def _github_tool_block_response(body: bytes, tool_name: str) -> web.Response | None:
    parsed = _parse_mcp_tool_call(body)
    if parsed is None:
        return None
    name, _arguments = parsed
    if name != tool_name:
        return None
    method = _parse_mcp_method(body)
    request_id = method[2] if method else None
    body_tool: str | None = None
    if tool_name == "merge_pull_request":
        replacement_tool = _TANK_MERGE_TOOL
    elif tool_name == "update_issue":
        # update_issue edits both the PR title and body. Point at the
        # governed title rename and, separately, the governed PR-body
        # editor so an agent can fill the Feature Contracts section the
        # check-pr-body workflow reads without break-glass.
        replacement_tool = _TANK_RENAME_PR_TOOL
        body_tool = _TANK_UPDATE_PR_BODY_TOOL
    else:
        replacement_tool = _TANK_PUBLISH_TOOL
    replacement_text = (
        f"Use the Tank MCP {replacement_tool} tool; direct GitHub write "
        "tokens and file/PR writes are reserved for break-glass approval. "
    )
    if body_tool is not None:
        replacement_text += (
            f"To edit the pull request body (for example to fill the Feature "
            f"Contracts section the check-pr-body workflow validates), use the "
            f"Tank MCP {body_tool} tool. "
        )
    data = {
        "blocked_tool": tool_name,
        "replacement_tool": replacement_tool,
        "break_glass_tool": _TANK_BREAK_GLASS_TOOL,
    }
    if body_tool is not None:
        data["body_tool"] = body_tool
    return _mcp_error_response(
        request_id,
        -32010,
        (
            f"GitHub MCP tool '{tool_name}' is disabled in Tank restricted Git mode. "
            f"{replacement_text}"
            "If governed publish is not sufficient, call the Tank MCP "
            "request_git_break_glass tool to get an approval URL."
        ),
        data,
    )


def _filter_github_write_tools(raw: bytes) -> bytes:
    def filter_payload(payload: dict) -> dict:
        result = payload.get("result")
        if not isinstance(result, dict):
            return payload
        tools = result.get("tools")
        if not isinstance(tools, list):
            return payload
        result["tools"] = [
            tool
            for tool in tools
            if not (
                isinstance(tool, dict)
                and isinstance(tool.get("name"), str)
                and tool["name"] in _GITHUB_WRITE_TOOL_DENYLIST
            )
        ]
        return payload

    text = raw.decode("utf-8", errors="replace")
    if any(line.strip().startswith("data:") for line in text.splitlines()):
        next_lines: list[str] = []
        changed = False
        for line in text.splitlines():
            if not line.startswith("data:"):
                next_lines.append(line)
                continue
            data = line[len("data:") :].strip()
            try:
                payload = json.loads(data)
            except json.JSONDecodeError:
                next_lines.append(line)
                continue
            filtered = filter_payload(payload)
            next_lines.append("data: " + json.dumps(filtered, separators=(",", ":")))
            changed = True
        if changed:
            return ("\n".join(next_lines) + "\n").encode("utf-8")
        return raw

    try:
        payload = json.loads(text)
    except json.JSONDecodeError:
        return raw
    return json.dumps(filter_payload(payload), separators=(",", ":")).encode("utf-8")


def _repo_scope_schema(description: str) -> dict:
    return {
        "type": "object",
        "description": description,
        "oneOf": [
            {
                "type": "object",
                "properties": {
                    "kind": {"type": "string", "enum": ["current_repo"]},
                    "repo": {"type": "string"},
                },
                "required": ["kind"],
                "additionalProperties": False,
            },
            {
                "type": "object",
                "properties": {
                    "kind": {"type": "string", "enum": ["repos"]},
                    "repos": {"type": "array", "items": {"type": "string"}, "minItems": 1},
                },
                "required": ["kind", "repos"],
                "additionalProperties": False,
            },
            {
                "type": "object",
                "properties": {
                    "kind": {"type": "string", "enum": ["all_repos"]},
                },
                "required": ["kind"],
                "additionalProperties": False,
            },
        ],
    }


def _branch_scope_schema(description: str) -> dict:
    return {
        "type": "object",
        "description": description,
        "oneOf": [
            {
                "type": "object",
                "properties": {
                    "kind": {"type": "string", "enum": ["named"]},
                    "branches": {"type": "array", "items": {"type": "string"}, "minItems": 1},
                },
                "required": ["kind", "branches"],
                "additionalProperties": False,
            },
            {
                "type": "object",
                "properties": {
                    "kind": {"type": "string", "enum": ["count"]},
                    "count": {"type": "integer", "minimum": 1},
                },
                "required": ["kind", "count"],
                "additionalProperties": False,
            },
            {
                "type": "object",
                "properties": {
                    "kind": {"type": "string", "enum": ["unlimited"]},
                },
                "required": ["kind"],
                "additionalProperties": False,
            },
        ],
    }


def _append_tank_publish_tool(raw: bytes) -> bytes:
    text = raw.decode("utf-8", errors="replace")
    if any(line.strip().startswith("data:") for line in text.splitlines()):
        next_lines: list[str] = []
        changed = False
        for line in text.splitlines():
            stripped = line.strip()
            if not stripped.startswith("data:") or changed:
                next_lines.append(line)
                continue
            payload = stripped[5:].strip()
            try:
                value = json.loads(payload)
            except json.JSONDecodeError:
                next_lines.append(line)
                continue
            if _append_tank_publish_tool_to_json(value):
                next_lines.append("data: " + json.dumps(value, separators=(",", ":")))
                changed = True
            else:
                next_lines.append(line)
        suffix = "\n" if text.endswith("\n") else ""
        return ("\n".join(next_lines) + suffix).encode("utf-8") if changed else raw
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        return raw
    if not _append_tank_publish_tool_to_json(value):
        return raw
    return json.dumps(value, separators=(",", ":")).encode("utf-8")


def _append_tank_publish_tool_to_json(value) -> bool:
    if not isinstance(value, dict):
        return False
    result = value.get("result")
    if not isinstance(result, dict):
        return False
    tools = result.setdefault("tools", [])
    if not isinstance(tools, list):
        return False
    if any(isinstance(tool, dict) and tool.get("name") == _TANK_PUBLISH_TOOL for tool in tools):
        has_publish = True
    else:
        has_publish = False
    has_break_glass = any(isinstance(tool, dict) and tool.get("name") == _TANK_BREAK_GLASS_TOOL for tool in tools)
    has_pr_lane = any(isinstance(tool, dict) and tool.get("name") == _TANK_PR_LANE_TOOL for tool in tools)
    has_create_pr_lane = any(isinstance(tool, dict) and tool.get("name") == _TANK_CREATE_PR_LANE_TOOL for tool in tools)
    has_merge = any(isinstance(tool, dict) and tool.get("name") == _TANK_MERGE_TOOL for tool in tools)
    has_rename_pr = any(isinstance(tool, dict) and tool.get("name") == _TANK_RENAME_PR_TOOL for tool in tools)
    has_update_pr_body = any(isinstance(tool, dict) and tool.get("name") == _TANK_UPDATE_PR_BODY_TOOL for tool in tools)
    changed = False
    for tool in tools:
        if isinstance(tool, dict) and tool.get("name") == _GLIMMUNG_HOT_SWAP_TOOL:
            changed = _augment_glimmung_hot_swap_tool_schema(tool) or changed
    if not has_publish:
        tools.append(
            {
                "name": _TANK_PUBLISH_TOOL,
                "description": (
                    "Publish the current committed HEAD of a Tank-managed workspace "
                    "repo to its session branch, record the commit, and start Tank "
                    "CI/mergeability watching. Direct git push is disabled in normal mode."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo": {
                            "type": "string",
                            "description": "Optional GitHub slug, for example romaine-life/tank-operator.",
                        },
                        "repo_path": {
                            "type": "string",
                            "description": "Optional absolute or /workspace-relative path to the repo.",
                        },
                        "allow_dirty": {
                            "type": "boolean",
                            "description": "Allow publishing HEAD when the worktree has uncommitted changes.",
                        },
                        "source": {
                            "type": "string",
                            "description": "Caller source such as post-commit or agent.",
                        },
                    },
                    "additionalProperties": False,
                },
            }
        )
        changed = True
    if not has_pr_lane:
        tools.append(
            {
                "name": _TANK_PR_LANE_TOOL,
                "description": (
                    "Request an additional Tank-governed PR lane for this session. "
                    "This records a durable approval request; it does not create a "
                    "branch or pull request until Tank policy or a human approves it."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo_scope": _repo_scope_schema(
                            "Choose exactly one repo scope: {\"kind\":\"current_repo\"}, "
                            "{\"kind\":\"repos\",\"repos\":[\"romaine-life/tank-operator\",\"romaine-life/auth\"]}, "
                            "or {\"kind\":\"all_repos\"}."
                        ),
                        "repo_path": {
                            "type": "string",
                            "description": "Optional absolute or /workspace-relative path to the repo.",
                        },
                        "lane_name": {
                            "type": "string",
                            "description": "Short proposed lane name, for example docs or mcp-proxy.",
                        },
                        "branch_scope": _branch_scope_schema(
                            "Required for allocation requests when lane_name is omitted. Choose exactly one: "
                            "{\"kind\":\"named\",\"branches\":[\"docs\",\"backend\"]}, "
                            "{\"kind\":\"count\",\"count\":5}, or {\"kind\":\"unlimited\"}."
                        ),
                        "relationship": {
                            "type": "string",
                            "description": "parallel, stacked, or followup.",
                        },
                        "base": {
                            "type": "string",
                            "description": "Base branch or lane name, for example main or tank/session/123/repo.",
                        },
                        "scope": {
                            "type": "string",
                            "description": "Expected file or contract scope for this PR lane.",
                        },
                        "reason": {
                            "type": "string",
                            "description": "Why this must be a separate review boundary.",
                        },
                    },
                    "required": ["repo_scope", "reason"],
                    "additionalProperties": False,
                },
            }
        )
        changed = True
    if not has_create_pr_lane:
        tools.append(
            {
                "name": _TANK_CREATE_PR_LANE_TOOL,
                "description": (
                    "Create a Tank-governed branch/worktree and draft pull request "
                    "from an approved PR lane request. Requires request_event_id."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "request_event_id": {
                            "type": "string",
                            "description": "Event id returned by request_pr_lane / shown in Tank approvals.",
                        },
                        "repo_path": {
                            "type": "string",
                            "description": "Optional existing repo path used as the source worktree.",
                        },
                    },
                    "required": ["request_event_id"],
                    "additionalProperties": False,
                },
            }
        )
        changed = True
    if not has_merge:
        tools.append(
            {
                "name": _TANK_MERGE_TOOL,
                "description": (
                    "Merge the open pull request for the current Tank-governed "
                    "session branch after verifying local HEAD, remote branch, "
                    "PR head, CI, mergeability, and Tank's governed publish ledger."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo": {
                            "type": "string",
                            "description": "Optional GitHub slug, for example romaine-life/tank-operator.",
                        },
                        "repo_path": {
                            "type": "string",
                            "description": "Optional absolute or /workspace-relative path to the repo.",
                        },
                        "pr_number": {
                            "type": "integer",
                            "description": "Optional PR number; if provided it must match the governed branch's open PR.",
                        },
                        "merge_method": {
                            "type": "string",
                            "enum": ["merge", "squash", "rebase"],
                            "description": "Optional GitHub merge method.",
                        },
                        "commit_title": {
                            "type": "string",
                            "description": "Optional merge commit title.",
                        },
                        "commit_message": {
                            "type": "string",
                            "description": "Optional merge commit message.",
                        },
                        "mark_ready": {
                            "type": "boolean",
                            "description": "Convert a draft PR to ready for review before merging. Defaults to true.",
                        },
                    },
                    "additionalProperties": False,
                },
            }
        )
        changed = True
    if not has_rename_pr:
        tools.append(
            {
                "name": _TANK_RENAME_PR_TOOL,
                "description": (
                    "Rename the open pull request for the current Tank-governed "
                    "session branch after verifying the current repo, branch, "
                    "and PR head belong to this session lane."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo": {
                            "type": "string",
                            "description": "Optional GitHub slug, for example romaine-life/tank-operator.",
                        },
                        "repo_path": {
                            "type": "string",
                            "description": "Optional absolute or /workspace-relative path to the repo.",
                        },
                        "pr_number": {
                            "type": "integer",
                            "description": "Optional PR number; if provided it must match the current governed branch's open PR.",
                        },
                        "title": {
                            "type": "string",
                            "description": "New pull request title.",
                        },
                    },
                    "required": ["title"],
                    "additionalProperties": False,
                },
            }
        )
        changed = True
    if not has_update_pr_body:
        tools.append(
            {
                "name": _TANK_UPDATE_PR_BODY_TOOL,
                "description": (
                    "Replace the body/description of the open pull request for the "
                    "current Tank-governed session branch after verifying the repo, "
                    "branch, and PR head belong to this session lane. Use this to "
                    "fill the Feature Contracts section the check-pr-body workflow "
                    "validates; a PR comment does not satisfy that check. Direct "
                    "GitHub PR-body edits are disabled in restricted Git mode."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo": {
                            "type": "string",
                            "description": "Optional GitHub slug, for example romaine-life/tank-operator.",
                        },
                        "repo_path": {
                            "type": "string",
                            "description": "Optional absolute or /workspace-relative path to the repo.",
                        },
                        "pr_number": {
                            "type": "integer",
                            "description": "Optional PR number; if provided it must match the current governed branch's open PR.",
                        },
                        "body": {
                            "type": "string",
                            "description": "New pull request body in Markdown. Replaces the existing body in full.",
                        },
                    },
                    "required": ["body"],
                    "additionalProperties": False,
                },
            }
        )
        changed = True
    if has_break_glass:
        return changed
    tools.append(
        {
            "name": _TANK_BREAK_GLASS_TOOL,
            "description": (
                "Record a request for break-glass GitHub write access and return "
                "a human approval URL. This normal-mode tool does not mint or "
                "reveal a token, and it does not expose privileged GitHub options."
            ),
            "inputSchema": {
                "type": "object",
                "properties": {
                    "repo_scope": _repo_scope_schema(
                        "Choose exactly one repo scope. Use {\"kind\":\"current_repo\"} for the checked-out repo, "
                        "{\"kind\":\"repos\",\"repos\":[...]} for a specific repo list, or {\"kind\":\"all_repos\"} "
                        "only when every repo is explicitly needed."
                    ),
                    "repo_path": {
                        "type": "string",
                        "description": "Optional absolute or /workspace-relative path used to resolve current_repo.",
                    },
                    "branch_scope": _branch_scope_schema(
                        "Choose exactly one branch scope. Examples: {\"kind\":\"named\",\"branches\":[\"branch-a\",\"branch-b\"]}, "
                        "{\"kind\":\"count\",\"count\":5}, or {\"kind\":\"unlimited\"}."
                    ),
                    "reason": {
                        "type": "string",
                        "description": "Short reason the governed publish path is insufficient.",
                    },
                    "source": {
                        "type": "string",
                        "description": "Caller source such as agent.",
                    },
                },
                "required": ["repo_scope", "branch_scope", "reason"],
                "additionalProperties": False,
            },
        }
    )
    return True


def _append_azure_break_glass_tool(raw: bytes) -> bytes:
    # Inject request_azure_break_glass into the mcp-tank-operator tools/list,
    # the same way _append_tank_publish_tool injects the git tools. Unlike the
    # git tools this is NOT gated on restricted-git mode: azure-personal is
    # locked by default for every session, so its break-glass request tool is
    # always present on the Tank surface.
    text = raw.decode("utf-8", errors="replace")
    if any(line.strip().startswith("data:") for line in text.splitlines()):
        next_lines: list[str] = []
        changed = False
        for line in text.splitlines():
            stripped = line.strip()
            if not stripped.startswith("data:") or changed:
                next_lines.append(line)
                continue
            payload = stripped[5:].strip()
            try:
                value = json.loads(payload)
            except json.JSONDecodeError:
                next_lines.append(line)
                continue
            if _append_azure_break_glass_tool_to_json(value):
                next_lines.append("data: " + json.dumps(value, separators=(",", ":")))
                changed = True
            else:
                next_lines.append(line)
        suffix = "\n" if text.endswith("\n") else ""
        return ("\n".join(next_lines) + suffix).encode("utf-8") if changed else raw
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        return raw
    if not _append_azure_break_glass_tool_to_json(value):
        return raw
    return json.dumps(value, separators=(",", ":")).encode("utf-8")


def _append_azure_break_glass_tool_to_json(value) -> bool:
    if not isinstance(value, dict):
        return False
    result = value.get("result")
    if not isinstance(result, dict):
        return False
    tools = result.setdefault("tools", [])
    if not isinstance(tools, list):
        return False
    if any(isinstance(tool, dict) and tool.get("name") == _TANK_AZURE_BREAK_GLASS_TOOL for tool in tools):
        return False
    tools.append(
        {
            "name": _TANK_AZURE_BREAK_GLASS_TOOL,
            "description": (
                "Record a request for break-glass access to the azure-personal MCP "
                "(Postgres, Key Vault, Cosmos, ARM/AKS) and return a human approval "
                "URL. The azure-personal MCP is locked by default and normal feature "
                "work never needs it. This tool does not grant access or reveal a "
                "token. After an admin approves, the azure-personal tools become "
                "available for the session until the grant expires; reload the MCP "
                "registry to see them."
            ),
            "inputSchema": {
                "type": "object",
                "properties": {
                    "reason": {
                        "type": "string",
                        "description": "Short reason azure access is needed and why no governed path suffices.",
                    },
                    "source": {
                        "type": "string",
                        "description": "Caller source such as agent.",
                    },
                },
                "additionalProperties": False,
            },
        }
    )
    return True


def _augment_glimmung_hot_swap_tool_schema(tool: dict) -> bool:
    schema = tool.get("inputSchema")
    if not isinstance(schema, dict):
        schema = {"type": "object", "properties": {}}
        tool["inputSchema"] = schema
    properties = schema.get("properties")
    if not isinstance(properties, dict):
        properties = {}
        schema["properties"] = properties

    changed = False
    if "repo" not in properties:
        properties["repo"] = {
            "type": "string",
            "description": "Optional GitHub slug, for example romaine-life/tank-operator. Used by Tank to verify the governed branch before hot-swap.",
        }
        changed = True
    if "repo_path" not in properties:
        properties["repo_path"] = {
            "type": "string",
            "description": "Optional absolute or /workspace-relative path to the repo. Used by Tank to verify branch, publish, PR, CI, and mergeability before hot-swap.",
        }
        changed = True
    return changed


def _append_ci_reminder(raw: bytes) -> bytes:
    text = raw.decode("utf-8", errors="replace")
    if any(line.strip().startswith("data:") for line in text.splitlines()):
        next_lines: list[str] = []
        changed = False
        for line in text.splitlines():
            stripped = line.strip()
            if not stripped.startswith("data:") or changed:
                next_lines.append(line)
                continue
            payload = stripped[5:].strip()
            try:
                value = json.loads(payload)
            except json.JSONDecodeError:
                next_lines.append(line)
                continue
            if _append_ci_reminder_to_json(value):
                next_lines.append("data: " + json.dumps(value, separators=(",", ":")))
                changed = True
            else:
                next_lines.append(line)
        suffix = "\n" if text.endswith("\n") else ""
        return ("\n".join(next_lines) + suffix).encode("utf-8") if changed else raw
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        return raw
    if not _append_ci_reminder_to_json(value):
        return raw
    return json.dumps(value, separators=(",", ":")).encode("utf-8")


def _append_ci_reminder_to_json(value) -> bool:
    if not isinstance(value, dict):
        return False
    result = value.get("result")
    if not isinstance(result, dict):
        return False
    content = result.setdefault("content", [])
    if not isinstance(content, list):
        return False
    for item in content:
        if isinstance(item, dict) and item.get("type") == "text" and _CI_ENFORCEMENT_TEXT in str(item.get("text", "")):
            return False
    content.append({"type": "text", "text": _CI_ENFORCEMENT_TEXT})
    return True


async def _record_github_tool_activity(
    http: ClientSession,
    auth_romaine_provider,
    tool_name: str,
    arguments: dict,
    response_body: bytes,
) -> None:
    if not ORIGIN_SESSION_ID:
        return
    if tool_name not in {"create_pull_request", "commit_to_branch", "create_or_update_file"}:
        return
    response_objects = _json_objects_from_mcp_body(response_body)
    if not response_objects:
        return
    token = await auth_romaine_provider.token()
    headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}
    if tool_name == "create_pull_request":
        pr = _first_pr_from_response(response_objects)
        if pr is None:
            return
        owner, name, url, number = pr
        payload = {
            "event_id": f"mcp-github-pr-open-{ORIGIN_SESSION_ID}-{uuid4().hex}",
            "invocation_id": f"mcp-github-pr-open-{uuid4().hex}",
            "source_service": "mcp-github",
            "source_tool": tool_name,
            "action": "github.pull_request.open",
            "status": "succeeded",
            "target_kind": "github_pull_request",
            "target_ref": url,
            "repo_owner": owner,
            "repo_name": name,
            "pr_number": number,
            "payload": {
                "base": arguments.get("base"),
                "head": arguments.get("head"),
                "draft": arguments.get("draft"),
                "title": arguments.get("title"),
            },
        }
        await _post_tank_control_action(http, headers, payload)
        await _post_tank_pull_request_link(http, headers, url)
        return
    commit = _first_commit_from_response(response_objects, arguments)
    if commit is None:
        return
    owner, name, url, sha = commit
    payload = {
        "event_id": f"mcp-github-commit-{ORIGIN_SESSION_ID}-{sha}-{uuid4().hex}",
        "invocation_id": f"mcp-github-commit-{uuid4().hex}",
        "source_service": "mcp-github",
        "source_tool": tool_name,
        "action": "github.commit.write",
        "status": "succeeded",
        "target_kind": "github_commit",
        "target_ref": url,
        "repo_owner": owner,
        "repo_name": name,
        "result_sha": sha,
        "payload": {
            "branch": arguments.get("branch"),
            "message": arguments.get("message"),
            "path": arguments.get("path"),
        },
    }
    await _post_tank_control_action(http, headers, payload)


async def _post_tank_control_action(http: ClientSession, headers: dict[str, str], payload: dict) -> None:
    url = f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/control-actions"
    async with http.post(url, headers=headers, json=payload) as resp:
        if resp.status >= 400:
            text = await resp.text()
            log.warning("failed to record GitHub MCP activity in Tank: status=%d body=%s", resp.status, text[:500])


async def _post_tank_pull_request_link(http: ClientSession, headers: dict[str, str], pr_url: str) -> None:
    url = f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/pull-request-link"
    async with http.post(url, headers=headers, json={"url": pr_url}) as resp:
        if resp.status >= 400:
            text = await resp.text()
            log.warning("failed to update Tank pull request link: status=%d body=%s", resp.status, text[:500])


async def _post_tank_hot_swap_verify(http: ClientSession, service_token: str, payload: dict) -> dict:
    url = f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/hot-swap/verify"
    async with http.post(
        url,
        headers={"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
        json=payload,
    ) as resp:
        text = await resp.text()
    try:
        body = json.loads(text) if text else {}
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Tank hot-swap verification returned invalid JSON: {text[:500]}") from exc
    if resp.status >= 400 and not isinstance(body, dict):
        raise RuntimeError(f"Tank hot-swap verification failed with HTTP {resp.status}: {text[:500]}")
    if isinstance(body, dict):
        body.setdefault("http_status", resp.status)
        return body
    return {"allowed": False, "http_status": resp.status, "reasons": [f"Tank hot-swap verification failed with HTTP {resp.status}"]}


async def _get_tank_pr_lane_authorization(http: ClientSession, service_token: str, request_event_id: str) -> dict:
    from urllib.parse import quote

    url = (
        f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/pr-lane-requests/"
        f"{quote(request_event_id, safe='')}/authorization"
    )
    async with http.get(url, headers={"Authorization": f"Bearer {service_token}"}) as resp:
        text = await resp.text()
    try:
        body = json.loads(text) if text else {}
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Tank PR lane authorization returned invalid JSON: {text[:500]}") from exc
    if isinstance(body, dict):
        body.setdefault("http_status", resp.status)
        return body
    return {"allowed": False, "http_status": resp.status, "reasons": [f"Tank PR lane authorization failed with HTTP {resp.status}"]}


async def _call_mcp_github_tool(http: ClientSession, service_token: str, name: str, arguments: dict) -> list[dict]:
    payload = {
        "jsonrpc": "2.0",
        "id": f"tank-lane-{uuid4().hex}",
        "method": "tools/call",
        "params": {"name": name, "arguments": arguments},
    }
    async with http.post(
        f"{MCP_GITHUB_INTERNAL_URL}/",
        headers={
            "Authorization": f"Bearer {service_token}",
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        },
        json=payload,
    ) as resp:
        body = await resp.read()
        if resp.status >= 400:
            raise RuntimeError(f"mcp-github {name} failed with HTTP {resp.status}: {body[:500]!r}")
    objects = _json_objects_from_mcp_body(body)
    for obj in objects:
        if isinstance(obj, dict) and isinstance(obj.get("error"), dict):
            raise RuntimeError(str(obj["error"].get("message") or f"mcp-github {name} failed"))
    return objects


async def _run_git(repo_path: Path, *args: str, env: dict[str, str] | None = None, timeout: float = 60) -> tuple[int, str, str]:
    proc = await asyncio.create_subprocess_exec(
        "git",
        "-C",
        str(repo_path),
        *args,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=env,
    )
    try:
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        return 124, "", f"git {' '.join(args)} timed out"
    return (
        proc.returncode,
        stdout.decode("utf-8", errors="replace").strip(),
        stderr.decode("utf-8", errors="replace").strip(),
    )


async def _git_output(repo_path: Path, *args: str) -> str:
    rc, stdout, stderr = await _run_git(repo_path, *args)
    if rc != 0:
        raise RuntimeError(stderr or stdout or f"git {' '.join(args)} failed with exit code {rc}")
    return stdout.strip()


def _repo_slug_from_remote(remote_url: str) -> tuple[str, str] | None:
    match = _GITHUB_REMOTE_RE.search(remote_url.strip())
    if not match:
        return None
    owner, name = match.groups()
    return owner, name.removesuffix(".git")


def _normalize_repo_slug(value: object) -> str:
    slug = str(value or "").strip()
    if not slug:
        return ""
    parts = slug.split("/")
    if len(parts) != 2 or not parts[0].strip() or not parts[1].strip():
        raise ValueError("repo values must be GitHub slugs like owner/name")
    return f"{parts[0].strip()}/{parts[1].strip()}"


def _string_list_argument(arguments: dict, key: str) -> list[str]:
    raw = arguments.get(key) or []
    if isinstance(raw, str):
        raw = [raw]
    if not isinstance(raw, list):
        raise ValueError(f"{key} must be an array of strings")
    out: list[str] = []
    for value in raw:
        item = str(value or "").strip()
        if item and item not in out:
            out.append(item)
    return out


def _normalize_repo_list(values: object) -> list[str]:
    if not isinstance(values, list):
        raise ValueError("repo_scope.repos must be an array of GitHub slugs")
    repos = [_normalize_repo_slug(value) for value in values]
    out: list[str] = []
    for repo in repos:
        if repo and repo not in out:
            out.append(repo)
    return out


async def _current_repo_from_arguments(arguments: dict, explicit_repo: object = "") -> tuple[str, Path | None]:
    repo = _normalize_repo_slug(explicit_repo)
    if repo:
        return repo, None
    repo_path = _repo_path_from_arguments(arguments)
    remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
    slug = _repo_slug_from_remote(remote_url)
    if slug is None:
        raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
    return f"{slug[0]}/{slug[1]}", repo_path


async def _repo_scope_from_arguments(arguments: dict) -> tuple[dict, str, list[str], Path | None]:
    scope = arguments.get("repo_scope")
    if not isinstance(scope, dict):
        raise ValueError("repo_scope is required")
    kind = str(scope.get("kind") or "").strip()
    if kind == "current_repo":
        if scope.get("repos"):
            raise ValueError("repo_scope current_repo rejects repos")
        repo, repo_path = await _current_repo_from_arguments(arguments, scope.get("repo"))
        return {"kind": "current_repo", "repo": repo}, repo, [repo], repo_path
    if kind == "repos":
        if str(scope.get("repo") or "").strip():
            raise ValueError("repo_scope repos rejects repo")
        repos = _normalize_repo_list(scope.get("repos"))
        if not repos:
            raise ValueError("repo_scope repos requires at least one repo")
        return {"kind": "repos", "repos": repos}, repos[0], repos, None
    if kind == "all_repos":
        if str(scope.get("repo") or "").strip() or scope.get("repos"):
            raise ValueError("repo_scope all_repos rejects repo and repos")
        return {"kind": "all_repos"}, "", [], None
    raise ValueError("repo_scope.kind must be current_repo, repos, or all_repos")


def _branch_scope_from_arguments(arguments: dict) -> dict:
    scope = arguments.get("branch_scope")
    if not isinstance(scope, dict):
        raise ValueError("branch_scope is required")
    kind = str(scope.get("kind") or "").strip()
    branches_raw = scope.get("branches") or []
    count_raw = scope.get("count")
    if kind == "named":
        if count_raw not in (None, 0, ""):
            raise ValueError("branch_scope named rejects count")
        branches = [_sanitize_pr_lane_name(value) for value in _normalize_string_list(branches_raw, "branch_scope.branches")]
        branches = [branch for branch in branches if branch]
        if not branches:
            raise ValueError("branch_scope named requires branches")
        return {"kind": "named", "branches": branches}
    if kind == "count":
        if branches_raw:
            raise ValueError("branch_scope count rejects branches")
        try:
            count = int(count_raw or 0)
        except (TypeError, ValueError) as exc:
            raise ValueError("branch_scope count requires an integer count") from exc
        if count <= 0:
            raise ValueError("branch_scope count requires a positive count")
        return {"kind": "count", "count": min(count, 50)}
    if kind == "unlimited":
        if branches_raw or count_raw not in (None, 0, ""):
            raise ValueError("branch_scope unlimited rejects branches and count")
        return {"kind": "unlimited"}
    raise ValueError("branch_scope.kind must be named, count, or unlimited")


def _normalize_string_list(value: object, label: str) -> list[str]:
    if isinstance(value, str):
        value = [value]
    if not isinstance(value, list):
        raise ValueError(f"{label} must be an array of strings")
    out: list[str] = []
    for item in value:
        text = str(item or "").strip()
        if text and text not in out:
            out.append(text)
    return out


def _repo_path_from_arguments(arguments: dict) -> Path:
    repo = str(arguments.get("repo") or "").strip()
    repo_path = str(arguments.get("repo_path") or "").strip()
    if repo_path:
        path = Path(repo_path)
        if not path.is_absolute():
            path = WORKSPACE_ROOT / path
        path = path.resolve()
    elif repo:
        name = repo.split("/")[-1]
        path = (WORKSPACE_ROOT / name).resolve()
    else:
        candidates = [
            item.resolve()
            for item in WORKSPACE_ROOT.iterdir()
            if item.is_dir() and (item / ".git").exists()
        ]
        if len(candidates) != 1:
            raise ValueError("repo or repo_path is required when the workspace has zero or multiple repos")
        path = candidates[0]
    try:
        path.relative_to(WORKSPACE_ROOT)
    except ValueError as exc:
        raise ValueError(f"repo_path must be under {WORKSPACE_ROOT}") from exc
    if not path.exists():
        raise ValueError(f"repo path does not exist: {path}")
    return path


def _pr_lane_worktree_path(repo_slug: str, lane_name: str) -> Path:
    owner, repo = repo_slug.split("/", 1)
    safe_lane = re.sub(r"[^A-Za-z0-9._-]+", "-", lane_name).strip("-._") or "lane"
    return (WORKSPACE_ROOT / ".tank" / "pr-lanes" / owner / repo / safe_lane).resolve()


def _base_branch_ref(base: str) -> tuple[str, str] | None:
    cleaned = base.strip()
    if not cleaned:
        return None
    if re.fullmatch(r"[0-9a-fA-F]{7,40}", cleaned):
        return None
    if cleaned.startswith("refs/heads/"):
        branch = cleaned.removeprefix("refs/heads/")
    elif cleaned.startswith("origin/"):
        branch = cleaned.removeprefix("origin/")
    else:
        branch = cleaned
    return branch, f"origin/{branch}"


def _sanitize_pr_lane_name(value: object) -> str:
    raw = str(value or "").strip()
    if raw.startswith("refs/heads/"):
        raw = raw.removeprefix("refs/heads/")
    if "/" in raw:
        raw = raw.rsplit("/", 1)[-1]
    return re.sub(r"[^A-Za-z0-9._-]+", "-", raw).strip("-._")


async def _ensure_pr_lane_worktree(
    source_repo_path: Path,
    *,
    branch: str,
    base: str,
    lane_name: str,
    worktree_path: Path,
) -> None:
    try:
        worktree_path.relative_to(WORKSPACE_ROOT)
    except ValueError as exc:
        raise ValueError(f"lane worktree path must be under {WORKSPACE_ROOT}") from exc

    if worktree_path.exists():
        current = await _git_output(worktree_path, "branch", "--show-current")
        if current == branch:
            return
        raise ValueError(f"lane worktree already exists at {worktree_path} on branch {current!r}")

    worktree_path.parent.mkdir(parents=True, exist_ok=True)
    base_ref = base.strip() or "main"
    remote_ref = _base_branch_ref(base_ref)
    if remote_ref is not None:
        remote_branch, remote_tracking_ref = remote_ref
        rc, stdout, stderr = await _run_git(
            source_repo_path,
            "fetch",
            "origin",
            f"{remote_branch}:refs/remotes/origin/{remote_branch}",
            timeout=180,
        )
        if rc == 0:
            base_ref = remote_tracking_ref
        else:
            verify_rc, verify_stdout, verify_stderr = await _run_git(
                source_repo_path,
                "rev-parse",
                "--verify",
                base_ref,
            )
            if verify_rc != 0:
                raise RuntimeError(stderr or stdout or verify_stderr or verify_stdout or f"could not resolve base {base!r}")

    rc, stdout, stderr = await _run_git(
        source_repo_path,
        "worktree",
        "add",
        "-B",
        branch,
        str(worktree_path),
        base_ref,
        timeout=180,
    )
    if rc != 0:
        raise RuntimeError(stderr or stdout or f"git worktree add failed with exit code {rc}")

    rc, stdout, stderr = await _run_git(
        worktree_path,
        "-c",
        "user.name=Tank Operator",
        "-c",
        "user.email=tank-operator@romaine.life",
        "-c",
        "core.hooksPath=/dev/null",
        "commit",
        "--allow-empty",
        "-m",
        f"Tank session {ORIGIN_SESSION_ID} PR lane {lane_name}",
        timeout=60,
    )
    if rc != 0:
        raise RuntimeError(stderr or stdout or f"git empty commit failed with exit code {rc}")


def _repo_path_for_hot_swap(arguments: dict) -> Path:
    repo_path = str(arguments.get("repo_path") or "").strip()
    repo = str(arguments.get("repo") or "").strip()
    if repo_path or repo:
        return _repo_path_from_arguments(arguments)
    project = str(arguments.get("project") or "").strip()
    if project:
        path = (WORKSPACE_ROOT / project).resolve()
        try:
            path.relative_to(WORKSPACE_ROOT)
        except ValueError as exc:
            raise ValueError(f"project repo path must be under {WORKSPACE_ROOT}") from exc
        if path.exists() and (path / ".git").exists():
            return path
    return _repo_path_from_arguments(arguments)


def _rewrite_mcp_tool_arguments(body: bytes, arguments: dict) -> bytes:
    payload = json.loads(body.decode("utf-8"))
    payload["params"]["arguments"] = arguments
    return json.dumps(payload, separators=(",", ":")).encode("utf-8")


async def _verify_github_hot_swap_head(
    http: ClientSession,
    github_token: str,
    owner: str,
    repo: str,
    branch: str,
    sha: str,
) -> dict:
    reasons: list[str] = []
    branch_status, branch_body = await _github_api_json(
        http,
        github_token,
        "GET",
        f"/repos/{owner}/{repo}/branches/{quote(branch, safe='')}",
    )
    branch_sha = ""
    if branch_status < 400 and isinstance(branch_body, dict):
        commit = branch_body.get("commit")
        if isinstance(commit, dict):
            branch_sha = str(commit.get("sha") or "")
    if branch_sha != sha:
        reasons.append(f"remote branch {branch} is at {branch_sha[:12] or 'unknown'}, not local HEAD {sha[:12]}")

    pr_number: int | None = None
    pr_url = ""
    head = quote(f"{owner}:{branch}", safe="")
    pr_status, pr_body = await _github_api_json(
        http,
        github_token,
        "GET",
        f"/repos/{owner}/{repo}/pulls?head={head}&state=open",
    )
    if pr_status >= 400 or not isinstance(pr_body, list) or not pr_body:
        reasons.append(f"no open PR exists for {owner}:{branch}")
    else:
        pr = pr_body[0] if isinstance(pr_body[0], dict) else {}
        pr_number = int(pr.get("number") or 0) or None
        pr_url = str(pr.get("html_url") or "")
        pr_head = pr.get("head")
        pr_head_sha = str(pr_head.get("sha") or "") if isinstance(pr_head, dict) else ""
        if pr_head_sha and pr_head_sha != sha:
            reasons.append(f"open PR head is {pr_head_sha[:12]}, not local HEAD {sha[:12]}")
        if pr_number is None:
            reasons.append("open PR response did not include a PR number")
        else:
            detail_status, detail_body = await _github_api_json(
                http,
                github_token,
                "GET",
                f"/repos/{owner}/{repo}/pulls/{pr_number}",
            )
            if detail_status >= 400 or not isinstance(detail_body, dict):
                reasons.append(f"could not read PR #{pr_number} mergeability")
            else:
                mergeable = detail_body.get("mergeable")
                mergeable_state = str(detail_body.get("mergeable_state") or "")
                if mergeable is not True or mergeable_state == "dirty":
                    reasons.append(f"PR #{pr_number} is not confirmed mergeable: {mergeable_state or mergeable}")

    check_status, check_body = await _github_api_json(
        http,
        github_token,
        "GET",
        f"/repos/{owner}/{repo}/commits/{sha}/check-runs",
    )
    status_status, status_body = await _github_api_json(
        http,
        github_token,
        "GET",
        f"/repos/{owner}/{repo}/commits/{sha}/status",
    )
    check_runs = []
    if check_status < 400 and isinstance(check_body, dict) and isinstance(check_body.get("check_runs"), list):
        check_runs = check_body["check_runs"]
    combined_status = status_body if status_status < 400 and isinstance(status_body, dict) else None
    ci_status, ci_error, ci_payload = _checks_state(check_runs, combined_status)
    if ci_status != "succeeded":
        reasons.append(f"CI for {sha[:12]} is not green: {ci_error}")
    return {
        "allowed": not reasons,
        "reasons": reasons,
        "branch_sha": branch_sha,
        "pr_number": pr_number,
        "pr_url": pr_url,
        "ci_status": ci_status,
        "ci": ci_payload,
    }


def _break_glass_approval_url(
    session_id: str,
    repo_scope: dict,
    branch_scope: dict,
    reason: str,
    source: str,
    *,
    session_scope: str | None = None,
) -> str:
    scope = (session_scope or ORIGIN_SESSION_SCOPE or "default").strip() or "default"
    params = {
        "intent": "git-break-glass",
        "session_id": session_id,
        "session_scope": scope,
        "repo_scope": json.dumps(repo_scope, separators=(",", ":")),
        "branch_scope": json.dumps(branch_scope, separators=(",", ":")),
        "source": source,
    }
    if reason:
        params["reason"] = reason
    separator = "&" if "?" in _AUTH_ROMAINE_BREAK_GLASS_URL else "?"
    return f"{_AUTH_ROMAINE_BREAK_GLASS_URL}{separator}{urlencode(params)}"


def _azure_break_glass_approval_url(
    session_id: str,
    reason: str,
    source: str,
    session_scope: str | None = None,
) -> str:
    # Mirrors _break_glass_approval_url. azure-personal break-glass is not
    # repo-scoped, so there is no repo param; the auth.romaine.life admin
    # console keys its approval card on intent=azure-break-glass.
    scope = (session_scope or ORIGIN_SESSION_SCOPE or "default").strip() or "default"
    params = {
        "intent": "azure-break-glass",
        "session_id": session_id,
        "session_scope": scope,
        "source": source,
    }
    if reason:
        params["reason"] = reason
    separator = "&" if "?" in _AUTH_ROMAINE_BREAK_GLASS_URL else "?"
    return f"{_AUTH_ROMAINE_BREAK_GLASS_URL}{separator}{urlencode(params)}"


def _pr_lane_approval_url(session_id: str, request_event_id: str) -> str:
    return f"{TANK_UI_HOST}/?{urlencode({'session': session_id, 'pr_lane_request': request_event_id})}"


async def _active_break_glass_grant(http: ClientSession, service_jwt: str, repo_slug: str = "") -> dict | None:
    if not ORIGIN_SESSION_ID:
        return None
    from urllib.parse import quote

    params = f"?repo={quote(repo_slug, safe='')}" if repo_slug else ""
    url = f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/git-break-glass/grant{params}"
    async with http.get(url, headers={"Authorization": f"Bearer {service_jwt}"}) as resp:
        body = await resp.text()
        if resp.status == 204 or not body.strip():
            return None
        if resp.status >= 400:
            raise RuntimeError(f"Tank break-glass grant lookup failed with HTTP {resp.status}: {body[:500]}")
    try:
        value = json.loads(body)
    except json.JSONDecodeError as exc:
        log.warning(
            "Tank break-glass grant lookup returned invalid JSON; treating as no active grant",
            exc_info=exc,
        )
        return None
    if isinstance(value, dict) and value.get("active") is True:
        return value
    return None


async def _active_azure_break_glass_grant(http: ClientSession, service_jwt: str) -> dict | None:
    # Mirrors _active_break_glass_grant; azure grants are session-scoped, not
    # repo-scoped, so there is no repo query parameter. Used only to surface
    # status in request_azure_break_glass — the real enforcement lives in
    # mcp-azure-personal, which performs the same lookup before serving tools.
    if not ORIGIN_SESSION_ID:
        return None
    url = f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/azure-break-glass/grant"
    async with http.get(url, headers={"Authorization": f"Bearer {service_jwt}"}) as resp:
        body = await resp.text()
        if resp.status == 204 or not body.strip():
            return None
        if resp.status >= 400:
            raise RuntimeError(f"Tank azure break-glass grant lookup failed with HTTP {resp.status}: {body[:500]}")
    try:
        value = json.loads(body)
    except json.JSONDecodeError as exc:
        log.warning(
            "Tank azure break-glass grant lookup returned invalid JSON; treating as no active grant",
            exc_info=exc,
        )
        return None
    if isinstance(value, dict) and value.get("active") is True:
        return value
    return None


async def _active_pr_lane_auto_approval(
    http: ClientSession,
    service_jwt: str,
    repo_slug: str,
    *,
    lane_name: str = "",
    proposed_branch: str = "",
) -> dict | None:
    if not ORIGIN_SESSION_ID:
        return None
    from urllib.parse import quote

    params = f"?repo={quote(repo_slug, safe='')}"
    if lane_name:
        params += f"&lane_name={quote(lane_name, safe='')}"
    if proposed_branch:
        params += f"&proposed_branch={quote(proposed_branch, safe='')}"
    url = (
        f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/pr-lane-auto-approval"
        f"{params}"
    )
    async with http.get(url, headers={"Authorization": f"Bearer {service_jwt}"}) as resp:
        body = await resp.text()
        if resp.status == 204 or not body.strip():
            return None
        if resp.status >= 400:
            raise RuntimeError(f"Tank PR lane auto-approval lookup failed with HTTP {resp.status}: {body[:500]}")
    try:
        value = json.loads(body)
    except json.JSONDecodeError as exc:
        log.warning(
            "Tank PR lane auto-approval lookup returned invalid JSON; treating as inactive",
            exc_info=exc,
        )
        return None
    if isinstance(value, dict) and value.get("active") is True:
        return value
    return None


def _grant_allows(grant: dict | None, operation: str) -> bool:
    if not grant:
        return False
    operations = grant.get("operations")
    if not isinstance(operations, list):
        return False
    return operation in {str(item) for item in operations}


def _grant_branch_allows(grant: dict | None, branch: str) -> bool:
    if not grant:
        return False
    branch_scope = grant.get("branch_scope")
    if not isinstance(branch_scope, dict):
        return False
    kind = str(branch_scope.get("kind") or "").strip()
    if kind == "unlimited":
        return True
    if kind == "named":
        branches = branch_scope.get("branches")
        if not isinstance(branches, list) or not branches:
            return False
        allowed = {str(item).removeprefix("refs/heads/").strip() for item in branches}
        return branch.removeprefix("refs/heads/").strip() in allowed
    if kind == "count":
        return True
    return False


def _grant_covers_branch_scope(grant: dict | None, requested_scope: dict) -> bool:
    if not grant:
        return False
    grant_scope = grant.get("branch_scope")
    if not isinstance(grant_scope, dict):
        return False
    grant_kind = str(grant_scope.get("kind") or "").strip()
    requested_kind = str(requested_scope.get("kind") or "").strip()
    if grant_kind == "unlimited":
        return True
    if requested_kind == "unlimited":
        return False
    if grant_kind == "named":
        grant_branches = grant_scope.get("branches")
        requested_branches = requested_scope.get("branches")
        if not isinstance(grant_branches, list) or not isinstance(requested_branches, list) or requested_kind != "named":
            return False
        allowed = {str(item).removeprefix("refs/heads/").strip() for item in grant_branches if str(item).strip()}
        return all(str(item).removeprefix("refs/heads/").strip() in allowed for item in requested_branches if str(item).strip())
    if grant_kind == "count":
        try:
            remaining_count = int(grant.get("remaining_branches"))
        except (TypeError, ValueError):
            return False
        if requested_kind == "count":
            try:
                requested_count = int(requested_scope.get("count") or 0)
            except (TypeError, ValueError):
                return False
            return remaining_count >= requested_count
        if requested_kind == "named":
            requested_branches = requested_scope.get("branches")
            if not isinstance(requested_branches, list):
                return False
            unique_requested = {str(item).removeprefix("refs/heads/").strip() for item in requested_branches if str(item).strip()}
            return remaining_count >= len(unique_requested)
    return False


def _grant_is_branch_restricted(grant: dict | None) -> bool:
    if not grant:
        return False
    branch_scope = grant.get("branch_scope")
    return isinstance(branch_scope, dict) and str(branch_scope.get("kind") or "").strip() != "unlimited"


def _activation_scope_allows_repo(activation: dict | None, repo_slug: str) -> bool:
    if not activation:
        return False
    repo_scope = activation.get("repo_scope")
    if not isinstance(repo_scope, dict):
        return False
    kind = str(repo_scope.get("kind") or "").strip()
    if kind == "all_repos":
        return True
    if kind == "current_repo":
        return str(repo_scope.get("repo") or "").strip() == repo_slug
    if kind == "repos":
        repos = repo_scope.get("repos")
        if not isinstance(repos, list):
            return False
        return repo_slug in {str(item).strip() for item in repos}
    return False


def _break_glass_activation_path() -> Path:
    return WORKSPACE_ROOT / ".tank" / "git-break-glass-active.json"


def _read_break_glass_activation() -> dict | None:
    try:
        value = json.loads(_break_glass_activation_path().read_text(encoding="utf-8"))
    except Exception:
        return None
    return value if isinstance(value, dict) else None


def _activate_break_glass_mcp_config(repo_slug: str, grant: dict) -> dict:
    changed: list[str] = []
    marker_path = _break_glass_activation_path()
    marker_path.parent.mkdir(parents=True, exist_ok=True)
    marker = {
        "server_name": _BREAK_GLASS_MCP_SERVER_NAME,
        "server_url": f"http://127.0.0.1:{_BREAK_GLASS_MCP_PORT}/",
        "repo": repo_slug,
        "grant_event_id": grant.get("event_id"),
        "expires_at": grant.get("expires_at"),
        "repo_scope": grant.get("repo_scope") if isinstance(grant.get("repo_scope"), dict) else {},
        "branch_scope": grant.get("branch_scope") if isinstance(grant.get("branch_scope"), dict) else {},
        "activated_at": datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z"),
    }
    marker_path.write_text(json.dumps(marker, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    changed.append(str(marker_path))

    mcp_path = WORKSPACE_ROOT / ".mcp.json"
    try:
        config = json.loads(mcp_path.read_text(encoding="utf-8"))
    except Exception:
        config = {}
    try:
        servers = config.setdefault("mcpServers", {})
        if isinstance(servers, dict):
            wanted = {"type": "http", "url": f"http://127.0.0.1:{_BREAK_GLASS_MCP_PORT}/"}
            if servers.get(_BREAK_GLASS_MCP_SERVER_NAME) != wanted:
                servers[_BREAK_GLASS_MCP_SERVER_NAME] = wanted
                mcp_path.write_text(json.dumps(config, indent=2, sort_keys=True) + "\n", encoding="utf-8")
                changed.append(str(mcp_path))
    except Exception:
        log.warning("failed to update workspace MCP config for break-glass", exc_info=True)

    codex_config = WORKSPACE_ROOT / ".tank" / "codex" / "config.toml"
    block = f'[mcp_servers.{_BREAK_GLASS_MCP_SERVER_NAME}]\nurl = "http://127.0.0.1:{_BREAK_GLASS_MCP_PORT}/"\n'
    try:
        if codex_config.exists():
            text = codex_config.read_text(encoding="utf-8")
            if f"[mcp_servers.{_BREAK_GLASS_MCP_SERVER_NAME}]" not in text:
                codex_config.write_text(text.rstrip() + "\n\n" + block, encoding="utf-8")
                changed.append(str(codex_config))
    except Exception:
        log.warning("failed to update Codex MCP config for break-glass", exc_info=True)

    claude_settings = WORKSPACE_ROOT / ".tank" / "claude" / "settings.json"
    try:
        if claude_settings.exists():
            settings = json.loads(claude_settings.read_text(encoding="utf-8"))
            permissions = settings.setdefault("permissions", {})
            allow = permissions.setdefault("allow", [])
            if isinstance(allow, list) and f"mcp__{_BREAK_GLASS_MCP_SERVER_NAME}" not in allow:
                allow.append(f"mcp__{_BREAK_GLASS_MCP_SERVER_NAME}")
                allow.sort()
                claude_settings.write_text(json.dumps(settings, indent=2, sort_keys=True) + "\n", encoding="utf-8")
                changed.append(str(claude_settings))
    except Exception:
        log.warning("failed to update Claude settings for break-glass", exc_info=True)

    return {
        "server_name": _BREAK_GLASS_MCP_SERVER_NAME,
        "server_url": f"http://127.0.0.1:{_BREAK_GLASS_MCP_PORT}/",
        "repo": repo_slug,
        "grant_event_id": grant.get("event_id"),
        "expires_at": grant.get("expires_at"),
        "repo_scope": grant.get("repo_scope") if isinstance(grant.get("repo_scope"), dict) else {},
        "branch_scope": grant.get("branch_scope") if isinstance(grant.get("branch_scope"), dict) else {},
        "changed_files": changed,
        "reload_required": True,
    }


async def _mint_github_installation_token(http: ClientSession, service_token: str, repo_slug: str, *, workflows: bool = False, full: bool = False) -> str:
    arguments: dict[str, object] = {"repos": [repo_slug], "write": True}
    if workflows:
        arguments["workflows"] = True
    if full:
        # Break-glass mint_full_git_token: request the App's entire granted
        # permission set (not just contents) so the approved token is a genuine
        # escape hatch. The internal governed-publish callers deliberately omit
        # this and stay contents-only.
        arguments["full"] = True
    payload = {
        "jsonrpc": "2.0",
        "id": f"tank-publish-{uuid4().hex}",
        "method": "tools/call",
        "params": {"name": "mint_clone_token", "arguments": arguments},
    }
    async with http.post(
        f"{MCP_GITHUB_INTERNAL_URL}/",
        headers={
            "Authorization": f"Bearer {service_token}",
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        },
        json=payload,
    ) as resp:
        body = await resp.read()
        if resp.status >= 400:
            raise RuntimeError(f"mcp-github mint_clone_token failed with HTTP {resp.status}: {body[:500]!r}")
    objects = _json_objects_from_mcp_body(body)
    for obj in objects:
        result = obj.get("result")
        if isinstance(result, dict):
            structured = result.get("structuredContent")
            if isinstance(structured, dict):
                token = str(structured.get("token") or "").strip()
                if token:
                    return token
    raise RuntimeError("mcp-github mint_clone_token returned no token")


async def _push_head_with_token(repo_path: Path, branch: str, token: str) -> None:
    askpass_path = ""
    try:
        with tempfile.NamedTemporaryFile("w", delete=False) as askpass:
            askpass.write(
                "#!/bin/sh\n"
                "case \"$1\" in\n"
                "  *Username*) printf '%s\\n' x-access-token ;;\n"
                "  *Password*) printf '%s\\n' \"$GITHUB_TOKEN\" ;;\n"
                "  *) printf '\\n' ;;\n"
                "esac\n"
            )
            askpass_path = askpass.name
        os.chmod(askpass_path, stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR)
        env = os.environ.copy()
        env.update(
            {
                "GIT_ASKPASS": askpass_path,
                "GITHUB_TOKEN": token,
                "GIT_TERMINAL_PROMPT": "0",
            }
        )
        rc, stdout, stderr = await _run_git(
            repo_path,
            "push",
            "--no-verify",
            "origin",
            f"HEAD:refs/heads/{branch}",
            env=env,
            timeout=180,
        )
        if rc != 0:
            raise RuntimeError(stderr or stdout or f"git push failed with exit code {rc}")
    finally:
        if askpass_path:
            try:
                os.unlink(askpass_path)
            except FileNotFoundError:
                pass


async def _github_api_json(
    http: ClientSession,
    token: str,
    method: str,
    path: str,
    *,
    json_body: dict | None = None,
) -> tuple[int, dict | list | None]:
    async with http.request(
        method,
        f"https://api.github.com{path}",
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
        json=json_body,
    ) as resp:
        text = await resp.text()
        if not text:
            return resp.status, None
        try:
            return resp.status, json.loads(text)
        except json.JSONDecodeError:
            return resp.status, {"raw": text[:1000]}


async def _github_graphql_json(http: ClientSession, token: str, query: str, variables: dict) -> tuple[int, dict | None]:
    async with http.request(
        "POST",
        "https://api.github.com/graphql",
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
        json={"query": query, "variables": variables},
    ) as resp:
        text = await resp.text()
        if not text:
            return resp.status, None
        try:
            body = json.loads(text)
        except json.JSONDecodeError:
            return resp.status, {"raw": text[:1000]}
        return resp.status, body if isinstance(body, dict) else {"body": body}


def _check_run_recency(run: dict) -> tuple[str, int]:
    """Sort key for picking the most recent run of a check.

    started_at is ISO8601 (lexically sortable); the GitHub check-run id is a
    monotonic tiebreaker for runs that share a timestamp.
    """
    started = str(run.get("started_at") or run.get("completed_at") or "")
    try:
        run_id = int(run.get("id") or 0)
    except (TypeError, ValueError):
        run_id = 0
    return (started, run_id)


def _latest_check_runs(check_runs: list) -> list:
    """Reduce check runs to the most recent run per check name.

    GitHub retains every run for a commit, including stale runs from an earlier
    attempt. Branch protection evaluates the latest run per name; Tank must too,
    or a check re-run from failure -> success still reads as failed and blocks
    the governed merge/hot-swap gate forever.
    """
    latest: dict[str, dict] = {}
    for run in check_runs:
        if not isinstance(run, dict):
            continue
        name = str(run.get("name") or run.get("app", {}).get("slug") or "check")
        existing = latest.get(name)
        if existing is None or _check_run_recency(run) >= _check_run_recency(existing):
            latest[name] = run
    return list(latest.values())


def _checks_state(check_runs: list, combined_status: dict | None) -> tuple[str, str, dict]:
    conclusions_ok = {"success", "skipped", "neutral"}
    failed: list[str] = []
    pending: list[str] = []
    completed = 0
    for run in _latest_check_runs(check_runs):
        if not isinstance(run, dict):
            continue
        name = str(run.get("name") or run.get("app", {}).get("slug") or "check")
        status = str(run.get("status") or "")
        conclusion = str(run.get("conclusion") or "")
        if status != "completed":
            pending.append(name)
            continue
        completed += 1
        if conclusion not in conclusions_ok:
            failed.append(f"{name}: {conclusion or 'failed'}")
    statuses = []
    if isinstance(combined_status, dict):
        statuses = [s for s in combined_status.get("statuses", []) if isinstance(s, dict)]
        state = str(combined_status.get("state") or "")
        if state in {"failure", "error"}:
            for status in statuses:
                if status.get("state") in {"failure", "error"}:
                    failed.append(f"{status.get('context') or 'status'}: {status.get('state')}")
        elif state == "pending" and statuses:
            pending.append("combined status")
    if failed:
        return "failed", "; ".join(failed), {"pending": pending, "failed": failed, "completed": completed, "statuses": len(statuses)}
    if pending or (not check_runs and not statuses):
        return "started", "checks are pending or have not appeared yet", {"pending": pending, "failed": failed, "completed": completed, "statuses": len(statuses)}
    return "succeeded", "all observed checks passed", {"pending": pending, "failed": failed, "completed": completed, "statuses": len(statuses)}


async def _watch_published_commit(
    http: ClientSession,
    auth_romaine_provider,
    owner: str,
    repo: str,
    branch: str,
    sha: str,
    invocation_id: str,
    source_tool: str = _TANK_PUBLISH_TOOL,
) -> None:
    if not ORIGIN_SESSION_ID:
        return
    commit_url = f"https://github.com/{owner}/{repo}/commit/{sha}"
    pr_number: int | None = None
    for attempt in range(60):
        try:
            service_token = await auth_romaine_provider.token()
            github_token = await _mint_github_installation_token(http, service_token, f"{owner}/{repo}")
            check_status, check_body = await _github_api_json(
                http,
                github_token,
                "GET",
                f"/repos/{owner}/{repo}/commits/{sha}/check-runs",
            )
            status_status, status_body = await _github_api_json(
                http,
                github_token,
                "GET",
                f"/repos/{owner}/{repo}/commits/{sha}/status",
            )
            check_runs = []
            if check_status < 400 and isinstance(check_body, dict):
                raw_runs = check_body.get("check_runs")
                if isinstance(raw_runs, list):
                    check_runs = raw_runs
            combined_status = status_body if status_status < 400 and isinstance(status_body, dict) else None
            ci_status, ci_error, ci_payload = _checks_state(check_runs, combined_status)
            headers = {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"}
            await _post_tank_control_action(
                http,
                headers,
                {
                    "event_id": f"tank-publish-ci-{ORIGIN_SESSION_ID}-{sha}-{attempt}",
                    "invocation_id": invocation_id,
                    "source_service": "mcp-tank-operator",
                    "source_tool": source_tool,
                    "action": "github.commit.ci",
                    "status": ci_status,
                    "target_kind": "github_commit",
                    "target_ref": commit_url,
                    "repo_owner": owner,
                    "repo_name": repo,
                    "result_sha": sha,
                    "error": "" if ci_status != "failed" else ci_error,
                    "payload": ci_payload | {"attempt": attempt},
                },
            )

            from urllib.parse import quote

            head = quote(f"{owner}:{branch}", safe="")
            pr_status, pr_body = await _github_api_json(
                http,
                github_token,
                "GET",
                f"/repos/{owner}/{repo}/pulls?head={head}&state=open",
            )
            if pr_status < 400 and isinstance(pr_body, list) and pr_body:
                pr = pr_body[0]
                if isinstance(pr, dict):
                    pr_number = int(pr.get("number") or 0) or None
                    mergeable = pr.get("mergeable")
                    mergeable_state = str(pr.get("mergeable_state") or "")
                    pr_url = str(pr.get("html_url") or f"https://github.com/{owner}/{repo}/pull/{pr_number}")
                    # The /pulls?head= list endpoint returns mergeable=null;
                    # GitHub only computes mergeability on the single-PR GET, so
                    # fetch it (otherwise the recorded observation is stuck
                    # "unknown" forever and the governed merge gate never opens).
                    if pr_number is not None:
                        detail_status, detail_body = await _github_api_json(
                            http,
                            github_token,
                            "GET",
                            f"/repos/{owner}/{repo}/pulls/{pr_number}",
                        )
                        if detail_status < 400 and isinstance(detail_body, dict):
                            mergeable = detail_body.get("mergeable")
                            mergeable_state = str(detail_body.get("mergeable_state") or "")
                            pr_url = str(detail_body.get("html_url") or pr_url)
                    merge_status = "started"
                    merge_error = "mergeability is still unknown"
                    if mergeable is True and mergeable_state not in {"dirty", "blocked"}:
                        merge_status = "succeeded"
                        merge_error = ""
                    elif mergeable is False or mergeable_state == "dirty":
                        merge_status = "failed"
                        merge_error = f"PR is not mergeable: {mergeable_state or 'mergeable=false'}"
                    await _post_tank_control_action(
                        http,
                        headers,
                        {
                            "event_id": f"tank-publish-mergeability-{ORIGIN_SESSION_ID}-{sha}-{attempt}",
                            "invocation_id": invocation_id,
                            "source_service": "mcp-tank-operator",
                            "source_tool": source_tool,
                            "action": "github.pull_request.mergeability",
                            "status": merge_status,
                            "target_kind": "github_pull_request",
                            "target_ref": pr_url,
                            "repo_owner": owner,
                            "repo_name": repo,
                            "pr_number": pr_number,
                            "result_sha": sha,
                            "error": merge_error,
                            "payload": {"branch": branch, "mergeable": mergeable, "mergeable_state": mergeable_state, "attempt": attempt},
                        },
                    )
                    if merge_status == "failed" or (ci_status == "succeeded" and merge_status == "succeeded"):
                        return
            if ci_status == "failed":
                return
        except Exception:
            log.warning("failed while watching published commit %s/%s@%s", owner, repo, sha, exc_info=True)
        await asyncio.sleep(30)

    try:
        service_token = await auth_romaine_provider.token()
        await _post_tank_control_action(
            http,
            {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
            {
                "event_id": f"tank-publish-ci-timeout-{ORIGIN_SESSION_ID}-{sha}",
                "invocation_id": invocation_id,
                "source_service": "mcp-tank-operator",
                "source_tool": source_tool,
                "action": "github.commit.ci",
                "status": "failed",
                "target_kind": "github_commit",
                "target_ref": commit_url,
                "repo_owner": owner,
                "repo_name": repo,
                "result_sha": sha,
                "error": "timed out waiting for GitHub checks",
                "payload": {"branch": branch, "pr_number": pr_number, "timeout_seconds": 1800},
            },
        )
    except Exception:
        log.warning("failed to record CI watcher timeout for %s/%s@%s", owner, repo, sha, exc_info=True)


async def _handle_tank_publish_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-publish-{uuid4().hex}"
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for Tank publish")
        repo_path = _repo_path_from_arguments(arguments)
        git_dir = await _git_output(repo_path, "rev-parse", "--absolute-git-dir")
        if not git_dir:
            raise ValueError(f"{repo_path} is not a git repository")
        branch = await _git_output(repo_path, "branch", "--show-current")
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
        if not _is_tank_session_branch(branch, repo):
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {_default_tank_session_branch(repo)!r} or one of its approved lanes")
        if not bool(arguments.get("allow_dirty")):
            rc, stdout, stderr = await _run_git(repo_path, "status", "--porcelain")
            if rc != 0:
                raise RuntimeError(stderr or stdout or "git status failed")
        if stdout.strip():
            raise ValueError("worktree has uncommitted changes; commit or pass allow_dirty=true before publishing HEAD")
        sha = await _git_output(repo_path, "rev-parse", "HEAD")
        subject = await _git_output(repo_path, "log", "-1", "--format=%s")
        requested_repo = str(arguments.get("repo") or "").strip()
        if requested_repo and requested_repo != f"{owner}/{repo}":
            raise ValueError(f"repo argument {requested_repo!r} does not match origin {owner}/{repo}")
        service_token = await auth_romaine_provider.token()
        github_token = await _mint_github_installation_token(http, service_token, f"{owner}/{repo}")
        headers = {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"}
        started_payload = {
            "event_id": f"tank-publish-start-{ORIGIN_SESSION_ID}-{sha}",
            "invocation_id": invocation_id,
            "source_service": "mcp-tank-operator",
            "source_tool": _TANK_PUBLISH_TOOL,
            "action": "github.commit.push",
            "status": "started",
            "target_kind": "github_commit",
            "target_ref": f"https://github.com/{owner}/{repo}/commit/{sha}",
            "repo_owner": owner,
            "repo_name": repo,
            "result_sha": sha,
            "payload": {"branch": branch, "subject": subject, "source": arguments.get("source") or "agent", "repo_path": str(repo_path)},
        }
        await _post_tank_control_action(http, headers, started_payload)
        await _push_head_with_token(repo_path, branch, github_token)
        await _post_tank_control_action(
            http,
            headers,
            started_payload | {"event_id": f"tank-publish-succeeded-{ORIGIN_SESSION_ID}-{sha}", "status": "succeeded"},
        )
        asyncio.create_task(_watch_published_commit(http, auth_romaine_provider, owner, repo, branch, sha, invocation_id))
        commit_url = f"https://github.com/{owner}/{repo}/commit/{sha}"
        text = (
            f"Published {owner}/{repo}@{sha[:12]} to {branch}.\n"
            f"Commit: {commit_url}\n"
            "Tank recorded the commit and started CI/mergeability watching. "
            f"{_CI_ENFORCEMENT_TEXT}"
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": {
                    "repo": f"{owner}/{repo}",
                    "branch": branch,
                    "sha": sha,
                    "commit_url": commit_url,
                    "ci_watch": "started",
                },
            },
        )
    except Exception as exc:
        log.warning("Tank publish_current_head failed", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32011,
            str(exc),
            {"tool": _TANK_PUBLISH_TOOL, "invocation_id": invocation_id},
        )


async def _mark_pull_request_ready_for_review(
    http: ClientSession,
    github_token: str,
    node_id: str,
) -> None:
    status, body = await _github_graphql_json(
        http,
        github_token,
        """
        mutation MarkPullRequestReadyForReview($pullRequestId: ID!) {
          markPullRequestReadyForReview(input: {pullRequestId: $pullRequestId}) {
            pullRequest {
              id
              isDraft
            }
          }
        }
        """,
        {"pullRequestId": node_id},
    )
    if status >= 400 or not isinstance(body, dict):
        raise RuntimeError(f"GitHub mark ready failed with HTTP {status}: {body!r}")
    errors = body.get("errors")
    if isinstance(errors, list) and errors:
        messages = "; ".join(str(error.get("message") or error) for error in errors if isinstance(error, dict))
        raise RuntimeError(f"GitHub mark ready failed: {messages or errors!r}")


async def _handle_tank_merge_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-merge-{uuid4().hex}"
    service_token = ""
    headers: dict[str, str] = {}
    owner = ""
    repo = ""
    repo_slug = ""
    branch = ""
    sha = ""
    pr_number: int | None = None
    pr_url = ""
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for governed PR merge")
        repo_path = _repo_path_from_arguments(arguments)
        git_dir = await _git_output(repo_path, "rev-parse", "--absolute-git-dir")
        if not git_dir:
            raise ValueError(f"{repo_path} is not a git repository")
        branch = await _git_output(repo_path, "branch", "--show-current")
        if not branch:
            raise ValueError("current repo is detached; checkout the Tank session branch before merging")
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
        repo_slug = f"{owner}/{repo}"
        requested_repo = str(arguments.get("repo") or "").strip()
        if requested_repo and requested_repo != repo_slug:
            raise ValueError(f"repo argument {requested_repo!r} does not match origin {repo_slug}")
        if not _is_tank_session_branch(branch, repo):
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {_default_tank_session_branch(repo)!r} or one of its approved lanes")
        rc, stdout, stderr = await _run_git(repo_path, "status", "--porcelain")
        if rc != 0:
            raise RuntimeError(stderr or stdout or "git status failed")
        if stdout.strip():
            raise ValueError("worktree has uncommitted changes; commit and publish before merging")
        sha = await _git_output(repo_path, "rev-parse", "HEAD")

        raw_pr_number = arguments.get("pr_number")
        requested_pr_number: int | None = None
        if raw_pr_number not in (None, ""):
            try:
                requested_pr_number = int(raw_pr_number)
            except (TypeError, ValueError) as exc:
                raise ValueError("pr_number must be an integer") from exc
            if requested_pr_number <= 0:
                raise ValueError("pr_number must be positive")

        merge_method = str(arguments.get("merge_method") or "").strip()
        if merge_method and merge_method not in {"merge", "squash", "rebase"}:
            raise ValueError("merge_method must be one of merge, squash, or rebase")
        mark_ready = arguments.get("mark_ready")
        if mark_ready is None:
            mark_ready = True
        else:
            mark_ready = bool(mark_ready)

        service_token = await auth_romaine_provider.token()
        headers = {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"}
        # The token is never returned to the agent. Governed merge needs the
        # App's pull-request write permission, but only after the lane checks
        # below have proven this is the session-owned branch and exact PR head.
        github_token = await _mint_github_installation_token(http, service_token, repo_slug, full=True)
        tank_verify = await _post_tank_hot_swap_verify(
            http,
            service_token,
            {
                "repo": repo_slug,
                "branch": branch,
                "sha": sha,
                "source_tool": _TANK_MERGE_TOOL,
            },
        )
        live_verify = await _verify_github_hot_swap_head(http, github_token, owner, repo, branch, sha)
        reasons: list[str] = []
        if tank_verify.get("allowed") is not True:
            tank_reasons = tank_verify.get("reasons")
            if isinstance(tank_reasons, list):
                reasons.extend(f"Tank ledger: {reason}" for reason in tank_reasons)
            else:
                reasons.append("Tank ledger did not confirm governed publish, green CI, and clean mergeability")
        if live_verify.get("allowed") is not True:
            live_reasons = live_verify.get("reasons")
            if isinstance(live_reasons, list):
                reasons.extend(f"GitHub live state: {reason}" for reason in live_reasons)
            else:
                reasons.append("GitHub live state did not confirm latest branch head, open PR, green CI, and clean mergeability")
        pr_number = int(live_verify.get("pr_number") or tank_verify.get("pr_number") or 0) or None
        pr_url = str(live_verify.get("pr_url") or "")
        if requested_pr_number is not None and pr_number != requested_pr_number:
            reasons.append(f"requested PR #{requested_pr_number} does not match governed branch PR #{pr_number or 'unknown'}")
        if pr_number is None:
            reasons.append("no governed PR number was verified")
        if reasons:
            raise ValueError("merge blocked:\n- " + "\n- ".join(reasons))

        detail_status, detail_body = await _github_api_json(
            http,
            github_token,
            "GET",
            f"/repos/{owner}/{repo}/pulls/{pr_number}",
        )
        if detail_status >= 400 or not isinstance(detail_body, dict):
            raise RuntimeError(f"could not read PR #{pr_number}: HTTP {detail_status}")
        pr_url = str(detail_body.get("html_url") or pr_url or f"https://github.com/{repo_slug}/pull/{pr_number}")
        pr_head = detail_body.get("head")
        pr_head_sha = str(pr_head.get("sha") or "") if isinstance(pr_head, dict) else ""
        pr_head_ref = str(pr_head.get("ref") or "") if isinstance(pr_head, dict) else ""
        if pr_head_sha != sha:
            raise ValueError(f"PR #{pr_number} head is {pr_head_sha[:12] or 'unknown'}, not local HEAD {sha[:12]}")
        if pr_head_ref and pr_head_ref != branch:
            raise ValueError(f"PR #{pr_number} head branch is {pr_head_ref!r}, not local branch {branch!r}")

        if detail_body.get("draft") is True:
            if not mark_ready:
                raise ValueError(f"PR #{pr_number} is still draft; pass mark_ready=true or mark it ready before merging")
            node_id = str(detail_body.get("node_id") or "").strip()
            if not node_id:
                raise ValueError(f"PR #{pr_number} is draft and GitHub did not return node_id for ready-for-review")
            ready_payload = {
                "event_id": f"tank-merge-ready-start-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "invocation_id": invocation_id,
                "source_service": "mcp-tank-operator",
                "source_tool": _TANK_MERGE_TOOL,
                "action": "github.pull_request.ready_for_review",
                "status": "started",
                "target_kind": "github_pull_request",
                "target_ref": pr_url,
                "repo_owner": owner,
                "repo_name": repo,
                "pr_number": pr_number,
                "result_sha": sha,
                "payload": {"branch": branch},
            }
            await _post_tank_control_action(http, headers, ready_payload)
            await _mark_pull_request_ready_for_review(http, github_token, node_id)
            await _post_tank_control_action(
                http,
                headers,
                ready_payload | {"event_id": f"tank-merge-ready-succeeded-{ORIGIN_SESSION_ID}-{uuid4().hex}", "status": "succeeded"},
            )

        merge_payload = {
            "event_id": f"tank-merge-start-{ORIGIN_SESSION_ID}-{uuid4().hex}",
            "invocation_id": invocation_id,
            "source_service": "mcp-tank-operator",
            "source_tool": _TANK_MERGE_TOOL,
            "action": "github.pull_request.merge",
            "status": "started",
            "target_kind": "github_pull_request",
            "target_ref": pr_url,
            "repo_owner": owner,
            "repo_name": repo,
            "pr_number": pr_number,
            "result_sha": sha,
            "payload": {"branch": branch, "merge_method": merge_method or "", "head_sha": sha},
        }
        await _post_tank_control_action(http, headers, merge_payload)
        merge_request: dict[str, object] = {"sha": sha}
        if merge_method:
            merge_request["merge_method"] = merge_method
        for arg_name, request_name in (("commit_title", "commit_title"), ("commit_message", "commit_message")):
            value = str(arguments.get(arg_name) or "").strip()
            if value:
                merge_request[request_name] = value
        merge_status, merge_body = await _github_api_json(
            http,
            github_token,
            "PUT",
            f"/repos/{owner}/{repo}/pulls/{pr_number}/merge",
            json_body=merge_request,
        )
        if merge_status >= 400 or not isinstance(merge_body, dict) or merge_body.get("merged") is not True:
            message = ""
            if isinstance(merge_body, dict):
                message = str(merge_body.get("message") or merge_body.get("raw") or "")
            raise RuntimeError(f"GitHub merge failed with HTTP {merge_status}: {message or merge_body!r}")
        merge_sha = str(merge_body.get("sha") or "")
        await _post_tank_control_action(
            http,
            headers,
            merge_payload
            | {
                "event_id": f"tank-merge-succeeded-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "status": "succeeded",
                "result_sha": merge_sha or sha,
                "payload": merge_payload["payload"] | {"merge_sha": merge_sha},
            },
        )
        text = (
            f"Merged governed PR #{pr_number} for {repo_slug}.\n"
            f"Branch: {branch}\n"
            f"Head: {sha[:12]}\n"
            f"PR: {pr_url}"
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": {
                    "repo": repo_slug,
                    "branch": branch,
                    "sha": sha,
                    "pr_number": pr_number,
                    "pr_url": pr_url,
                    "merge_sha": merge_sha,
                    "merged": True,
                },
            },
        )
    except Exception as exc:
        log.warning("Tank merge_current_session_pr failed", exc_info=True)
        if service_token and owner and repo:
            try:
                await _post_tank_control_action(
                    http,
                    headers or {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
                    {
                        "event_id": f"tank-merge-failed-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                        "invocation_id": invocation_id,
                        "source_service": "mcp-tank-operator",
                        "source_tool": _TANK_MERGE_TOOL,
                        "action": "github.pull_request.merge",
                        "status": "failed",
                        "target_kind": "github_pull_request" if pr_url else "github_repository",
                        "target_ref": pr_url or f"https://github.com/{owner}/{repo}",
                        "repo_owner": owner,
                        "repo_name": repo,
                        "pr_number": pr_number,
                        "result_sha": sha,
                        "error": str(exc),
                        "payload": {"branch": branch, "repo_path": str(arguments.get("repo_path") or "")},
                    },
                )
            except Exception:
                log.warning("failed to record governed merge failure", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32015,
            str(exc),
            {"tool": _TANK_MERGE_TOOL, "invocation_id": invocation_id},
        )


async def _handle_tank_rename_pr_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-rename-pr-{uuid4().hex}"
    service_token = ""
    headers: dict[str, str] = {}
    owner = ""
    repo = ""
    repo_slug = ""
    branch = ""
    sha = ""
    pr_number: int | None = None
    pr_url = ""
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for governed PR rename")
        title = str(arguments.get("title") or "").strip()
        if not title:
            raise ValueError("title is required")
        if len(title) > 256:
            raise ValueError("title must be 256 characters or fewer")
        repo_path = _repo_path_from_arguments(arguments)
        git_dir = await _git_output(repo_path, "rev-parse", "--absolute-git-dir")
        if not git_dir:
            raise ValueError(f"{repo_path} is not a git repository")
        branch = await _git_output(repo_path, "branch", "--show-current")
        if not branch:
            raise ValueError("current repo is detached; checkout the Tank session branch before renaming its PR")
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
        repo_slug = f"{owner}/{repo}"
        requested_repo = str(arguments.get("repo") or "").strip()
        if requested_repo and requested_repo != repo_slug:
            raise ValueError(f"repo argument {requested_repo!r} does not match origin {repo_slug}")
        if not _is_tank_session_branch(branch, repo):
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {_default_tank_session_branch(repo)!r} or one of its approved lanes")
        sha = await _git_output(repo_path, "rev-parse", "HEAD")

        raw_pr_number = arguments.get("pr_number")
        requested_pr_number: int | None = None
        if raw_pr_number not in (None, ""):
            try:
                requested_pr_number = int(raw_pr_number)
            except (TypeError, ValueError) as exc:
                raise ValueError("pr_number must be an integer") from exc
            if requested_pr_number <= 0:
                raise ValueError("pr_number must be positive")

        service_token = await auth_romaine_provider.token()
        headers = {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"}
        github_token = await _mint_github_installation_token(http, service_token, repo_slug, full=True)
        head = quote(f"{owner}:{branch}", safe="")
        pr_status, pr_body = await _github_api_json(
            http,
            github_token,
            "GET",
            f"/repos/{owner}/{repo}/pulls?head={head}&state=open",
        )
        if pr_status >= 400 or not isinstance(pr_body, list) or not pr_body:
            raise ValueError(f"no open PR exists for {owner}:{branch}")
        pr = pr_body[0] if isinstance(pr_body[0], dict) else {}
        pr_number = int(pr.get("number") or 0) or None
        pr_url = str(pr.get("html_url") or "")
        if pr_number is None:
            raise ValueError("open PR response did not include a PR number")
        if requested_pr_number is not None and pr_number != requested_pr_number:
            raise ValueError(f"requested PR #{requested_pr_number} does not match governed branch PR #{pr_number}")
        pr_head = pr.get("head")
        pr_head_ref = str(pr_head.get("ref") or "") if isinstance(pr_head, dict) else ""
        if pr_head_ref and pr_head_ref != branch:
            raise ValueError(f"PR #{pr_number} head branch is {pr_head_ref!r}, not local branch {branch!r}")

        started_payload = {
            "event_id": f"tank-rename-pr-start-{ORIGIN_SESSION_ID}-{uuid4().hex}",
            "invocation_id": invocation_id,
            "source_service": "mcp-tank-operator",
            "source_tool": _TANK_RENAME_PR_TOOL,
            "action": "github.pull_request.rename",
            "status": "started",
            "target_kind": "github_pull_request",
            "target_ref": pr_url or f"https://github.com/{repo_slug}/pull/{pr_number}",
            "repo_owner": owner,
            "repo_name": repo,
            "pr_number": pr_number,
            "result_sha": sha,
            "payload": {"branch": branch, "title": title},
        }
        await _post_tank_control_action(http, headers, started_payload)
        update_status, update_body = await _github_api_json(
            http,
            github_token,
            "PATCH",
            f"/repos/{owner}/{repo}/issues/{pr_number}",
            json_body={"title": title},
        )
        if update_status >= 400 or not isinstance(update_body, dict):
            raise RuntimeError(f"GitHub PR rename failed with HTTP {update_status}: {update_body!r}")
        pr_url = str(update_body.get("html_url") or pr_url or f"https://github.com/{repo_slug}/pull/{pr_number}")
        await _post_tank_control_action(
            http,
            headers,
            started_payload
            | {
                "event_id": f"tank-rename-pr-succeeded-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "status": "succeeded",
                "target_ref": pr_url,
            },
        )
        text = (
            f"Renamed governed PR #{pr_number} for {repo_slug}.\n"
            f"Branch: {branch}\n"
            f"Title: {title}\n"
            f"PR: {pr_url}"
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": {
                    "repo": repo_slug,
                    "branch": branch,
                    "sha": sha,
                    "pr_number": pr_number,
                    "pr_url": pr_url,
                    "title": title,
                },
            },
        )
    except Exception as exc:
        log.warning("Tank rename_current_session_pr failed", exc_info=True)
        if service_token and owner and repo:
            try:
                await _post_tank_control_action(
                    http,
                    headers or {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
                    {
                        "event_id": f"tank-rename-pr-failed-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                        "invocation_id": invocation_id,
                        "source_service": "mcp-tank-operator",
                        "source_tool": _TANK_RENAME_PR_TOOL,
                        "action": "github.pull_request.rename",
                        "status": "failed",
                        "target_kind": "github_pull_request" if pr_url else "github_repository",
                        "target_ref": pr_url or f"https://github.com/{owner}/{repo}",
                        "repo_owner": owner,
                        "repo_name": repo,
                        "pr_number": pr_number,
                        "result_sha": sha,
                        "error": str(exc),
                        "payload": {"branch": branch, "title": str(arguments.get("title") or ""), "repo_path": str(arguments.get("repo_path") or "")},
                    },
                )
            except Exception:
                log.warning("failed to record governed PR rename failure", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32016,
            str(exc),
            {"tool": _TANK_RENAME_PR_TOOL, "invocation_id": invocation_id},
        )


# GitHub caps issue/PR bodies at 65536 characters.
_GITHUB_PR_BODY_MAX = 65536


def _feature_contracts_body_status(body: str) -> tuple[bool, list[str]]:
    """Advisory preview of the check-pr-body result for a PR body.

    Mirrors `.github/workflows/pr-feature-contracts.yml` so
    `update_current_session_pr_body` can tell the caller whether the body it
    just set will satisfy the `check-pr-body` status check, without waiting for
    a CI round-trip. This is advisory only; the GitHub workflow remains the
    authoritative gate. Keep this in sync with that workflow.
    """
    text = body or ""
    missing: list[str] = []
    for marker in ("## Feature Contracts", "Affected contracts:", "Evidence:"):
        if marker not in text:
            missing.append(f"missing required marker {marker!r}")
    affected_match = re.search(r"Affected contracts:\s*([\s\S]*?)\n\s*Evidence:", text, re.IGNORECASE)
    affected = affected_match.group(1) if affected_match else ""
    evidence_match = re.search(r"Evidence:\s*([\s\S]*)", text, re.IGNORECASE)
    evidence = evidence_match.group(1) if evidence_match else ""
    if not re.search(r"- \[[xX]\] ", affected):
        missing.append("no affected contract is checked; use '- [x]', including None when no contract applies")
    evidence_lines = [
        line.strip()
        for line in re.split(r"\r?\n", evidence)
        if line.strip() and line.strip() != "-" and not line.strip().startswith("<!--")
    ]
    if not evidence_lines:
        missing.append("Evidence section has no concrete text")
    return (not missing, missing)


async def _handle_tank_update_pr_body_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-update-pr-body-{uuid4().hex}"
    service_token = ""
    headers: dict[str, str] = {}
    owner = ""
    repo = ""
    repo_slug = ""
    branch = ""
    sha = ""
    pr_number: int | None = None
    pr_url = ""
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for governed PR body update")
        body_text = str(arguments.get("body") or "")
        if not body_text.strip():
            raise ValueError("body is required")
        if len(body_text) > _GITHUB_PR_BODY_MAX:
            raise ValueError(f"body must be {_GITHUB_PR_BODY_MAX} characters or fewer")
        contracts_ready, contracts_missing = _feature_contracts_body_status(body_text)
        repo_path = _repo_path_from_arguments(arguments)
        git_dir = await _git_output(repo_path, "rev-parse", "--absolute-git-dir")
        if not git_dir:
            raise ValueError(f"{repo_path} is not a git repository")
        branch = await _git_output(repo_path, "branch", "--show-current")
        if not branch:
            raise ValueError("current repo is detached; checkout the Tank session branch before updating its PR body")
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
        repo_slug = f"{owner}/{repo}"
        requested_repo = str(arguments.get("repo") or "").strip()
        if requested_repo and requested_repo != repo_slug:
            raise ValueError(f"repo argument {requested_repo!r} does not match origin {repo_slug}")
        if not _is_tank_session_branch(branch, repo):
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {_default_tank_session_branch(repo)!r} or one of its approved lanes")
        sha = await _git_output(repo_path, "rev-parse", "HEAD")

        raw_pr_number = arguments.get("pr_number")
        requested_pr_number: int | None = None
        if raw_pr_number not in (None, ""):
            try:
                requested_pr_number = int(raw_pr_number)
            except (TypeError, ValueError) as exc:
                raise ValueError("pr_number must be an integer") from exc
            if requested_pr_number <= 0:
                raise ValueError("pr_number must be positive")

        service_token = await auth_romaine_provider.token()
        headers = {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"}
        github_token = await _mint_github_installation_token(http, service_token, repo_slug, full=True)
        head = quote(f"{owner}:{branch}", safe="")
        pr_status, pr_body = await _github_api_json(
            http,
            github_token,
            "GET",
            f"/repos/{owner}/{repo}/pulls?head={head}&state=open",
        )
        if pr_status >= 400 or not isinstance(pr_body, list) or not pr_body:
            raise ValueError(f"no open PR exists for {owner}:{branch}")
        pr = pr_body[0] if isinstance(pr_body[0], dict) else {}
        pr_number = int(pr.get("number") or 0) or None
        pr_url = str(pr.get("html_url") or "")
        if pr_number is None:
            raise ValueError("open PR response did not include a PR number")
        if requested_pr_number is not None and pr_number != requested_pr_number:
            raise ValueError(f"requested PR #{requested_pr_number} does not match governed branch PR #{pr_number}")
        pr_head = pr.get("head")
        pr_head_ref = str(pr_head.get("ref") or "") if isinstance(pr_head, dict) else ""
        if pr_head_ref and pr_head_ref != branch:
            raise ValueError(f"PR #{pr_number} head branch is {pr_head_ref!r}, not local branch {branch!r}")

        # The governed ledger payload must stay under the backend's 16 KiB
        # control-action cap, so record body metadata (length + contract
        # readiness) rather than the full body text.
        started_payload = {
            "event_id": f"tank-update-pr-body-start-{ORIGIN_SESSION_ID}-{uuid4().hex}",
            "invocation_id": invocation_id,
            "source_service": "mcp-tank-operator",
            "source_tool": _TANK_UPDATE_PR_BODY_TOOL,
            "action": "github.pull_request.update_body",
            "status": "started",
            "target_kind": "github_pull_request",
            "target_ref": pr_url or f"https://github.com/{repo_slug}/pull/{pr_number}",
            "repo_owner": owner,
            "repo_name": repo,
            "pr_number": pr_number,
            "result_sha": sha,
            "payload": {
                "branch": branch,
                "body_length": len(body_text),
                "feature_contracts_ready": contracts_ready,
            },
        }
        await _post_tank_control_action(http, headers, started_payload)
        update_status, update_body = await _github_api_json(
            http,
            github_token,
            "PATCH",
            f"/repos/{owner}/{repo}/issues/{pr_number}",
            json_body={"body": body_text},
        )
        if update_status >= 400 or not isinstance(update_body, dict):
            raise RuntimeError(f"GitHub PR body update failed with HTTP {update_status}: {update_body!r}")
        pr_url = str(update_body.get("html_url") or pr_url or f"https://github.com/{repo_slug}/pull/{pr_number}")
        await _post_tank_control_action(
            http,
            headers,
            started_payload
            | {
                "event_id": f"tank-update-pr-body-succeeded-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "status": "succeeded",
                "target_ref": pr_url,
            },
        )
        if contracts_ready:
            contracts_line = "Feature Contracts: ready for check-pr-body."
        else:
            contracts_line = "Feature Contracts: NOT ready for check-pr-body -> " + "; ".join(contracts_missing)
        text = (
            f"Updated governed PR #{pr_number} body for {repo_slug}.\n"
            f"Branch: {branch}\n"
            f"Body length: {len(body_text)} characters\n"
            f"{contracts_line}\n"
            f"PR: {pr_url}"
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": {
                    "repo": repo_slug,
                    "branch": branch,
                    "sha": sha,
                    "pr_number": pr_number,
                    "pr_url": pr_url,
                    "body_length": len(body_text),
                    "feature_contracts_ready": contracts_ready,
                    "feature_contracts_missing": contracts_missing,
                },
            },
        )
    except Exception as exc:
        log.warning("Tank update_current_session_pr_body failed", exc_info=True)
        if service_token and owner and repo:
            try:
                await _post_tank_control_action(
                    http,
                    headers or {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
                    {
                        "event_id": f"tank-update-pr-body-failed-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                        "invocation_id": invocation_id,
                        "source_service": "mcp-tank-operator",
                        "source_tool": _TANK_UPDATE_PR_BODY_TOOL,
                        "action": "github.pull_request.update_body",
                        "status": "failed",
                        "target_kind": "github_pull_request" if pr_url else "github_repository",
                        "target_ref": pr_url or f"https://github.com/{owner}/{repo}",
                        "repo_owner": owner,
                        "repo_name": repo,
                        "pr_number": pr_number,
                        "result_sha": sha,
                        "error": str(exc),
                        "payload": {"branch": branch, "repo_path": str(arguments.get("repo_path") or "")},
                    },
                )
            except Exception:
                log.warning("failed to record governed PR body update failure", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32017,
            str(exc),
            {"tool": _TANK_UPDATE_PR_BODY_TOOL, "invocation_id": invocation_id},
        )


async def _prepare_glimmung_hot_swap_call(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    body: bytes,
    arguments: dict,
) -> tuple[bytes, dict] | web.Response:
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for governed hot-swap")
        repo_path = _repo_path_for_hot_swap(arguments)
        git_dir = await _git_output(repo_path, "rev-parse", "--absolute-git-dir")
        if not git_dir:
            raise ValueError(f"{repo_path} is not a git repository")
        branch = await _git_output(repo_path, "branch", "--show-current")
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
        if not _is_tank_session_branch(branch, repo):
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {_default_tank_session_branch(repo)!r} or one of its approved lanes")
        rc, stdout, stderr = await _run_git(repo_path, "status", "--porcelain")
        if rc != 0:
            raise RuntimeError(stderr or stdout or "git status failed")
        if stdout.strip():
            raise ValueError("worktree has uncommitted changes; commit and publish before hot-swapping")
        sha = await _git_output(repo_path, "rev-parse", "HEAD")
        requested_ref = str(arguments.get("git_ref") or "").strip()
        allowed_requested_refs = {sha, branch, f"refs/heads/{branch}", "HEAD"}
        if requested_ref and requested_ref not in allowed_requested_refs:
            raise ValueError(f"git_ref {requested_ref!r} does not match local governed branch HEAD {sha[:12]}")
        requested_repo = str(arguments.get("repo") or "").strip()
        if requested_repo and requested_repo != f"{owner}/{repo}":
            raise ValueError(f"repo argument {requested_repo!r} does not match origin {owner}/{repo}")

        service_token = await auth_romaine_provider.token()
        github_token = await _mint_github_installation_token(http, service_token, f"{owner}/{repo}")
        tank_verify = await _post_tank_hot_swap_verify(
            http,
            service_token,
            {
                "repo": f"{owner}/{repo}",
                "branch": branch,
                "sha": sha,
                "artifact_kind": str(arguments.get("artifact_kind") or ""),
                "validation_target": str(arguments.get("validation_target") or ""),
                "source_tool": _GLIMMUNG_HOT_SWAP_TOOL,
            },
        )
        live_verify = await _verify_github_hot_swap_head(http, github_token, owner, repo, branch, sha)
        reasons: list[str] = []
        if tank_verify.get("allowed") is not True:
            tank_reasons = tank_verify.get("reasons")
            if isinstance(tank_reasons, list):
                reasons.extend(f"Tank ledger: {reason}" for reason in tank_reasons)
            else:
                reasons.append("Tank ledger did not confirm governed publish, green CI, and clean mergeability")
        if live_verify.get("allowed") is not True:
            live_reasons = live_verify.get("reasons")
            if isinstance(live_reasons, list):
                reasons.extend(f"GitHub live state: {reason}" for reason in live_reasons)
            else:
                reasons.append("GitHub live state did not confirm latest branch head, open PR, green CI, and clean mergeability")
        if reasons:
            raise ValueError("hot-swap blocked:\n- " + "\n- ".join(reasons))

        forwarded_arguments = {
            key: value
            for key, value in arguments.items()
            if key not in {"repo", "repo_path"}
        }
        forwarded_arguments["git_ref"] = sha
        return _rewrite_mcp_tool_arguments(body, forwarded_arguments), {
            "repo": f"{owner}/{repo}",
            "branch": branch,
            "sha": sha,
            "pr_number": live_verify.get("pr_number") or tank_verify.get("pr_number"),
            "artifact_kind": arguments.get("artifact_kind"),
            "validation_target": arguments.get("validation_target"),
        }
    except Exception as exc:
        log.warning("Glimmung hot-swap gate failed", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32030,
            str(exc),
            {
                "tool": _GLIMMUNG_HOT_SWAP_TOOL,
                "replacement_tool": _TANK_PUBLISH_TOOL,
                "break_glass_tool": _TANK_BREAK_GLASS_TOOL,
                "ci_enforcement": _CI_ENFORCEMENT_TEXT,
            },
        )


async def _handle_tank_break_glass_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-break-glass-{uuid4().hex}"
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for Tank break-glass requests")
        repo_scope, repo_slug, requested_repos, repo_path = await _repo_scope_from_arguments(arguments)
        branch_scope = _branch_scope_from_arguments(arguments)
        reason = str(arguments.get("reason") or "").strip()
        if not reason:
            raise ValueError("reason is required")
        if len(reason) > 400:
            reason = reason[:400]
        source = str(arguments.get("source") or "agent").strip() or "agent"
        owner, repo = repo_slug.split("/", 1) if repo_slug else ("", "")
        service_token = await auth_romaine_provider.token()
        grant = await _active_break_glass_grant(http, service_token, repo_slug)
        if grant and not _grant_covers_branch_scope(grant, branch_scope):
            grant = None
        activation = _activate_break_glass_mcp_config(repo_slug, grant) if grant else None
        approval_url = _break_glass_approval_url(
            ORIGIN_SESSION_ID,
            repo_scope,
            branch_scope,
            reason,
            source,
        )
        target_ref = f"https://github.com/{repo_slug}"
        if repo_scope.get("kind") == "all_repos":
            target_ref = f"tank://session/{ORIGIN_SESSION_ID}/git-break-glass/all-repos"
        elif repo_scope.get("kind") == "repos":
            target_ref = f"tank://session/{ORIGIN_SESSION_ID}/git-break-glass/repos"
        await _post_tank_control_action(
            http,
            {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
            {
                "event_id": f"tank-break-glass-request-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "invocation_id": invocation_id,
                "source_service": "mcp-tank-operator",
                "source_tool": _TANK_BREAK_GLASS_TOOL,
                "action": "github.break_glass.request",
                "status": "started",
                "target_kind": "github_repository",
                "target_ref": target_ref,
                "repo_owner": owner,
                "repo_name": repo,
                "payload": {
                    "approval_url": approval_url,
                    "reason": reason,
                    "source": source,
                    "repo_scope": repo_scope,
                    "branch_scope": branch_scope,
                    "repo_path": str(repo_path) if repo_path is not None else "",
                },
            },
        )
        if activation:
            text = (
                "Break-glass GitHub access is approved and the separate MCP server was activated.\n"
                f"Server: {_BREAK_GLASS_MCP_SERVER_NAME} at {activation['server_url']}\n"
                f"Grant expires: {activation.get('expires_at')}\n"
                "Reload or restart the agent MCP registry before using the new server if it does not appear automatically."
            )
            structured = {
                "repo_scope": repo_scope,
                "branch_scope": branch_scope,
                "status": "approved",
                "approval_url": approval_url,
                "privileged_tools_visible": True,
                "activation": activation,
            }
        else:
            text = (
                "Break-glass GitHub access request recorded.\n"
                f"Approval URL: {approval_url}\n"
                "This tool did not mint or reveal a GitHub token. Until approval is "
                "completed, keep using publish_current_head and do not attempt raw "
                "GitHub writes."
            )
            structured = {
                "repo_scope": repo_scope,
                "branch_scope": branch_scope,
                "approval_url": approval_url,
                "status": "approval_required",
                "privileged_tools_visible": False,
            }
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": structured,
            },
        )
    except Exception as exc:
        log.warning("Tank request_git_break_glass failed", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32012,
            str(exc),
            {"tool": _TANK_BREAK_GLASS_TOOL, "invocation_id": invocation_id},
        )


async def _handle_tank_azure_break_glass_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    # Records the request and returns an auth.romaine.life approval URL; it never
    # mints or reveals a token, and it no longer writes any MCP config. Surfacing
    # is now automatic (B-auto): on approval the orchestrator enqueues an approval
    # turn carrying the azure-personal MCP-activation payload, and the pod-side
    # runner adds the server + rebuilds at the next idle boundary — so the tools
    # appear without a second call to this tool. mcp-azure-personal re-checks the
    # grant on every call, so it stays the real boundary.
    invocation_id = f"tank-azure-break-glass-{uuid4().hex}"
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for Tank break-glass requests")
        reason = str(arguments.get("reason") or "").strip()
        if len(reason) > 400:
            reason = reason[:400]
        source = str(arguments.get("source") or "agent").strip() or "agent"
        service_token = await auth_romaine_provider.token()
        grant = await _active_azure_break_glass_grant(http, service_token)
        approval_url = _azure_break_glass_approval_url(ORIGIN_SESSION_ID, reason, source)
        await _post_tank_control_action(
            http,
            {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
            {
                "event_id": f"tank-azure-break-glass-request-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "invocation_id": invocation_id,
                "source_service": "mcp-tank-operator",
                "source_tool": _TANK_AZURE_BREAK_GLASS_TOOL,
                "action": "azure.break_glass.request",
                "status": "started",
                "target_kind": "azure_mcp",
                "target_ref": "azure-personal",
                "payload": {
                    "approval_url": approval_url,
                    "reason": reason,
                    "source": source,
                },
            },
        )
        if grant:
            text = (
                "Azure break-glass access is already approved and active; the "
                "azure-personal tools surface automatically for this session "
                "(Tank sends an approval turn that activates them — no re-request "
                "needed).\n"
                f"Grant expires: {grant.get('expires_at')}"
            )
            structured = {
                "resource": "azure-personal",
                "status": "approved",
                "approval_url": approval_url,
                "expires_at": grant.get("expires_at"),
                "privileged_tools_visible": True,
            }
        else:
            text = (
                "Break-glass azure-personal access request recorded.\n"
                f"Approval URL: {approval_url}\n"
                "This tool did not grant access or reveal a token. Once an admin "
                "approves, Tank surfaces the azure-personal tools into this session "
                "automatically — you do not need to call this tool again. Normal "
                "feature work does not need azure access."
            )
            structured = {
                "resource": "azure-personal",
                "approval_url": approval_url,
                "status": "approval_required",
                "privileged_tools_visible": False,
            }
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": structured,
            },
        )
    except Exception as exc:
        log.warning("Tank request_azure_break_glass failed", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32012,
            str(exc),
            {"tool": _TANK_AZURE_BREAK_GLASS_TOOL, "invocation_id": invocation_id},
        )


async def _handle_tank_pr_lane_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-pr-lane-{uuid4().hex}"
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for Tank PR lane requests")
        repo_scope, repo_slug, requested_repos, repo_path = await _repo_scope_from_arguments(arguments)
        lane_name = _sanitize_pr_lane_name(arguments.get("lane_name"))
        branch_scope = _branch_scope_from_arguments(arguments) if not lane_name else None
        allocation_request = not lane_name
        if not lane_name and not allocation_request:
            raise ValueError("lane_name or branch_scope is required")
        if not allocation_request and repo_scope.get("kind") != "current_repo":
            raise ValueError("multi-repo PR lane requests must be allocation requests")
        if len(lane_name) > 64:
            lane_name = lane_name[:64].strip("-._")
        relationship = str(arguments.get("relationship") or ("parallel" if allocation_request else "")).strip().lower()
        if not allocation_request and relationship not in {"parallel", "stacked", "followup"}:
            raise ValueError("relationship must be one of parallel, stacked, or followup")
        if allocation_request and relationship and relationship not in {"parallel", "stacked", "followup"}:
            raise ValueError("relationship must be one of parallel, stacked, or followup")
        reason = str(arguments.get("reason") or "").strip()
        if not reason:
            raise ValueError("reason is required")
        if len(reason) > 800:
            reason = reason[:800]
        scope = str(arguments.get("scope") or "").strip()
        if len(scope) > 800:
            scope = scope[:800]
        base = str(arguments.get("base") or "main").strip() or "main"
        if len(base) > 200:
            base = base[:200]

        owner, repo = repo_slug.split("/", 1) if repo_slug and repo_scope.get("kind") == "current_repo" else ("", "")
        service_token = await auth_romaine_provider.token()
        if allocation_request:
            lane_names = branch_scope.get("branches", []) if branch_scope and branch_scope.get("kind") == "named" else []
            requested_count = branch_scope.get("count", 0) if branch_scope and branch_scope.get("kind") == "count" else 0
            unlimited = bool(branch_scope and branch_scope.get("kind") == "unlimited")
            proposed_branches = (
                [f"tank/session/{ORIGIN_SESSION_ID}/{repo}/{name}" for name in lane_names]
                if repo
                else []
            )
            event_id = f"tank-pr-lane-request-{ORIGIN_SESSION_ID}-{uuid4().hex}"
            target_ref = f"https://github.com/{repo_slug}"
            if repo_scope.get("kind") == "all_repos":
                target_ref = f"tank://session/{ORIGIN_SESSION_ID}/pr-lanes/all-repos"
            elif repo_scope.get("kind") == "repos":
                target_ref = f"tank://session/{ORIGIN_SESSION_ID}/pr-lanes/repos"
            await _post_tank_control_action(
                http,
                {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
                {
                    "event_id": event_id,
                    "invocation_id": invocation_id,
                    "source_service": "mcp-tank-operator",
                    "source_tool": _TANK_PR_LANE_TOOL,
                    "action": "github.pr_lane.request",
                    "status": "started",
                    "target_kind": "github_repository",
                    "target_ref": target_ref,
                    "repo_owner": owner,
                    "repo_name": repo,
                    "payload": {
                        "allocation_request": True,
                        "repo_scope": repo_scope,
                        "branch_scope": branch_scope,
                        "relationship": relationship,
                        "base": base,
                        "scope": scope,
                        "reason": reason,
                        "repo_path": str(repo_path) if repo_path is not None else "",
                    },
                },
            )
            approval_url = _pr_lane_approval_url(ORIGIN_SESSION_ID, event_id)
            if unlimited:
                allocation_text = "unlimited governed PR lanes"
            elif lane_names:
                allocation_text = f"{len(lane_names)} named governed PR lane{'s' if len(lane_names) != 1 else ''}"
            else:
                allocation_text = f"{requested_count} governed PR lane{'s' if requested_count != 1 else ''}"
            repo_text = "all repos" if repo_scope.get("kind") == "all_repos" else ", ".join(requested_repos)
            text = (
                f"PR lane allocation request recorded for {repo_text}: {allocation_text}.\n"
                f"Approval URL: {approval_url}"
            )
            return _mcp_result_response(
                request_id,
                {
                    "content": [{"type": "text", "text": text}],
                    "structuredContent": {
                        "request_event_id": event_id,
                        "repo_scope": repo_scope,
                        "branch_scope": branch_scope,
                        "allocation_request": True,
                        "relationship": relationship,
                        "base": base,
                        "scope": scope,
                        "reason": reason,
                        "approval_url": approval_url,
                        "status": "approval_required",
                    },
                },
            )

        proposed_branch = f"tank/session/{ORIGIN_SESSION_ID}/{repo}/{lane_name}"
        auto_approval = await _active_pr_lane_auto_approval(
            http,
            service_token,
            repo_slug,
            lane_name=lane_name,
            proposed_branch=proposed_branch,
        )
        auto_approved = auto_approval is not None
        status = "succeeded" if auto_approved else "started"
        event_id = f"tank-pr-lane-request-{ORIGIN_SESSION_ID}-{uuid4().hex}"
        approval_url = _pr_lane_approval_url(ORIGIN_SESSION_ID, event_id)
        await _post_tank_control_action(
            http,
            {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
            {
                "event_id": event_id,
                "invocation_id": invocation_id,
                "source_service": "mcp-tank-operator",
                "source_tool": _TANK_PR_LANE_TOOL,
                "action": "github.pr_lane.request",
                "status": status,
                "target_kind": "github_repository",
                "target_ref": f"https://github.com/{repo_slug}",
                "repo_owner": owner,
                "repo_name": repo,
                "payload": {
                    "lane_name": lane_name,
                    "relationship": relationship,
                    "base": base,
                    "scope": scope,
                    "reason": reason,
                    "proposed_branch": proposed_branch,
                    "repo_path": str(repo_path) if repo_path is not None else "",
                    "auto_approved": auto_approved,
                    "auto_approval_event_id": (auto_approval or {}).get("event_id", ""),
                },
            },
        )
        if auto_approved:
            text = (
                f"PR lane request for {repo_slug}/{lane_name} is auto-approved for this session.\n"
                f"Proposed branch: {proposed_branch}\n"
                "This did not create the branch or pull request yet; use the governed lane creation path when available."
            )
            structured_status = "approved"
        else:
            text = (
                f"PR lane request recorded for {repo_slug}/{lane_name}.\n"
                f"Approval URL: {approval_url}"
            )
            structured_status = "approval_required"
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": {
                    "request_event_id": event_id,
                    "repo": repo_slug,
                    "lane_name": lane_name,
                    "relationship": relationship,
                    "base": base,
                    "scope": scope,
                    "reason": reason,
                    "proposed_branch": proposed_branch,
                    "approval_url": approval_url,
                    "status": structured_status,
                    "auto_approved": auto_approved,
                    "auto_approval_event_id": (auto_approval or {}).get("event_id", ""),
                },
            },
        )
    except Exception as exc:
        log.warning("Tank request_pr_lane failed", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32013,
            str(exc),
            {"tool": _TANK_PR_LANE_TOOL, "invocation_id": invocation_id},
        )


async def _handle_tank_create_pr_lane_tool(
    http: ClientSession,
    auth_romaine_provider,
    request_id: object,
    arguments: dict,
) -> web.Response:
    invocation_id = f"tank-pr-lane-create-{uuid4().hex}"
    service_token = ""
    repo_slug = ""
    owner = ""
    repo = ""
    branch = ""
    worktree_path: Path | None = None
    request_event_id = str(arguments.get("request_event_id") or "").strip()
    try:
        if not ORIGIN_SESSION_ID:
            raise ValueError("SESSION_ID is required for Tank PR lane creation")
        if not request_event_id:
            raise ValueError("request_event_id is required")
        service_token = await auth_romaine_provider.token()
        authorization = await _get_tank_pr_lane_authorization(http, service_token, request_event_id)
        if authorization.get("allowed") is not True:
            reasons = authorization.get("reasons")
            if isinstance(reasons, list) and reasons:
                raise ValueError("PR lane request is not approved: " + "; ".join(str(reason) for reason in reasons))
            raise ValueError("PR lane request is not approved")

        repo_slug = str(authorization.get("repo") or "").strip()
        if "/" not in repo_slug:
            raise ValueError("PR lane authorization did not include a GitHub repo slug")
        owner, repo = repo_slug.split("/", 1)
        lane_name = str(authorization.get("lane_name") or "").strip()
        if not lane_name:
            raise ValueError("PR lane authorization did not include a lane name")
        branch = str(authorization.get("proposed_branch") or "").strip()
        expected_prefix = f"tank/session/{ORIGIN_SESSION_ID}/{repo}/"
        if not branch.startswith(expected_prefix):
            raise ValueError(f"authorized branch {branch!r} is outside Tank session lane prefix {expected_prefix!r}")
        base = str(authorization.get("base") or "main").strip() or "main"
        relationship = str(authorization.get("relationship") or "").strip()
        scope = str(authorization.get("scope") or "").strip()
        reason = str(authorization.get("reason") or "").strip()
        approval_event_id = str(authorization.get("approval_event_id") or "").strip()

        repo_path_args = {"repo": repo_slug}
        if arguments.get("repo_path"):
            repo_path_args["repo_path"] = arguments.get("repo_path")
        source_repo_path = _repo_path_from_arguments(repo_path_args)
        remote_url = await _git_output(source_repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        if f"{slug[0]}/{slug[1]}" != repo_slug:
            raise ValueError(f"repo path origin {slug[0]}/{slug[1]} does not match approved repo {repo_slug}")

        worktree_path = _pr_lane_worktree_path(repo_slug, lane_name)
        headers = {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"}
        started_payload = {
            "event_id": f"tank-pr-lane-create-start-{ORIGIN_SESSION_ID}-{uuid4().hex}",
            "invocation_id": invocation_id,
            "source_service": "mcp-tank-operator",
            "source_tool": _TANK_CREATE_PR_LANE_TOOL,
            "action": "github.pr_lane.create",
            "status": "started",
            "target_kind": "github_branch",
            "target_ref": f"https://github.com/{repo_slug}/tree/{quote(branch, safe='')}",
            "repo_owner": owner,
            "repo_name": repo,
            "payload": {
                "request_event_id": request_event_id,
                "approval_event_id": approval_event_id,
                "lane_name": lane_name,
                "relationship": relationship,
                "base": base,
                "scope": scope,
                "reason": reason,
                "branch": branch,
                "source_repo_path": str(source_repo_path),
                "worktree_path": str(worktree_path),
            },
        }
        await _post_tank_control_action(http, headers, started_payload)

        await _ensure_pr_lane_worktree(
            source_repo_path,
            branch=branch,
            base=base,
            lane_name=lane_name,
            worktree_path=worktree_path,
        )
        sha = await _git_output(worktree_path, "rev-parse", "HEAD")
        github_token = await _mint_github_installation_token(http, service_token, repo_slug)
        await _push_head_with_token(worktree_path, branch, github_token)

        pr_body_lines = [
            f"Tank session: {ORIGIN_SESSION_ID}",
            f"Lane: {lane_name}",
            f"Relationship: {relationship or 'unspecified'}",
            f"Scope: {scope or 'unspecified'}",
            "",
            reason or "Additional governed PR lane approved for this session.",
        ]
        pr_objects = await _call_mcp_github_tool(
            http,
            service_token,
            "create_pull_request",
            {
                "owner": owner,
                "name": repo,
                "title": f"Tank session {ORIGIN_SESSION_ID}: {lane_name}",
                "body": "\n".join(pr_body_lines),
                "head": branch,
                "base": base,
                "draft": True,
            },
        )
        pr = _first_pr_from_response(pr_objects)
        pr_url = ""
        pr_number: int | None = None
        if pr is not None:
            _pr_owner, _pr_repo, pr_url, pr_number = pr
            await _post_tank_control_action(
                http,
                headers,
                {
                    "event_id": f"tank-pr-lane-pr-open-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                    "invocation_id": invocation_id,
                    "source_service": "mcp-tank-operator",
                    "source_tool": _TANK_CREATE_PR_LANE_TOOL,
                    "action": "github.pull_request.open",
                    "status": "succeeded",
                    "target_kind": "github_pull_request",
                    "target_ref": pr_url,
                    "repo_owner": owner,
                    "repo_name": repo,
                    "pr_number": pr_number,
                    "result_sha": sha,
                    "payload": {
                        "request_event_id": request_event_id,
                        "lane_name": lane_name,
                        "branch": branch,
                        "base": base,
                        "draft": True,
                    },
                },
            )

        await _post_tank_control_action(
            http,
            headers,
            started_payload
            | {
                "event_id": f"tank-pr-lane-create-succeeded-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                "status": "succeeded",
                "target_kind": "github_pull_request" if pr_url else "github_branch",
                "target_ref": pr_url or f"https://github.com/{repo_slug}/tree/{quote(branch, safe='')}",
                "pr_number": pr_number,
                "result_sha": sha,
            },
        )
        asyncio.create_task(_watch_published_commit(http, auth_romaine_provider, owner, repo, branch, sha, invocation_id, source_tool=_TANK_CREATE_PR_LANE_TOOL))

        text = (
            f"Created governed PR lane {repo_slug}/{lane_name}.\n"
            f"Worktree: {worktree_path}\n"
            f"Branch: {branch}\n"
            + (f"Draft PR: {pr_url}\n" if pr_url else "Draft PR: not returned by mcp-github\n")
            + _CI_ENFORCEMENT_TEXT
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [{"type": "text", "text": text}],
                "structuredContent": {
                    "repo": repo_slug,
                    "lane_name": lane_name,
                    "branch": branch,
                    "base": base,
                    "worktree_path": str(worktree_path),
                    "sha": sha,
                    "pr_url": pr_url,
                    "pr_number": pr_number,
                    "request_event_id": request_event_id,
                    "approval_event_id": approval_event_id,
                    "ci_watch": "started",
                },
            },
        )
    except Exception as exc:
        log.warning("Tank create_pr_lane failed", exc_info=True)
        if service_token and repo_slug and owner and repo:
            try:
                await _post_tank_control_action(
                    http,
                    {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
                    {
                        "event_id": f"tank-pr-lane-create-failed-{ORIGIN_SESSION_ID}-{uuid4().hex}",
                        "invocation_id": invocation_id,
                        "source_service": "mcp-tank-operator",
                        "source_tool": _TANK_CREATE_PR_LANE_TOOL,
                        "action": "github.pr_lane.create",
                        "status": "failed",
                        "target_kind": "github_branch",
                        "target_ref": f"https://github.com/{repo_slug}/tree/{quote(branch, safe='')}" if branch else f"https://github.com/{repo_slug}",
                        "repo_owner": owner,
                        "repo_name": repo,
                        "error": str(exc),
                        "payload": {
                            "request_event_id": request_event_id,
                            "branch": branch,
                            "worktree_path": str(worktree_path) if worktree_path else "",
                        },
                    },
                )
            except Exception:
                log.warning("failed to record PR lane creation failure", exc_info=True)
        return _mcp_error_response(
            request_id,
            -32014,
            str(exc),
            {"tool": _TANK_CREATE_PR_LANE_TOOL, "invocation_id": invocation_id, "request_event_id": request_event_id},
        )


def _break_glass_tools_result() -> dict:
    return {
        "tools": [
            {
                "name": _BREAK_GLASS_MINT_TOKEN_TOOL,
                "description": (
                    "Mint an approved short-lived full GitHub git token for one repo. "
                    "Requires an active Tank break-glass grant and records an audit event."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo": {"type": "string", "description": "GitHub slug, for example romaine-life/tank-operator."},
                        "workflows": {"type": "boolean", "description": "Also request workflow-file write permission when approved."},
                        "reason": {"type": "string", "description": "Why the token is needed."},
                    },
                    "required": ["repo"],
                    "additionalProperties": False,
                },
            },
            {
                "name": _BREAK_GLASS_PUSH_HEAD_TOOL,
                "description": (
                    "Push the current HEAD of a workspace repo to its current branch using an "
                    "approved break-glass token. Requires an active Tank grant and records an audit event."
                ),
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "repo": {"type": "string", "description": "Optional GitHub slug; must match origin if repo_path is used."},
                        "repo_path": {"type": "string", "description": "Optional absolute or /workspace-relative path to the repo."},
                        "allow_dirty": {"type": "boolean", "description": "Allow pushing HEAD when the worktree has uncommitted changes."},
                        "reason": {"type": "string", "description": "Why governed publish is insufficient."},
                    },
                    "additionalProperties": False,
                },
            },
        ]
    }


async def _record_break_glass_use(
    http: ClientSession,
    service_token: str,
    *,
    action: str,
    status: str,
    tool: str,
    repo_slug: str,
    payload: dict,
    error: str = "",
    result_sha: str = "",
) -> None:
    owner, repo = repo_slug.split("/", 1)
    await _post_tank_control_action(
        http,
        {"Authorization": f"Bearer {service_token}", "Content-Type": "application/json"},
        {
            "event_id": f"tank-break-glass-use-{ORIGIN_SESSION_ID}-{uuid4().hex}",
            "invocation_id": f"tank-break-glass-use-{uuid4().hex}",
            "source_service": _BREAK_GLASS_MCP_SERVER_NAME,
            "source_tool": tool,
            "action": action,
            "status": status,
            "target_kind": "github_repository" if not result_sha else "github_commit",
            "target_ref": f"https://github.com/{repo_slug}" if not result_sha else f"https://github.com/{repo_slug}/commit/{result_sha}",
            "repo_owner": owner,
            "repo_name": repo,
            "result_sha": result_sha,
            "error": error,
            "payload": payload,
        },
    )


async def _handle_break_glass_mint_token(http: ClientSession, auth_romaine_provider, request_id: object, arguments: dict) -> web.Response:
    repo_slug = str(arguments.get("repo") or "").strip()
    if "/" not in repo_slug:
        return _mcp_error_response(request_id, -32602, "repo must be a GitHub slug like owner/name")
    service_token = await auth_romaine_provider.token()
    grant = await _active_break_glass_grant(http, service_token, repo_slug)
    if not _grant_allows(grant, _BREAK_GLASS_MINT_TOKEN_TOOL):
        return _mcp_error_response(request_id, -32020, "no active break-glass grant allows mint_full_git_token", {"repo": repo_slug})
    if _grant_is_branch_restricted(grant):
        return _mcp_error_response(
            request_id,
            -32020,
            "active break-glass grant is branch-scoped; use push_current_head so Tank can enforce the branch scope",
            {"repo": repo_slug, "grant_event_id": grant.get("event_id")},
        )
    workflows = bool(arguments.get("workflows"))
    try:
        token = await _mint_github_installation_token(http, service_token, repo_slug, workflows=workflows, full=True)
        await _record_break_glass_use(
            http,
            service_token,
            action="github.break_glass.token",
            status="succeeded",
            tool=_BREAK_GLASS_MINT_TOKEN_TOOL,
            repo_slug=repo_slug,
            payload={
                "grant_event_id": grant.get("event_id"),
                "grant_expires_at": grant.get("expires_at"),
                "workflows": workflows,
                "reason": str(arguments.get("reason") or ""),
            },
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [
                    {
                        "type": "text",
                        "text": (
                            "Approved break-glass GitHub token minted. Use it only for the approved repo and TTL; "
                            "all use remains subject to audit."
                        ),
                    }
                ],
                "structuredContent": {
                    "repo": repo_slug,
                    "token": token,
                    "grant_event_id": grant.get("event_id"),
                    "grant_expires_at": grant.get("expires_at"),
                    "workflows": workflows,
                },
            },
        )
    except Exception as exc:
        await _record_break_glass_use(
            http,
            service_token,
            action="github.break_glass.token",
            status="failed",
            tool=_BREAK_GLASS_MINT_TOKEN_TOOL,
            repo_slug=repo_slug,
            error=str(exc),
            payload={"grant_event_id": grant.get("event_id"), "workflows": workflows},
        )
        raise


async def _handle_break_glass_push_head(http: ClientSession, auth_romaine_provider, request_id: object, arguments: dict) -> web.Response:
    repo_path = _repo_path_from_arguments(arguments)
    branch = await _git_output(repo_path, "branch", "--show-current")
    if not branch:
        return _mcp_error_response(request_id, -32602, "current repo is detached; checkout a branch before pushing")
    if not bool(arguments.get("allow_dirty")):
        rc, stdout, stderr = await _run_git(repo_path, "status", "--porcelain")
        if rc != 0:
            raise RuntimeError(stderr or stdout or "git status failed")
        if stdout.strip():
            return _mcp_error_response(request_id, -32602, "worktree has uncommitted changes; commit or pass allow_dirty=true before pushing HEAD")
    sha = await _git_output(repo_path, "rev-parse", "HEAD")
    remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
    slug = _repo_slug_from_remote(remote_url)
    if slug is None:
        return _mcp_error_response(request_id, -32602, f"origin remote is not a GitHub URL: {remote_url}")
    repo_slug = f"{slug[0]}/{slug[1]}"
    requested_repo = str(arguments.get("repo") or "").strip()
    if requested_repo and requested_repo != repo_slug:
        return _mcp_error_response(request_id, -32602, f"repo argument {requested_repo!r} does not match origin {repo_slug}")
    service_token = await auth_romaine_provider.token()
    grant = await _active_break_glass_grant(http, service_token, repo_slug)
    if not _grant_allows(grant, _BREAK_GLASS_PUSH_HEAD_TOOL):
        return _mcp_error_response(request_id, -32020, "no active break-glass grant allows push_current_head", {"repo": repo_slug})
    if not _grant_branch_allows(grant, branch):
        return _mcp_error_response(
            request_id,
            -32020,
            f"active break-glass grant does not allow branch {branch}",
            {"repo": repo_slug, "branch": branch},
        )
    try:
        token = await _mint_github_installation_token(http, service_token, repo_slug)
        await _push_head_with_token(repo_path, branch, token)
        await _record_break_glass_use(
            http,
            service_token,
            action="github.break_glass.push",
            status="succeeded",
            tool=_BREAK_GLASS_PUSH_HEAD_TOOL,
            repo_slug=repo_slug,
            result_sha=sha,
            payload={
                "grant_event_id": grant.get("event_id"),
                "grant_expires_at": grant.get("expires_at"),
                "branch": branch,
                "reason": str(arguments.get("reason") or ""),
                "repo_path": str(repo_path),
            },
        )
        asyncio.create_task(
            _watch_published_commit(
                http,
                auth_romaine_provider,
                slug[0],
                slug[1],
                branch,
                sha,
                f"tank-break-glass-push-{uuid4().hex}",
                source_tool=_BREAK_GLASS_PUSH_HEAD_TOOL,
            )
        )
        return _mcp_result_response(
            request_id,
            {
                "content": [
                    {
                        "type": "text",
                        "text": (
                            f"Break-glass pushed {repo_slug}@{sha[:12]} to {branch}. "
                            f"Tank started CI/mergeability watching. {_CI_ENFORCEMENT_TEXT}"
                        ),
                    }
                ],
                "structuredContent": {
                    "repo": repo_slug,
                    "branch": branch,
                    "sha": sha,
                    "grant_event_id": grant.get("event_id"),
                    "ci_watch": "started",
                },
            },
        )
    except Exception as exc:
        await _record_break_glass_use(
            http,
            service_token,
            action="github.break_glass.push",
            status="failed",
            tool=_BREAK_GLASS_PUSH_HEAD_TOOL,
            repo_slug=repo_slug,
            result_sha=sha,
            error=str(exc),
            payload={"grant_event_id": grant.get("event_id"), "branch": branch, "repo_path": str(repo_path)},
        )
        raise


async def _handle_break_glass_mcp(
    http: ClientSession,
    auth_romaine_provider,
    request: web.Request,
) -> web.Response:
    body = await request.read()
    parsed = _parse_mcp_method(body)
    request_id = parsed[2] if parsed else None
    try:
        if parsed is None:
            return _mcp_error_response(None, -32600, "invalid MCP request")
        method, params, request_id = parsed
        if method == "tools/list":
            activation = _read_break_glass_activation()
            if not activation:
                return _mcp_result_response(request_id, {"tools": []})
            repo_slug = str(activation.get("repo") or "")
            service_token = await auth_romaine_provider.token()
            grant = await _active_break_glass_grant(http, service_token, repo_slug)
            if not grant:
                return _mcp_result_response(request_id, {"tools": []})
            return _mcp_result_response(request_id, _break_glass_tools_result())
        if method != "tools/call":
            return _mcp_error_response(request_id, -32601, f"unsupported method {method}")
        activation = _read_break_glass_activation()
        if not activation:
            return _mcp_error_response(
                request_id,
                -32022,
                "break-glass MCP is not activated; call request_git_break_glass after approval",
            )
        activated_repo = str(activation.get("repo") or "")
        name = params.get("name")
        arguments = params.get("arguments") or {}
        if not isinstance(arguments, dict):
            return _mcp_error_response(request_id, -32602, "arguments must be an object")
        if name == _BREAK_GLASS_MINT_TOKEN_TOOL:
            requested_repo = str(arguments.get("repo") or "").strip()
            if not requested_repo:
                if not activated_repo:
                    return _mcp_error_response(request_id, -32602, "repo is required for this all-repos break-glass activation")
                arguments = {**arguments, "repo": activated_repo}
                requested_repo = activated_repo
            if not _activation_scope_allows_repo(activation, requested_repo):
                return _mcp_error_response(request_id, -32602, f"repo is outside activated break-glass scope")
            return await _handle_break_glass_mint_token(http, auth_romaine_provider, request_id, arguments)
        if name == _BREAK_GLASS_PUSH_HEAD_TOOL:
            requested_repo = str(arguments.get("repo") or "").strip()
            if not requested_repo and not arguments.get("repo_path"):
                if not activated_repo:
                    return _mcp_error_response(request_id, -32602, "repo or repo_path is required for this all-repos break-glass activation")
                arguments = {**arguments, "repo": activated_repo}
                requested_repo = activated_repo
            if requested_repo and not _activation_scope_allows_repo(activation, requested_repo):
                return _mcp_error_response(request_id, -32602, f"repo is outside activated break-glass scope")
            return await _handle_break_glass_push_head(http, auth_romaine_provider, request_id, arguments)
        return _mcp_error_response(request_id, -32601, f"unknown break-glass tool {name}")
    except Exception as exc:
        log.warning("break-glass MCP request failed", exc_info=True)
        return _mcp_error_response(request_id, -32021, str(exc), {"server": _BREAK_GLASS_MCP_SERVER_NAME})


def _json_upstream_error(status: int, reason: str, *, mcp_label: str, attempts: int) -> web.Response:
    """Terminal upstream failure response. JSON-shaped so the Claude
    agent SDK's MCP transport parses it cleanly instead of landing on
    a plain-text body that leaves the connection unrecoverable across
    the session lifetime."""
    return web.json_response(
        {
            "error": "upstream_unavailable",
            "error_description": reason,
            "mcp_server": mcp_label,
            "attempts": attempts,
        },
        status=status,
    )

log = logging.getLogger(__name__)

SA_TOKEN_PATH = Path("/var/run/secrets/kubernetes.io/serviceaccount/token")
AZURE_MCP_PORT = 9991
GITHUB_MCP_PORT = 9992
GLIMMUNG_MCP_PORT = 9995
TANK_OPERATOR_MCP_PORT = 9996
SPIRELENS_MCP_PORT = 9997
GRAFANA_MCP_PORT = 9998

# Optional tailnet upstream: the SpireLens game-host MCP (spire-lens-mcp's
# server.py --transport http). Unlike the in-cluster .svc upstreams below it
# lives on the Tailscale tailnet (tag:spirelens-host), so its requests are
# routed through tailscaled's userspace outbound HTTP proxy (TAILNET_HTTP_PROXY)
# and authenticated with the pod's auth.romaine.life service JWT (the server
# validates it with --auth-mode jwt). The listener is added only when
# SPIRELENS_MCP_UPSTREAM is set (e.g. http://nelsonlaptop:15527), so a pod that
# never joins the tailnet does not expose a dead port. See
# docs/tailnet-host-access.md.
SPIRELENS_MCP_UPSTREAM = (os.environ.get("SPIRELENS_MCP_UPSTREAM") or "").strip()
# CONNECT proxy into the tailnet (tailscaled --outbound-http-proxy-listen).
# Applied ONLY to the SpireLens upstream, passed per-request rather than via the
# HTTP_PROXY env so the in-cluster .svc upstreams keep reaching cluster IPs
# directly (aiohttp would otherwise proxy every request).
TAILNET_HTTP_PROXY = (os.environ.get("TAILNET_HTTP_PROXY") or "").strip() or None

# auth.romaine.life service-principal exchange (see
# romaine-life/tank-operator#486). The session pod mounts a projected SA
# token with `audience: https://auth.romaine.life` at this path; this
# sidecar POSTs it to AUTH_ROMAINE_EXCHANGE_URL and receives a JWT with
# role=service that downstream tank-operator endpoints accept.
AUTH_ROMAINE_SA_TOKEN_PATH = Path(
    os.environ.get(
        "AUTH_ROMAINE_SA_TOKEN_PATH",
        "/var/run/secrets/auth.romaine.life/token",
    )
)
AUTH_ROMAINE_EXCHANGE_URL = os.environ.get(
    "AUTH_ROMAINE_EXCHANGE_URL",
    "https://auth.romaine.life/api/auth/exchange/k8s",
).rstrip("/")
# Header name shared with mcp-tank-operator's CallerIdentityMiddleware.
# Changing it requires a cross-repo coordinated deploy.
AUTH_ROMAINE_FORWARD_HEADER = "X-Auth-Romaine-Token"

# Originating tank-operator session id forwarded on outbound calls to
# mcp-tank-operator. Set from this pod's SESSION_ID env var (sourced
# from the `tank-operator/session-id` Pod label). mcp-tank-operator
# threads it into ORIGIN_SESSION_ID and forwards it on to the
# orchestrator, which stamps it onto the persisted user_message.created
# event so the frontend renders the parent session's avatar on the
# user bubble in the target session. Empty env (e.g. local dev without
# the downward-API mount) is fine — the header is omitted and the
# orchestrator falls back to the human-Gravatar rendering. Header name
# shared with mcp-tank-operator/src/mcp_tank_operator/caller.py and
# tank-operator/backend-go/cmd/tank-operator/handlers_internal.go;
# changing it requires a coordinated cross-repo deploy.
ORIGIN_SESSION_FORWARD_HEADER = "X-Tank-Origin-Session-Id"
ORIGIN_SESSION_AVATAR_FORWARD_HEADER = "X-Tank-Origin-Session-Avatar-Id"
ORIGIN_SESSION_ID = (os.environ.get("SESSION_ID") or "").strip()
ORIGIN_SESSION_SCOPE = (os.environ.get("SESSION_SCOPE") or "default").strip() or "default"
ORIGIN_SESSION_AVATAR_ID = (os.environ.get("AGENT_AVATAR_ID") or "").strip()
RESTRICTED_GIT_ENABLED = (os.environ.get("TANK_RESTRICTED_GIT") or "").strip().lower() in {
    "1",
    "true",
    "yes",
    "on",
}

# Caller-context headers identify the session pod that is making an MCP call.
# Unlike X-Tank-Origin-Session-Id, these are not handoff/display metadata; they
# are the workflow ownership identity that Tank/Glimmung tools use when a tool
# means "the current session". The auth.romaine.life JWT remains the authority
# for owner/email. These headers only bind the already-authenticated caller to
# its session row without asking the model to restate that id.
CALLER_SYSTEM_FORWARD_HEADER = "X-Tank-Caller-System"
CALLER_KIND_FORWARD_HEADER = "X-Tank-Caller-Kind"
CALLER_SESSION_ID_FORWARD_HEADER = "X-Tank-Caller-Session-Id"
CALLER_SESSION_SCOPE_FORWARD_HEADER = "X-Tank-Caller-Session-Scope"
SESSION_SCOPE = (os.environ.get("SESSION_SCOPE") or "").strip()

# (port, upstream URL). Mirrors k8s/session-config/mcp.json. Adding an
# MCP means: append here, append a port mapping in mcp.json, ship.
#
# Port allocation (next free: 9999):
#   9991 â€” mcp-azure-personal
#   9992 â€” mcp-github
#   9993 â€” mcp-k8s
#   9994 â€” mcp-argocd
#   9995 â€” mcp-glimmung
#   9996 â€” mcp-tank-operator
#   9997 â€” optional SpireLens MCP, only when SPIRELENS_MCP_UPSTREAM is set
#   9998 â€” mcp-grafana
LISTENERS: list[tuple[int, str]] = [
    # azure-personal: the listener always runs, but azure-personal is NOT in the
    # default session .mcp.json (k8s/session-config/mcp.json) — it is locked by
    # default. On an approved azure break-glass grant the orchestrator enqueues an
    # approval turn whose mcp_activate payload the pod-side runner uses to add the
    # server + rebuild, so this 9991 listener becomes reachable only then.
    (9991, "http://mcp-azure-personal.mcp-azure-personal.svc:80"),
    (9992, "http://mcp-github.mcp-github.svc:80"),
    (9993, "http://mcp-k8s.mcp-k8s.svc:80"),
    (9994, "http://mcp-argocd.mcp-argocd.svc:80"),
    (9995, "http://mcp-glimmung.mcp-glimmung.svc:80"),
    (9996, "http://mcp-tank-operator.mcp-tank-operator.svc:80"),
    (9998, "http://mcp-grafana.mcp-grafana.svc:80"),
]


def _effective_listeners(spirelens_upstream: str = SPIRELENS_MCP_UPSTREAM) -> list[tuple[int, str]]:
    listeners = list(LISTENERS)
    upstream = (spirelens_upstream or "").strip()
    if upstream:
        listeners.append((SPIRELENS_MCP_PORT, upstream))
    return listeners

# Headers we strip from the inbound request before forwarding. Host is
# rebuilt by aiohttp for the upstream; Authorization gets replaced with
# the fresh SA token; hop-by-hop and content-length are recomputed.
_STRIP_REQUEST_HEADERS = frozenset(
    {"host", "authorization", "content-length", "connection", "transfer-encoding"}
)
# Same idea on the way back â€” let aiohttp set framing headers on the
# response we stream to the client.
_STRIP_RESPONSE_HEADERS = frozenset(
    {"transfer-encoding", "content-encoding", "connection", "content-length"}
)


def _read_token(path: Path) -> str:
    return path.read_text().strip()


class ServiceAccountTokenProvider:
    def __init__(self, token_path: Path = SA_TOKEN_PATH) -> None:
        self._token_path = token_path

    async def token(self) -> str:
        try:
            value = _read_token(self._token_path)
        except Exception:
            record_sa_token_read("failure")
            raise
        record_sa_token_read("success")
        return value


class AuthRomaineServiceProvider:
    """Exchanges the pod's auth.romaine.life-audience projected SA token
    for a `role=service` JWT via auth.romaine.life's
    /api/auth/exchange/k8s. Caches the JWT until ~30s before expiry.

    Used to inject the X-Auth-Romaine-Token header on outbound calls to
    mcp-tank-operator (port 9996), enabling its spawn_service_session
    tool. See romaine-life/tank-operator#486.
    """

    def __init__(
        self,
        http: ClientSession,
        *,
        exchange_url: str = AUTH_ROMAINE_EXCHANGE_URL,
        token_path: Path = AUTH_ROMAINE_SA_TOKEN_PATH,
        refresh_skew_seconds: float = 30.0,
    ) -> None:
        self._http = http
        self._exchange_url = exchange_url
        self._token_path = token_path
        self._refresh_skew_seconds = refresh_skew_seconds
        self._cached_token = ""
        self._expires_at = 0.0
        self._lock = asyncio.Lock()

    async def token(self) -> str:
        now = time.time()
        if self._cached_token and self._expires_at > now + self._refresh_skew_seconds:
            record_auth_romaine_exchange("cache_hit")
            return self._cached_token
        async with self._lock:
            now = time.time()
            if self._cached_token and self._expires_at > now + self._refresh_skew_seconds:
                record_auth_romaine_exchange("cache_hit")
                return self._cached_token
            if not self._exchange_url:
                record_auth_romaine_exchange("exception")
                raise RuntimeError(
                    "AUTH_ROMAINE_EXCHANGE_URL is required for auth.romaine.life exchange"
                )
            try:
                sa_token = _read_token(self._token_path)
                async with self._http.post(
                    self._exchange_url,
                    headers={"Authorization": f"Bearer {sa_token}"},
                    json={},
                ) as response:
                    if response.status != 200:
                        detail = (await response.text())[:300]
                        record_auth_romaine_exchange("http_error")
                        raise RuntimeError(
                            f"auth.romaine.life exchange returned {response.status}: {detail}"
                        )
                    body = await response.json()
            except RuntimeError:
                raise
            except Exception:
                record_auth_romaine_exchange("exception")
                raise
            token = str(body.get("token") or "")
            expires_at = _parse_expires_at(body.get("expires_at"))
            if not token or expires_at <= time.time():
                record_auth_romaine_exchange("invalid_response")
                raise RuntimeError("auth.romaine.life exchange response was invalid")
            self._cached_token = token
            self._expires_at = expires_at
            record_auth_romaine_exchange("success")
            return token


def _parse_expires_at(value: object) -> float:
    if isinstance(value, (int, float)):
        return float(value)
    if not isinstance(value, str) or not value:
        return 0.0
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        return datetime.fromisoformat(text).timestamp()
    except ValueError:
        return 0.0


# OAuth discovery paths the MCP SDK probes. RFC 8414 (auth server),
# RFC 9728 (protected resource), and OIDC discovery â€” the SDK tries
# all of these before/after a transport failure to decide whether OAuth
# is available. Answering locally with a JSON-shaped 404 keeps the
# SDK's parser from crashing on upstream's plain-text "Not Found" body.
_OAUTH_DISCOVERY_PATHS = (
    "/.well-known/oauth-authorization-server",
    "/.well-known/oauth-protected-resource",
    "/.well-known/openid-configuration",
)


async def _oauth_discovery_not_configured(request: web.Request) -> web.Response:
    return web.json_response(
        {
            "error": "not_found",
            "error_description": (
                "OAuth not configured on this MCP server; bearer "
                "auth is injected by the mcp-auth-proxy sidecar."
            ),
        },
        status=404,
    )


def _mcp_server_label(upstream: str) -> str:
    """Extract a bounded label from the upstream URL. Example:
    'http://mcp-azure-personal.mcp-azure-personal.svc:80' → 'mcp-azure-personal'.
    The fallback is the full host string, which is still bounded by the
    LISTENERS map but less Grafana-friendly. Cardinality is the count
    of distinct upstreams (~6), never per-request.
    """
    host = upstream.replace("http://", "").replace("https://", "")
    name = host.split(".", 1)[0]
    return name or host or "unknown"


def _make_handler(
    upstream: str,
    http: ClientSession,
    token_provider,
    *,
    extra_header_provider=None,
    static_headers=None,
    proxy: str | None = None,
    github_activity_provider=None,
    tank_publish_provider=None,
    azure_break_glass_provider=None,
    glimmung_hot_swap_provider=None,
    block_github_write_tools: bool = False,
):
    """Build the request handler for an MCP upstream.

    `extra_header_provider`, when supplied, is awaited per request to
    obtain an additional header value injected on the way out (today:
    X-Auth-Romaine-Token for mcp-tank-operator). A None return skips
    injection so the upstream sees the request without the extra header.
    An exception in the provider is logged at INFO but does NOT fail
    the request — the upstream still receives the normal Bearer-authed
    call, will reject any service-principal-gated route with 401, and
    the caller surfaces the error end-to-end. See
    romaine-life/tank-operator#486.

    `static_headers`, when supplied, is a mapping of header-name → value
    injected verbatim on every outbound request to this upstream.
    Synchronous and per-process-constant — used for identity inputs
    sourced from the pod environment that don't change at runtime
    (today: X-Tank-Origin-Session-Id from SESSION_ID). Empty or None
    values are omitted so the upstream sees the request without the
    header, matching the orchestrator's "fall back to human Gravatar"
    behavior when the field is absent.
    """
    upstream = upstream.rstrip("/")
    mcp_label = _mcp_server_label(upstream)

    async def handler(request: web.Request) -> web.StreamResponse:
        try:
            token = await token_provider.token()
        except Exception:
            log.exception("could not load bearer token for %s", upstream)
            record_proxy_request(mcp_label, 503)
            return web.Response(status=503, text="bearer token unavailable")

        forwarded_headers = {
            k: v for k, v in request.headers.items() if k.lower() not in _STRIP_REQUEST_HEADERS
        }
        forwarded_headers["Authorization"] = f"Bearer {token}"

        if static_headers:
            for name, value in static_headers.items():
                if name and value:
                    forwarded_headers[name] = value

        if extra_header_provider is not None:
            try:
                name, value = await extra_header_provider()
            except Exception:
                # Non-fatal: the upstream will reject any service-
                # principal-gated route with 401 (post-#486 there is no
                # acceptance shape other than the auth.romaine.life
                # service JWT this header carries). Logged at INFO
                # because the exchange failure rate is already tracked
                # via the dedicated counter (auth.romaine.life
                # exchange) and a duplicate WARN here would just spam.
                log.info(
                    "extra-header provider failed for %s; forwarding without it",
                    upstream,
                    exc_info=True,
                )
            else:
                if name and value:
                    forwarded_headers[name] = value

        body = await request.read()
        if block_github_write_tools:
            parsed_tool = _parse_mcp_tool_call(body)
            if parsed_tool is not None and parsed_tool[0] in _GITHUB_WRITE_TOOL_DENYLIST:
                record_proxy_request(mcp_label, 200)
                return _github_tool_block_response(body, parsed_tool[0])

        parsed_method = _parse_mcp_method(body)
        if tank_publish_provider is not None and parsed_method is not None:
            method, params, request_id = parsed_method
            if method == "tools/call" and params.get("name") in {
                _TANK_PUBLISH_TOOL,
                _TANK_BREAK_GLASS_TOOL,
                _TANK_PR_LANE_TOOL,
                _TANK_CREATE_PR_LANE_TOOL,
                _TANK_MERGE_TOOL,
                _TANK_RENAME_PR_TOOL,
                _TANK_UPDATE_PR_BODY_TOOL,
            }:
                arguments = params.get("arguments") or {}
                if not isinstance(arguments, dict):
                    return _mcp_error_response(request_id, -32602, "arguments must be an object")
                record_proxy_request(mcp_label, 200)
                if params.get("name") == _TANK_PUBLISH_TOOL:
                    return await _handle_tank_publish_tool(http, tank_publish_provider, request_id, arguments)
                if params.get("name") == _TANK_BREAK_GLASS_TOOL:
                    return await _handle_tank_break_glass_tool(http, tank_publish_provider, request_id, arguments)
                if params.get("name") == _TANK_CREATE_PR_LANE_TOOL:
                    return await _handle_tank_create_pr_lane_tool(http, tank_publish_provider, request_id, arguments)
                if params.get("name") == _TANK_MERGE_TOOL:
                    return await _handle_tank_merge_tool(http, tank_publish_provider, request_id, arguments)
                if params.get("name") == _TANK_RENAME_PR_TOOL:
                    return await _handle_tank_rename_pr_tool(http, tank_publish_provider, request_id, arguments)
                if params.get("name") == _TANK_UPDATE_PR_BODY_TOOL:
                    return await _handle_tank_update_pr_body_tool(http, tank_publish_provider, request_id, arguments)
                return await _handle_tank_pr_lane_tool(http, tank_publish_provider, request_id, arguments)

        # azure-personal break-glass is independent of restricted-git mode:
        # azure is locked by default for every session, so the request tool is
        # always handled on the Tank surface (not gated on tank_publish_provider).
        if azure_break_glass_provider is not None and parsed_method is not None:
            method, params, request_id = parsed_method
            if method == "tools/call" and params.get("name") == _TANK_AZURE_BREAK_GLASS_TOOL:
                arguments = params.get("arguments") or {}
                if not isinstance(arguments, dict):
                    return _mcp_error_response(request_id, -32602, "arguments must be an object")
                record_proxy_request(mcp_label, 200)
                return await _handle_tank_azure_break_glass_tool(http, azure_break_glass_provider, request_id, arguments)

        if glimmung_hot_swap_provider is not None and parsed_method is not None:
            method, params, request_id = parsed_method
            if method == "tools/call" and params.get("name") == _GLIMMUNG_HOT_SWAP_TOOL:
                arguments = params.get("arguments") or {}
                if not isinstance(arguments, dict):
                    return _mcp_error_response(request_id, -32602, "arguments must be an object")
                prepared = await _prepare_glimmung_hot_swap_call(
                    http,
                    glimmung_hot_swap_provider,
                    request_id,
                    body,
                    arguments,
                )
                if isinstance(prepared, web.Response):
                    record_proxy_request(mcp_label, 200)
                    return prepared
                body, _verification = prepared

        github_tool_call = _parse_mcp_tool_call(body) if github_activity_provider is not None else None
        tank_tools_list = tank_publish_provider is not None and parsed_method is not None and parsed_method[0] == "tools/list"
        azure_tools_list = azure_break_glass_provider is not None and parsed_method is not None and parsed_method[0] == "tools/list"
        github_tools_list = block_github_write_tools and parsed_method is not None and parsed_method[0] == "tools/list"
        url = upstream + request.path_qs

        # Bounded-retry loop. Two failure modes are retried because the
        # SDK has no recovery for them within a session:
        #   - aiohttp.ClientError before we start streaming (upstream
        #     pod rotation: connection refused / reset / DNS flap).
        #   - HTTP 502/503/504 from the upstream (kube-rbac-proxy
        #     sidecar in front of the MCP returns 502 briefly while
        #     the MCP container restarts).
        # Anything past response.prepare() is mid-stream; we surface
        # the broken stream rather than mask a real regression.
        last_failure_reason = "all retry attempts failed"

        for attempt in range(_MAX_UPSTREAM_ATTEMPTS):
            started_streaming = False
            try:
                async with upstream_timer(mcp_label):
                    async with http.request(
                        request.method,
                        url,
                        headers=forwarded_headers,
                        data=body,
                        allow_redirects=False,
                        proxy=proxy,
                    ) as upstream_resp:
                        status = upstream_resp.status

                        # Transient upstream statuses get handled
                        # entirely inside this branch BEFORE we call
                        # response.prepare() — once streaming starts
                        # we can't fail back into the loop without
                        # leaving the SDK to parse a truncated body,
                        # which is the exact unrecoverable state this
                        # whole change exists to prevent.
                        if status in _TRANSIENT_UPSTREAM_STATUSES:
                            # Drain so the connection returns to the
                            # pool cleanly rather than getting closed.
                            await upstream_resp.read()
                            record_proxy_retry(mcp_label, "transient_status")
                            last_failure_reason = (
                                f"upstream returned transient status {status}"
                            )
                            if attempt < _MAX_UPSTREAM_ATTEMPTS - 1:
                                log.info(
                                    "upstream %s returned %d on attempt %d/%d; retrying",
                                    url,
                                    status,
                                    attempt + 1,
                                    _MAX_UPSTREAM_ATTEMPTS,
                                )
                                await asyncio.sleep(_retry_delay(attempt))
                                continue
                            # Final attempt was still transient: do
                            # NOT pass the upstream's body through —
                            # an upstream "Bad Gateway" plain-text
                            # body would crash the SDK's JSON parser
                            # just as badly as a transport error.
                            # Fall through to the exhaustion path
                            # below.
                            log.warning(
                                "upstream %s returned %d on final attempt %d/%d",
                                url,
                                status,
                                attempt + 1,
                                _MAX_UPSTREAM_ATTEMPTS,
                            )
                            break

                        response_headers = {
                            k: v
                            for k, v in upstream_resp.headers.items()
                            if k.lower() not in _STRIP_RESPONSE_HEADERS
                        }
                        if github_tool_call is not None or tank_tools_list or azure_tools_list or github_tools_list:
                            response_headers.pop("Content-Length", None)
                            response_headers.pop("content-length", None)
                            response_body = await upstream_resp.read()
                            if 200 <= status < 300:
                                if github_tool_call is not None:
                                    tool_name, arguments = github_tool_call
                                    try:
                                        await _record_github_tool_activity(
                                            http,
                                            github_activity_provider,
                                            tool_name,
                                            arguments,
                                            response_body,
                                        )
                                    except Exception:
                                        log.warning(
                                            "failed to record GitHub MCP activity",
                                            exc_info=True,
                                        )
                                    if tool_name in {"create_pull_request", "commit_to_branch", "create_or_update_file"}:
                                        response_body = _append_ci_reminder(response_body)
                                if tank_tools_list:
                                    response_body = _append_tank_publish_tool(response_body)
                                if azure_tools_list:
                                    response_body = _append_azure_break_glass_tool(response_body)
                                if github_tools_list:
                                    response_body = _filter_github_write_tools(response_body)
                            record_proxy_request(mcp_label, status)
                            return web.Response(
                                status=status,
                                headers=response_headers,
                                body=response_body,
                            )

                        response = web.StreamResponse(
                            status=status,
                            headers=response_headers,
                        )
                        await response.prepare(request)
                        started_streaming = True
                        async for chunk in upstream_resp.content.iter_any():
                            await response.write(chunk)
                        await response.write_eof()
                record_proxy_request(mcp_label, status)
                return response
            except ClientError as exc:
                if started_streaming:
                    # Mid-stream drop. The wire is already committed
                    # to a partial response; let it surface as the
                    # broken stream it is rather than mask a real
                    # upstream regression with a confusing retry that
                    # writes JSON on top of partial bytes.
                    log.warning(
                        "upstream %s dropped mid-stream on attempt %d: %r",
                        url,
                        attempt + 1,
                        exc,
                    )
                    record_proxy_request(mcp_label, 502)
                    raise
                record_proxy_retry(mcp_label, "transport_error")
                last_failure_reason = f"transport error: {exc!r}"
                if attempt >= _MAX_UPSTREAM_ATTEMPTS - 1:
                    log.warning(
                        "upstream request to %s failed after %d attempts: %r",
                        url,
                        attempt + 1,
                        exc,
                    )
                    break
                log.info(
                    "upstream request to %s failed on attempt %d/%d (%r); retrying",
                    url,
                    attempt + 1,
                    _MAX_UPSTREAM_ATTEMPTS,
                    exc,
                )
                await asyncio.sleep(_retry_delay(attempt))

        # Retry budget exhausted. Return a JSON-shaped 502 so the SDK's
        # MCP transport parser doesn't crash on a plain-text body and
        # leave the connection unrecoverable for the rest of the
        # session — same shape as the OAuth discovery 404 short-circuit
        # at the top of this file.
        record_proxy_retry(mcp_label, "exhausted")
        record_proxy_request(mcp_label, 502)
        return _json_upstream_error(
            502,
            last_failure_reason,
            mcp_label=mcp_label,
            attempts=_MAX_UPSTREAM_ATTEMPTS,
        )

    return handler


async def run() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    # Long total timeout â€” MCP tool calls can be minutes. Connect timeout
    # short so a dead upstream Service surfaces fast instead of hanging
    # the user-visible MCP call.
    http = ClientSession(timeout=ClientTimeout(total=600, sock_connect=5))
    runners: list[web.AppRunner] = []
    # Bind metrics on a separate port so the PodMonitor scrape doesn't
    # clash with the localhost-only MCP listeners. 9990 sits just below
    # the LISTENERS port range (9991+) so the entire observability +
    # MCP-proxy port block reads contiguously.
    metrics_port = int(os.environ.get("MCP_AUTH_PROXY_METRICS_PORT", "9990"))
    metrics_runner = await start_metrics_server(metrics_port)
    runners.append(metrics_runner)
    # Shared across mcp-tank-operator and mcp-github outbound calls so
    # the cached JWT (15-min TTL) is reused across many tool calls in a
    # session.
    auth_romaine_provider = AuthRomaineServiceProvider(http)

    # The SpireLens game-host MCP is a tailnet upstream, added only when
    # configured (SPIRELENS_MCP_UPSTREAM) and reached through the tailscaled
    # outbound HTTP proxy. See docs/tailnet-host-access.md.
    effective_listeners = _effective_listeners()
    if SPIRELENS_MCP_UPSTREAM:
        if not TAILNET_HTTP_PROXY:
            log.warning(
                "SPIRELENS_MCP_UPSTREAM set but TAILNET_HTTP_PROXY is empty; the "
                "tailnet upstream on :%d will be unreachable until the pod joins "
                "the tailnet and exposes its outbound HTTP proxy",
                SPIRELENS_MCP_PORT,
            )

    try:
        for port, upstream in effective_listeners:
            app = web.Application()
            for discovery_path in _OAUTH_DISCOVERY_PATHS:
                app.router.add_route("GET", discovery_path, _oauth_discovery_not_configured)
            # RFC 7591 Dynamic Client Registration â€” also intercepted so the
            # SDK gets a JSON 404 rather than an upstream plain-text one.
            app.router.add_route("POST", "/register", _oauth_discovery_not_configured)
            if port in (GITHUB_MCP_PORT, SPIRELENS_MCP_PORT):
                # Both authenticate with the auth.romaine.life service JWT as
                # the bearer. mcp-github verifies it against the IdP's JWKS and
                # resolves the caller's GitHub App installation by calling
                # tank-operator's /api/internal/github/installation with the
                # same bearer forwarded; the SpireLens game-host MCP validates
                # it directly with --auth-mode jwt.
                token_provider = auth_romaine_provider
            else:
                token_provider = ServiceAccountTokenProvider()

            # mcp-tank-operator, mcp-glimmung, mcp-grafana, and mcp-azure-personal
            # gate their tool surface on the caller's auth.romaine.life service
            # JWT (read from X-Auth-Romaine-Token because Authorization carries
            # the SA token kube-rbac-proxy validates in front of each). Inject
            # the header so the upstreams can attribute and authorize every call
            # to the originating session/user. For mcp-azure-personal this JWT,
            # plus the caller-session headers below, is what lets the server look
            # up the session's break-glass grant and refuse without one.
            extra_header_provider = None
            if port in (TANK_OPERATOR_MCP_PORT, GLIMMUNG_MCP_PORT, GRAFANA_MCP_PORT, AZURE_MCP_PORT):
                async def _provide_auth_romaine_header(
                    provider=auth_romaine_provider,
                ) -> tuple[str, str]:
                    return AUTH_ROMAINE_FORWARD_HEADER, await provider.token()
                extra_header_provider = _provide_auth_romaine_header

            # Tell Tank/Glimmung MCP servers which session pod is calling.
            # These current-session headers are ownership context for workflow
            # controls such as test slots and PR links; they are injected by
            # infrastructure, not supplied by the model. Keep the older origin
            # header scoped to mcp-tank-operator handoff/avatar semantics.
            static_headers = None
            if port in (TANK_OPERATOR_MCP_PORT, GLIMMUNG_MCP_PORT, AZURE_MCP_PORT) and ORIGIN_SESSION_ID:
                static_headers = {
                    CALLER_SYSTEM_FORWARD_HEADER: "tank-operator",
                    CALLER_KIND_FORWARD_HEADER: "session",
                    CALLER_SESSION_ID_FORWARD_HEADER: ORIGIN_SESSION_ID,
                }
                if SESSION_SCOPE:
                    static_headers[CALLER_SESSION_SCOPE_FORWARD_HEADER] = SESSION_SCOPE
                if port == TANK_OPERATOR_MCP_PORT:
                    static_headers[ORIGIN_SESSION_FORWARD_HEADER] = ORIGIN_SESSION_ID
                    if ORIGIN_SESSION_AVATAR_ID:
                        static_headers[ORIGIN_SESSION_AVATAR_FORWARD_HEADER] = ORIGIN_SESSION_AVATAR_ID

            # The SpireLens upstream is on the tailnet; route it through the
            # tailscaled outbound HTTP proxy. Every other upstream is an
            # in-cluster .svc and must connect directly (proxy=None).
            request_proxy = TAILNET_HTTP_PROXY if port == SPIRELENS_MCP_PORT else None

            app.router.add_route(
                "*",
                "/{tail:.*}",
                _make_handler(
                    upstream,
                    http,
                    token_provider,
                    extra_header_provider=extra_header_provider,
                    static_headers=static_headers,
                    proxy=request_proxy,
                    github_activity_provider=auth_romaine_provider
                    if RESTRICTED_GIT_ENABLED and port == GITHUB_MCP_PORT
                    else None,
                    tank_publish_provider=(
                        auth_romaine_provider
                        if RESTRICTED_GIT_ENABLED and port == TANK_OPERATOR_MCP_PORT
                        else None
                    ),
                    # azure break-glass request tool is always available on the
                    # Tank surface (not restricted-git gated): azure-personal is
                    # locked by default for every session.
                    azure_break_glass_provider=(
                        auth_romaine_provider if port == TANK_OPERATOR_MCP_PORT else None
                    ),
                    glimmung_hot_swap_provider=(
                        auth_romaine_provider
                        if RESTRICTED_GIT_ENABLED and port == GLIMMUNG_MCP_PORT
                        else None
                    ),
                    block_github_write_tools=RESTRICTED_GIT_ENABLED and port == GITHUB_MCP_PORT,
                ),
            )
            runner = web.AppRunner(app)
            await runner.setup()
            site = web.TCPSite(runner, "127.0.0.1", port)
            await site.start()
            log.info("listening on 127.0.0.1:%d â†’ %s", port, upstream)
            runners.append(runner)
        break_glass_app = web.Application()
        for discovery_path in _OAUTH_DISCOVERY_PATHS:
            break_glass_app.router.add_route("GET", discovery_path, _oauth_discovery_not_configured)
        break_glass_app.router.add_route("POST", "/register", _oauth_discovery_not_configured)
        break_glass_app.router.add_route(
            "*",
            "/{tail:.*}",
            lambda request: _handle_break_glass_mcp(http, auth_romaine_provider, request),
        )
        break_glass_runner = web.AppRunner(break_glass_app)
        await break_glass_runner.setup()
        break_glass_site = web.TCPSite(break_glass_runner, "127.0.0.1", _BREAK_GLASS_MCP_PORT)
        await break_glass_site.start()
        log.info(
            "listening on 127.0.0.1:%d for %s (not advertised until grant activation)",
            _BREAK_GLASS_MCP_PORT,
            _BREAK_GLASS_MCP_SERVER_NAME,
        )
        runners.append(break_glass_runner)
        # Park forever; container lifecycle owns us.
        await asyncio.Event().wait()
    finally:
        for runner in runners:
            await runner.cleanup()
        await http.close()
