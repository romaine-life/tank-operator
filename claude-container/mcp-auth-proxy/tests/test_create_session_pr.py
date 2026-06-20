from __future__ import annotations

import asyncio
import json
import subprocess

import pytest

from mcp_auth_proxy.server import _handle_create_session_pr_wrapper


def _dumps(value) -> bytes:
    return json.dumps(value).encode("utf-8")


def _sse(obj: dict) -> bytes:
    return b"event: message\ndata: " + _dumps(obj) + b"\n\n"


_MINT_TOKEN_SSE = _sse({"jsonrpc": "2.0", "result": {"structuredContent": {"token": "github-token"}}})


def _create_pr_sse(number: int) -> bytes:
    url = f"https://github.com/romaine-life/tank-operator/pull/{number}"
    return _sse(
        {
            "jsonrpc": "2.0",
            "result": {
                "content": [{"type": "text", "text": f"Opened {url}"}],
                "structuredContent": {"html_url": url, "number": number},
            },
        }
    )


def _err_sse(message: str) -> bytes:
    return _sse({"jsonrpc": "2.0", "error": {"code": -32000, "message": message}})


class _Resp:
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


class _CreatePRHTTP:
    """Routes the HTTP the create-session-PR handler makes.

    - mcp-github POST -> mint_clone_token (token) or create_pull_request (PR url / error)
    - GitHub API GET  -> /pulls?head= (open-PR list) and /repos/<o>/<r> (default_branch)
    - Tank internal POST -> control-actions / pull-request-link (recorded)
    """

    def __init__(self, *, open_prs=None, create_error=None, open_prs_after_create=None) -> None:
        self._open_prs = open_prs if open_prs is not None else []
        self._open_prs_after_create = open_prs_after_create
        self._create_error = create_error
        self._create_attempted = False
        self.create_calls: list[dict] = []
        self.control_actions: list[dict] = []
        self.pr_links: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        name = (json.get("params") or {}).get("name") if isinstance(json, dict) else None
        if "mcp-github" in url:
            if name == "mint_clone_token":
                return _Resp(200, _MINT_TOKEN_SSE)
            if name == "create_pull_request":
                self._create_attempted = True
                self.create_calls.append(json["params"]["arguments"])
                if self._create_error is not None:
                    return _Resp(200, _err_sse(self._create_error))
                return _Resp(200, _create_pr_sse(1234))
            return _Resp(200, b"{}")
        if "control-actions" in url:
            self.control_actions.append(json)
            return _Resp(200, b"{}")
        if "pull-request-link" in url:
            self.pr_links.append(json)
            return _Resp(200, b"{}")
        return _Resp(200, b"{}")

    def request(self, method: str, url: str, *, headers: dict, **kwargs):
        if "/pulls?head=" in url:
            prs = self._open_prs
            if self._create_attempted and self._open_prs_after_create is not None:
                prs = self._open_prs_after_create
            return _Resp(200, _dumps(prs))
        # repo metadata for default_branch
        return _Resp(200, _dumps({"default_branch": "main"}))


class _Tok:
    async def token(self) -> str:
        return "service-token"


class _Req:
    def __init__(self, body: dict) -> None:
        self._body = json.dumps(body).encode("utf-8")

    async def read(self) -> bytes:
        return self._body


def _make_repo(tmp_path, branch: str = "tank/session/95/tank-operator"):
    repo = tmp_path / "tank-operator"
    repo.mkdir()
    subprocess.run(["git", "init"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "config", "user.email", "agent@example.test"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "Agent"], cwd=repo, check=True)
    (repo / "README.md").write_text("test\n", encoding="utf-8")
    subprocess.run(["git", "add", "README.md"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "initial"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "checkout", "-b", branch], cwd=repo, check=True, stdout=subprocess.DEVNULL)
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
    monkeypatch.setattr("mcp_auth_proxy.server.RESTRICTED_GIT_ENABLED", True)


def _run(http, repo, **body):
    payload = {"repo_path": str(repo)}
    payload.update(body)
    response = asyncio.run(_handle_create_session_pr_wrapper(http, _Tok(), _Req(payload)))
    return response.status, json.loads(response.text)


def test_creates_draft_pr_when_absent(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _CreatePRHTTP(open_prs=[])
    status, body = _run(http, repo, title="My change", body="Why")
    assert status == 200
    assert body == {
        "ok": True,
        "created": True,
        "pr_url": "https://github.com/romaine-life/tank-operator/pull/1234",
        "pr_number": 1234,
    }
    # One governed create, opened as a draft on the session branch against main.
    assert len(http.create_calls) == 1
    args = http.create_calls[0]
    assert args["draft"] is True
    assert args["base"] == "main"
    assert args["head"] == "tank/session/95/tank-operator"
    assert args["owner"] == "romaine-life" and args["name"] == "tank-operator"
    assert args["title"] == "My change" and args["body"] == "Why"
    # Audit + UI link recorded.
    assert any(a.get("action") == "github.pull_request.open" for a in http.control_actions)
    assert http.pr_links and http.pr_links[0]["url"].endswith("/pull/1234")


def test_idempotent_when_open_pr_exists(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _CreatePRHTTP(
        open_prs=[{"number": 1113, "html_url": "https://github.com/romaine-life/tank-operator/pull/1113"}]
    )
    status, body = _run(http, repo)
    assert status == 200
    assert body["ok"] is True and body["created"] is False
    assert body["pr_number"] == 1113
    # No create attempted, no duplicate PR.
    assert http.create_calls == []


def test_create_race_already_exists_returns_existing(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _CreatePRHTTP(
        open_prs=[],
        create_error="A pull request already exists for romaine-life:tank/session/95/tank-operator.",
        open_prs_after_create=[
            {"number": 1113, "html_url": "https://github.com/romaine-life/tank-operator/pull/1113"}
        ],
    )
    status, body = _run(http, repo)
    assert status == 200
    assert body["ok"] is True and body["created"] is False
    assert body["pr_number"] == 1113


def test_no_commits_between_is_clean_422(tmp_path) -> None:
    repo = _make_repo(tmp_path)
    http = _CreatePRHTTP(open_prs=[], create_error="No commits between main and tank/session/95/tank-operator")
    status, body = _run(http, repo)
    assert status == 422
    assert body["ok"] is False
    assert "commit your work first" in body["reason"]


def test_rejects_non_session_branch(tmp_path) -> None:
    repo = _make_repo(tmp_path, branch="feature/not-governed")
    http = _CreatePRHTTP(open_prs=[])
    status, body = _run(http, repo)
    assert status == 400
    assert body["ok"] is False
    assert "not a Tank session branch" in body["reason"]
    assert http.create_calls == []


def test_requires_restricted_git(tmp_path, monkeypatch) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.RESTRICTED_GIT_ENABLED", False)
    repo = _make_repo(tmp_path)
    http = _CreatePRHTTP(open_prs=[])
    status, body = _run(http, repo)
    assert status == 400
    assert body["ok"] is False
