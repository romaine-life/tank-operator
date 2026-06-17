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
    """Routes the GitHub + backend calls _handle_tank_watch_pr_tool makes.

    pr_detail_sequence is a list of (mergeable, mergeable_state) returned one per
    GET /pulls/{n} call, so a test can model GitHub resolving mergeable_state
    from 'unknown' across polls.
    """

    def __init__(self, *, pr_detail_sequence, check_runs, pr_list=None) -> None:
        self._pr_detail_sequence = list(pr_detail_sequence)
        self._check_runs = list(check_runs)
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
        return _Resp(200, b"{}")

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
    return any("ci-watches" in p["url"] for p in http.posts)


def test_watch_pr_reports_conflict_on_dirty(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(False, "dirty")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "conflict"
    assert _registered(http) is False


def test_watch_pr_reports_failed_on_failing_check(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "unstable")],
        check_runs=[{"name": "build", "status": "completed", "conclusion": "failure"}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "failed"
    assert "build" in structured["failing_checks"]
    assert _registered(http) is False


def test_watch_pr_reports_ready_when_green(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "clean")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "ready"
    assert _registered(http) is True
    watch_post = next(p for p in http.posts if "ci-watches" in p["url"])
    assert watch_post["json"]["status"] == "ready"
    assert watch_post["json"]["check_state"] == "success"


def test_watch_pr_registers_watch_when_pending(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "clean")],
        check_runs=[{"name": "build", "status": "in_progress", "conclusion": None}],
    )
    structured = _structured(http, repo)
    assert structured["state"] == "watching"
    assert _registered(http) is True
    watch_post = next(p for p in http.posts if "ci-watches" in p["url"])
    assert watch_post["json"]["pr_owner"] == "romaine-life"
    assert watch_post["json"]["pr_name"] == "tank-operator"
    assert watch_post["json"]["pr_number"] == 1113
    assert watch_post["json"]["head_sha"] == "headsha"


def test_watch_pr_polls_until_mergeable_state_resolves(monkeypatch, tmp_path) -> None:
    # GitHub returns mergeable=null / mergeable_state='unknown' right after a push;
    # the tool must keep reading until it resolves rather than trusting the first
    # read. This is the fix for "reports it's good while the PR has a conflict".
    async def _no_sleep(*_a, **_k):
        return None

    monkeypatch.setattr("mcp_auth_proxy.server.asyncio.sleep", _no_sleep)
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(None, "unknown"), (True, "clean")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    structured = _structured(http, repo)
    assert http.pr_detail_calls == 2  # polled past the unresolved first read
    assert structured["state"] == "ready"


def test_watch_pr_resolves_pr_number_from_open_pr(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _WatchHTTP(
        pr_detail_sequence=[(True, "clean")],
        check_runs=[{"name": "test", "status": "completed", "conclusion": "success"}],
    )
    # No pr_number argument -> the tool resolves the open PR for the branch head.
    response = asyncio.run(_handle_tank_watch_pr_tool(http, _Tok(), 7, {"repo_path": str(repo)}))
    structured = json.loads(response.text)["result"]["structuredContent"]
    assert structured["pr_number"] == 1113
    assert structured["state"] == "ready"
