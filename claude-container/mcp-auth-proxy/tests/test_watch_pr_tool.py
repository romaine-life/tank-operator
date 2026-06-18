from __future__ import annotations

import asyncio
import json
import subprocess

import pytest

from mcp_auth_proxy.server import _handle_tank_watch_pr_tool


def _dumps(value) -> bytes:
    return json.dumps(value).encode("utf-8")


_MINT_TOKEN_SSE = (
    b"event: message\n"
    b'data: {"jsonrpc":"2.0","result":{"structuredContent":{"token":"github-token"}}}\n\n'
)


class _Resp:
    """Minimal async-context-manager response matching the shape the tool reads
    via _github_api_json / _mint_github_installation_token / the backend POST."""

    def __init__(self, status: int, body: bytes) -> None:
        self.status = status
        self._body = body

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_args):
        return False

    async def read(self) -> bytes:
        return self._body

    async def text(self) -> str:
        return self._body.decode("utf-8")


class _WatchHTTP:
    """Routes the backend call _handle_tank_watch_pr_tool makes.

    pr_detail_sequence/check_runs are used to synthesize the backend
    /pr-readiness response in tests. The tool itself no longer resolves PR
    detail; the backend reducer owns live CI and mergeability state.
    """

    def __init__(self, *, pr_detail_sequence, check_runs, pr_list=None, watch_response=None) -> None:
        self._pr_detail_sequence = list(pr_detail_sequence)
        self._check_runs = list(check_runs)
        self._watch_response = watch_response
        self._pr_list = pr_list if pr_list is not None else [
            {
                "number": 1113,
                "head": {"sha": "headsha"},
                "html_url": "https://github.com/romaine-life/tank-operator/pull/1113",
            }
        ]
        self.posts: list[dict] = []
        self.pr_detail_calls = 0

    def post(self, url: str, *, headers: dict, json: dict):
        self.posts.append({"url": url, "json": json})
        if "mcp-github" in url:
            return _Resp(200, _MINT_TOKEN_SSE)
        if "pr-readiness" in url:
            return _Resp(200, _dumps(self._backend_watch_response(json)))
        return _Resp(200, b"{}")

    def _backend_watch_response(self, watch_payload: dict) -> dict:
        if self._watch_response is not None:
            return self._watch_response
        mergeable, mergeable_state = self._pr_detail_sequence[0]
        failed = [
            str(run.get("name") or "check")
            for run in self._check_runs
            if run.get("status") == "completed" and run.get("conclusion") not in {"success", "skipped", "neutral"}
        ]
        pending = [str(run.get("name") or "check") for run in self._check_runs if run.get("status") != "completed"]
        check_state = "failure" if failed else "pending" if pending or not self._check_runs else "success"
        if mergeable_state in {"dirty", "behind"}:
            state = "conflict"
            detail = f"PR #{watch_payload.get('pr_number') or 1113} needs a rebase onto its base (mergeable_state={mergeable_state})."
        elif failed:
            state = "failed"
            detail = f"Required checks failed: {', '.join(failed[:8])}."
        elif mergeable is True and mergeable_state == "clean" and check_state == "success":
            state = "ready"
            detail = f"PR #{watch_payload.get('pr_number') or 1113} is green and mergeable, awaiting human merge in Tank."
        else:
            state = "watching"
            detail = f"CI in progress (mergeable_state={mergeable_state or 'unknown'}, checks={check_state})."
        return {
            "state": state,
            "detail": detail,
            "repo": watch_payload.get("repo") or "romaine-life/tank-operator",
            "pr_number": watch_payload.get("pr_number") or 1113,
            "head_sha": watch_payload.get("expected_head_sha") or "headsha",
            "pr_url": watch_payload.get("pr_url") or "https://github.com/romaine-life/tank-operator/pull/1113",
            "mergeable_state": mergeable_state,
            "check_state": check_state,
            "failing_checks": failed,
        }

    def request(self, method: str, url: str, *, headers: dict, **kwargs):
        if "/pulls?head=" in url:
            return _Resp(200, _dumps(self._pr_list))
        if "/check-runs" in url:
            return _Resp(200, _dumps({"check_runs": self._check_runs}))
        if "/status" in url:
            return _Resp(200, _dumps({"state": "pending", "statuses": []}))
        if "/pulls/" in url and "/commits" in url:
            return _Resp(200, _dumps([]))
        if "/pulls/" in url:
            idx = min(self.pr_detail_calls, len(self._pr_detail_sequence) - 1)
            mergeable, mergeable_state = self._pr_detail_sequence[idx]
            self.pr_detail_calls += 1
            return _Resp(
                200,
                _dumps(
                    {
                        "html_url": "https://github.com/romaine-life/tank-operator/pull/1113",
                        "mergeable": mergeable,
                        "mergeable_state": mergeable_state,
                        "head": {"sha": "headsha", "ref": "tank/session/95/tank-operator"},
                    }
                ),
            )
        return _Resp(404, b"{}")


class _Tok:
    async def token(self) -> str:
        return "service-token"


def _make_repo(tmp_path):
    repo = tmp_path / "tank-operator"
    repo.mkdir()
    subprocess.run(["git", "init"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "config", "user.email", "agent@example.test"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "Agent"], cwd=repo, check=True)
    (repo / "README.md").write_text("test\n", encoding="utf-8")
    subprocess.run(["git", "add", "README.md"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "initial"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(
        ["git", "checkout", "-b", "tank/session/95/tank-operator"],
        cwd=repo,
        check=True,
        stdout=subprocess.DEVNULL,
    )
    subprocess.run(
        ["git", "remote", "add", "origin", "https://github.com/romaine-life/tank-operator.git"],
        cwd=repo,
        check=True,
    )
    return repo


@pytest.fixture(autouse=True)
def _session_env(monkeypatch, tmp_path):
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)


def _structured(http, repo, **extra):
    args = {"repo_path": str(repo), "pr_number": 1113}
    args.update(extra)
    response = asyncio.run(_handle_tank_watch_pr_tool(http, _Tok(), 7, args))
    payload = json.loads(response.text)
    return payload["result"]["structuredContent"]


def _registered(http) -> bool:
    return any("pr-readiness" in p["url"] for p in http.posts)


def test_watch_pr_reports_conflict_on_dirty(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(False, "dirty")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "conflict"
    assert _registered(http) is True


def test_watch_pr_reports_failed_on_failing_check(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "unstable")],
        check_runs=[{"name": "build", "status": "completed", "conclusion": "failure"}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "failed"
    assert "build" in structured["failing_checks"]
    assert _registered(http) is True


def test_watch_pr_reports_ready_when_green(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "clean")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "ready"
    assert _registered(http) is True
    watch_post = next(p for p in http.posts if "pr-readiness" in p["url"])
    assert watch_post["json"]["status"] == "watching"
    assert watch_post["json"]["check_state"] == "pending"


def test_watch_pr_registers_watch_when_pending(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "clean")],
        check_runs=[{"name": "build", "status": "in_progress", "conclusion": None}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "watching"
    assert _registered(http) is True
    watch_post = next(p for p in http.posts if "pr-readiness" in p["url"])
    assert watch_post["json"]["repo"] == "romaine-life/tank-operator"
    assert watch_post["json"]["pr_number"] == 1113
    assert watch_post["json"]["expected_head_sha"]
    assert structured["head_sha"] == watch_post["json"]["expected_head_sha"]


def test_watch_pr_hands_mergeability_poll_to_backend(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(None, "unknown"), (True, "clean")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
        watch_response={
            "state": "watching",
            "detail": "CI in progress (mergeable_state=unknown, checks=success).",
            "head_sha": "headsha",
            "pr_url": "https://github.com/romaine-life/tank-operator/pull/1113",
            "mergeable_state": "unknown",
            "check_state": "success",
            "failing_checks": [],
        },
    )
    structured = _structured(http, repo)
    assert http.pr_detail_calls == 0
    assert structured["state"] == "watching"


def test_watch_pr_resolves_pr_number_from_open_pr(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "clean")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    # No pr_number argument -> Tank resolves the open PR for the branch head.
    response = asyncio.run(_handle_tank_watch_pr_tool(http, _Tok(), 7, {"repo_path": str(repo)}))
    structured = json.loads(response.text)["result"]["structuredContent"]
    assert structured["pr_number"] == 1113
    assert structured["state"] == "ready"
    assert http.pr_detail_calls == 0
