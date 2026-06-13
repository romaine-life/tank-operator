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
WORKSPACE_ROOT = Path(os.environ.get("WORKSPACE", "/workspace")).resolve()
MCP_GITHUB_INTERNAL_URL = (
    os.environ.get("MCP_GITHUB_URL") or "http://mcp-github.mcp-github.svc:80"
).rstrip("/")

_GITHUB_PR_URL_RE = re.compile(r"https://github\.com/([^/\s]+)/([^/\s]+)/pull/([0-9]+)")
_GITHUB_COMMIT_URL_RE = re.compile(r"https://github\.com/([^/\s]+)/([^/\s]+)/commit/([0-9a-fA-F]{7,40})")
_GITHUB_REMOTE_RE = re.compile(r"github\.com[:/]([^/\s]+)/([^/\s]+?)(?:\.git)?(?:\s|$)")
_TANK_PUBLISH_TOOL = "publish_current_head"
_TANK_BREAK_GLASS_TOOL = "request_git_break_glass"
_GLIMMUNG_HOT_SWAP_TOOL = "apply_test_slot_hot_swap"
_BREAK_GLASS_MCP_SERVER_NAME = "tank-git-break-glass"
_BREAK_GLASS_MCP_PORT = 9999
_BREAK_GLASS_MINT_TOKEN_TOOL = "mint_full_git_token"
_BREAK_GLASS_PUSH_HEAD_TOOL = "push_current_head"
_AUTH_ROMAINE_BREAK_GLASS_URL = os.environ.get("AUTH_ROMAINE_BREAK_GLASS_URL") or "https://auth.romaine.life/admin"
_GITHUB_WRITE_TOOL_DENYLIST = {
    "mint_clone_token",
    "create_pull_request",
    "commit_to_branch",
    "create_or_update_file",
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
    return _mcp_error_response(
        request_id,
        -32010,
        (
            f"GitHub MCP tool '{tool_name}' is disabled in Tank normal mode. "
            "Use the Tank MCP publish_current_head tool; direct GitHub write "
            "tokens and file/PR writes are reserved for break-glass approval. "
            "If governed publish is not sufficient, call the Tank MCP "
            "request_git_break_glass tool to get an approval URL."
        ),
        {"blocked_tool": tool_name, "replacement_tool": _TANK_PUBLISH_TOOL, "break_glass_tool": _TANK_BREAK_GLASS_TOOL},
    )


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
                    "repo": {
                        "type": "string",
                        "description": "Optional GitHub slug, for example romaine-life/tank-operator.",
                    },
                    "repo_path": {
                        "type": "string",
                        "description": "Optional absolute or /workspace-relative path to the repo.",
                    },
                    "reason": {
                        "type": "string",
                        "description": "Short reason the governed publish path is insufficient.",
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
    repo_slug: str,
    reason: str,
    source: str,
    session_scope: str | None = None,
) -> str:
    scope = (session_scope or ORIGIN_SESSION_SCOPE or "default").strip() or "default"
    params = {
        "intent": "git-break-glass",
        "session_id": session_id,
        "session_scope": scope,
        "repo": repo_slug,
        "source": source,
    }
    if reason:
        params["reason"] = reason
    separator = "&" if "?" in _AUTH_ROMAINE_BREAK_GLASS_URL else "?"
    return f"{_AUTH_ROMAINE_BREAK_GLASS_URL}{separator}{urlencode(params)}"


async def _active_break_glass_grant(http: ClientSession, service_jwt: str, repo_slug: str) -> dict | None:
    if not ORIGIN_SESSION_ID:
        return None
    from urllib.parse import quote

    url = (
        f"{TANK_OPERATOR_INTERNAL_URL}/api/internal/sessions/{ORIGIN_SESSION_ID}/git-break-glass/grant"
        f"?repo={quote(repo_slug, safe='')}"
    )
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


def _grant_allows(grant: dict | None, operation: str) -> bool:
    if not grant:
        return False
    operations = grant.get("operations")
    if not isinstance(operations, list):
        return False
    return operation in {str(item) for item in operations}


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
        "changed_files": changed,
        "reload_required": True,
    }


async def _mint_github_installation_token(http: ClientSession, service_token: str, repo_slug: str, *, workflows: bool = False) -> str:
    arguments: dict[str, object] = {"repos": [repo_slug], "write": True}
    if workflows:
        arguments["workflows"] = True
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


async def _github_api_json(http: ClientSession, token: str, method: str, path: str) -> tuple[int, dict | list | None]:
    async with http.request(
        method,
        f"https://api.github.com{path}",
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    ) as resp:
        text = await resp.text()
        if not text:
            return resp.status, None
        try:
            return resp.status, json.loads(text)
        except json.JSONDecodeError:
            return resp.status, {"raw": text[:1000]}


def _checks_state(check_runs: list, combined_status: dict | None) -> tuple[str, str, dict]:
    conclusions_ok = {"success", "skipped", "neutral"}
    failed: list[str] = []
    pending: list[str] = []
    completed = 0
    for run in check_runs:
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
        repo_name_from_path = repo_path.name
        expected_branch = f"tank/session/{ORIGIN_SESSION_ID}/{repo_name_from_path}"
        if branch != expected_branch:
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {expected_branch!r}")
        if not bool(arguments.get("allow_dirty")):
            rc, stdout, stderr = await _run_git(repo_path, "status", "--porcelain")
            if rc != 0:
                raise RuntimeError(stderr or stdout or "git status failed")
            if stdout.strip():
                raise ValueError("worktree has uncommitted changes; commit or pass allow_dirty=true before publishing HEAD")
        sha = await _git_output(repo_path, "rev-parse", "HEAD")
        subject = await _git_output(repo_path, "log", "-1", "--format=%s")
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
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
        repo_name_from_path = repo_path.name
        expected_branch = f"tank/session/{ORIGIN_SESSION_ID}/{repo_name_from_path}"
        if branch != expected_branch:
            raise ValueError(f"current branch is {branch!r}; expected Tank session branch {expected_branch!r}")
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
        remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
        slug = _repo_slug_from_remote(remote_url)
        if slug is None:
            raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
        owner, repo = slug
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
        repo_slug = str(arguments.get("repo") or "").strip()
        repo_path = None
        if repo_slug:
            if "/" not in repo_slug:
                raise ValueError("repo must be a GitHub slug like owner/name")
        else:
            repo_path = _repo_path_from_arguments(arguments)
            remote_url = await _git_output(repo_path, "config", "--get", "remote.origin.url")
            slug = _repo_slug_from_remote(remote_url)
            if slug is None:
                raise ValueError(f"origin remote is not a GitHub URL: {remote_url}")
            repo_slug = f"{slug[0]}/{slug[1]}"

        reason = str(arguments.get("reason") or "").strip()
        if len(reason) > 400:
            reason = reason[:400]
        source = str(arguments.get("source") or "agent").strip() or "agent"
        owner, repo = repo_slug.split("/", 1)
        service_token = await auth_romaine_provider.token()
        grant = await _active_break_glass_grant(http, service_token, repo_slug)
        activation = _activate_break_glass_mcp_config(repo_slug, grant) if grant else None
        approval_url = _break_glass_approval_url(ORIGIN_SESSION_ID, repo_slug, reason, source)
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
                "target_ref": f"https://github.com/{repo_slug}",
                "repo_owner": owner,
                "repo_name": repo,
                "payload": {
                    "approval_url": approval_url,
                    "reason": reason,
                    "source": source,
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
                "repo": repo_slug,
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
                "repo": repo_slug,
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
    workflows = bool(arguments.get("workflows"))
    try:
        token = await _mint_github_installation_token(http, service_token, repo_slug, workflows=workflows)
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
            repo_slug = str((activation or {}).get("repo") or "")
            if not repo_slug:
                return _mcp_result_response(request_id, {"tools": []})
            service_token = await auth_romaine_provider.token()
            grant = await _active_break_glass_grant(http, service_token, repo_slug)
            if not grant:
                return _mcp_result_response(request_id, {"tools": []})
            return _mcp_result_response(request_id, _break_glass_tools_result())
        if method != "tools/call":
            return _mcp_error_response(request_id, -32601, f"unsupported method {method}")
        activation = _read_break_glass_activation()
        activated_repo = str((activation or {}).get("repo") or "")
        if not activated_repo:
            return _mcp_error_response(
                request_id,
                -32022,
                "break-glass MCP is not activated; call request_git_break_glass after approval",
            )
        name = params.get("name")
        arguments = params.get("arguments") or {}
        if not isinstance(arguments, dict):
            return _mcp_error_response(request_id, -32602, "arguments must be an object")
        if name == _BREAK_GLASS_MINT_TOKEN_TOOL:
            if str(arguments.get("repo") or "").strip() != activated_repo:
                return _mcp_error_response(request_id, -32602, f"repo must match activated break-glass repo {activated_repo}")
            return await _handle_break_glass_mint_token(http, auth_romaine_provider, request_id, arguments)
        if name == _BREAK_GLASS_PUSH_HEAD_TOOL:
            if not arguments.get("repo"):
                arguments = {**arguments, "repo": activated_repo}
            elif str(arguments.get("repo") or "").strip() != activated_repo:
                return _mcp_error_response(request_id, -32602, f"repo must match activated break-glass repo {activated_repo}")
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
            if method == "tools/call" and params.get("name") in {_TANK_PUBLISH_TOOL, _TANK_BREAK_GLASS_TOOL}:
                arguments = params.get("arguments") or {}
                if not isinstance(arguments, dict):
                    return _mcp_error_response(request_id, -32602, "arguments must be an object")
                record_proxy_request(mcp_label, 200)
                if params.get("name") == _TANK_PUBLISH_TOOL:
                    return await _handle_tank_publish_tool(http, tank_publish_provider, request_id, arguments)
                return await _handle_tank_break_glass_tool(http, tank_publish_provider, request_id, arguments)

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
                        if github_tool_call is not None or tank_tools_list:
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

            # mcp-tank-operator, mcp-glimmung, and mcp-grafana gate their tool
            # surface on the caller's auth.romaine.life service JWT (read
            # from X-Auth-Romaine-Token because Authorization is consumed
            # by kube-rbac-proxy in front of each, which strips it before
            # forwarding upstream). Inject the header so the upstreams
            # can attribute every call to the originating user.
            extra_header_provider = None
            if port in (TANK_OPERATOR_MCP_PORT, GLIMMUNG_MCP_PORT, GRAFANA_MCP_PORT):
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
            if port in (TANK_OPERATOR_MCP_PORT, GLIMMUNG_MCP_PORT) and ORIGIN_SESSION_ID:
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
                    github_activity_provider=auth_romaine_provider if port == GITHUB_MCP_PORT else None,
                    tank_publish_provider=auth_romaine_provider if port == TANK_OPERATOR_MCP_PORT else None,
                    glimmung_hot_swap_provider=auth_romaine_provider if port == GLIMMUNG_MCP_PORT else None,
                    block_github_write_tools=port == GITHUB_MCP_PORT,
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
