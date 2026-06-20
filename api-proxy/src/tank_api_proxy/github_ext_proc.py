"""GitHub egress ext_proc servicer — the IO shell around github_governor.

The Envoy listener TLS-terminates github.com / api.github.com and streams each
request here. This servicer is the side-effecting half (gRPC framing + httpx to
mcp-github and the control-actions endpoint); all the contract logic it calls is
the pure, unit-tested github_governor core.

Per stream:
  request_headers  -> read the relayed role=service JWT (Bearer for gh/REST, or
                      Basic x-access-token:<jwt> for git smart-HTTP), policy-check,
                      RELAY-mint the owner's App token via mcp-github (single
                      stateless tools/call, exactly as git-credential-tank.sh does),
                      and overwrite Authorization with it. The agent's JWT never
                      reaches GitHub; the minted token never reaches the agent.
  response_body    -> only buffered on the api.github.com route (REST). When the
                      request was a PR open/merge and it succeeded, record the
                      github.* control action against the session — the same record
                      an mcp-github PR produces, now also produced for a raw gh/git
                      PR. github.com (git) responses are NOT buffered (a clone/fetch
                      response is the whole packfile), so git pushes mint+inject but
                      record no PR, which is correct — a push is not a PR.

Soft by construction: if a request carries no parseable JWT we pass it through
untouched (the in-pod token still works during cutover). Enforcement (reject
unidentified egress) is the later NetworkPolicy phase, not this code.
"""

from __future__ import annotations

import base64
import logging
import time
import uuid
from typing import AsyncIterator

import httpx

from envoy.config.core.v3 import base_pb2
from envoy.service.ext_proc.v3 import external_processor_pb2 as ext_proc_pb2
from envoy.service.ext_proc.v3 import external_processor_pb2_grpc as ext_proc_grpc

from . import github_governor as gg

log = logging.getLogger(__name__)


def _peek_header(msg: "ext_proc_pb2.HttpHeaders", key: str) -> str:
    key = key.lower()
    for h in msg.headers.headers:
        if h.key.lower() == key:
            return (h.raw_value.decode() if h.raw_value else h.value) or ""
    return ""


def _peek_status(msg: "ext_proc_pb2.HttpHeaders") -> int:
    raw = _peek_header(msg, ":status")
    try:
        return int(raw)
    except (TypeError, ValueError):
        return 0


def _inject_auth_headers(host: str, token: str) -> list["base_pb2.HeaderValueOption"]:
    """Overwrite Authorization with the minted App token, in the shape each host
    expects: REST takes `token <t>`; git smart-HTTP takes Basic x-access-token."""
    if host == "api.github.com":
        raw = f"token {token}".encode()
    else:
        basic = base64.b64encode(f"x-access-token:{token}".encode()).decode()
        raw = f"Basic {basic}".encode()
    return [
        base_pb2.HeaderValueOption(
            header=base_pb2.HeaderValue(key="authorization", raw_value=raw),
            append_action=base_pb2.HeaderValueOption.OVERWRITE_IF_EXISTS_OR_ADD,
        )
    ]


class _Stream:
    """Per-request state carried from the request phase to the response phase."""

    __slots__ = ("ident", "decision", "action", "pr_number", "owner", "repo", "status")

    def __init__(self) -> None:
        self.ident: gg.SessionIdentity | None = None
        self.decision: gg.Decision | None = None
        self.action: str = ""
        self.pr_number: int | None = None
        self.owner: str = ""
        self.repo: str = ""
        self.status: int = 0


class GitHubGovernor(ext_proc_grpc.ExternalProcessorServicer):
    def __init__(self, *, mint_url: str, internal_url: str, exchange_url: str) -> None:
        self._mint_url = mint_url.rstrip("/")
        self._internal_url = internal_url.rstrip("/")
        self._exchange_url = exchange_url
        self._http = httpx.AsyncClient(timeout=25.0)
        # Exchanged role=service JWT cache, keyed by the pod's raw SA token. The
        # exchange auto-resolves the session owner; the result is valid ~15 min, so
        # caching collapses a request burst to one exchange (mirrors the sidecar).
        self._xchg: dict[str, tuple[str, float]] = {}
        # __main__/metrics compatibility (mirrors the AuthInjector surface the
        # entrypoint wires up); this servicer has no background keeper.
        self._keeper_task = None
        self._stopping = False

    # -- metrics/health surface expected by __main__ ------------------------
    def health_snapshot(self) -> dict:
        return {"provider": "github", "status": "ok"}

    def usage_snapshot(self) -> dict:
        return {}

    # -- ext_proc stream ----------------------------------------------------
    async def Process(  # noqa: N802 (grpc servicer method name)
        self,
        request_iterator: AsyncIterator["ext_proc_pb2.ProcessingRequest"],
        context,
    ) -> AsyncIterator["ext_proc_pb2.ProcessingResponse"]:
        st = _Stream()
        async for req in request_iterator:
            kind = req.WhichOneof("request")
            if kind == "request_headers":
                yield await self._on_request_headers(req.request_headers, st)
            elif kind == "response_headers":
                st.status = _peek_status(req.response_headers)
                yield ext_proc_pb2.ProcessingResponse(
                    response_headers=ext_proc_pb2.HeadersResponse()
                )
            elif kind == "response_body":
                yield await self._on_response_body(req.response_body, st)
            else:
                # request_body (only if a route buffers it) / trailers: pass through.
                yield ext_proc_pb2.ProcessingResponse()

    async def _on_request_headers(
        self, msg: "ext_proc_pb2.HttpHeaders", st: _Stream
    ) -> "ext_proc_pb2.ProcessingResponse":
        authority = _peek_header(msg, ":authority").split(":", 1)[0].lower()
        path = _peek_header(msg, ":path")
        method = _peek_header(msg, ":method")

        raw_token = gg.jwt_from_authorization(_peek_header(msg, "authorization"))
        if not raw_token:
            log.info("egress: pass-through (no credential) %s %s", method, path)
            return ext_proc_pb2.ProcessingResponse(request_headers=ext_proc_pb2.HeadersResponse())
        # The pod presents its RAW k8s SA token; exchange it for the session's
        # role=service JWT (auto-resolved owner) before parsing identity / minting.
        service_jwt = await self._exchange(raw_token)
        ident = gg.identity_from_jwt(service_jwt)
        if not ident.ok:
            # Soft cutover: a credential that doesn't exchange to a session identity
            # passes through untouched (the in-pod token still works during cutover).
            log.info("egress: pass-through (no session identity from exchange) %s %s", method, path)
            return ext_proc_pb2.ProcessingResponse(request_headers=ext_proc_pb2.HeadersResponse())
        st.ident = ident

        decision = gg.evaluate_policy(ident, method, authority, path)
        if not decision.allow:
            log.warning("egress: DENY session=%s %s %s (%s)", ident.session_id, method, path, decision.reason)
            return ext_proc_pb2.ProcessingResponse(
                immediate_response=ext_proc_pb2.ImmediateResponse(
                    status={"code": 403},
                    body=f"blocked by egress policy: {decision.reason}".encode(),
                )
            )
        st.decision = decision

        rec = gg.recordable_pr_action(method, authority, path)
        if rec is not None:
            st.action, st.pr_number = rec

        repo = gg.repo_from_path(authority, path)
        if repo is not None:
            st.owner, st.repo = repo
            token = await self._mint(ident, f"{repo[0]}/{repo[1]}", decision)
            if token:
                return ext_proc_pb2.ProcessingResponse(
                    request_headers=ext_proc_pb2.HeadersResponse(
                        response=ext_proc_pb2.CommonResponse(
                            header_mutation=ext_proc_pb2.HeaderMutation(
                                set_headers=_inject_auth_headers(authority, token),
                            ),
                        ),
                    )
                )
            log.warning("egress: mint returned no token for session=%s repo=%s/%s", ident.session_id, *repo)
        # No repo in path (e.g. /user, /meta) or mint failed: forward unmodified.
        return ext_proc_pb2.ProcessingResponse(request_headers=ext_proc_pb2.HeadersResponse())

    async def _on_response_body(
        self, msg: "ext_proc_pb2.HttpBody", st: _Stream
    ) -> "ext_proc_pb2.ProcessingResponse":
        passthrough = ext_proc_pb2.ProcessingResponse(response_body=ext_proc_pb2.BodyResponse())
        if not (st.ident and st.action and 200 <= st.status < 300):
            return passthrough
        number, html_url = gg.pr_fields_from_response_json(
            msg.body.decode("utf-8", "ignore") if msg.body else ""
        )
        if st.action == "github.pull_request.merge":
            pr_number = st.pr_number or number
        else:
            pr_number = number
        if not pr_number:
            return passthrough
        target_ref = html_url or f"https://github.com/{st.owner}/{st.repo}/pull/{pr_number}"
        await self._record(st, pr_number, target_ref)
        return passthrough

    # -- side effects (relayed with the session's own JWT) ------------------
    async def _exchange(self, raw_token: str) -> str:
        """Exchange the pod's raw k8s SA token for the session's role=service JWT.
        Empty-body POST to /api/auth/exchange/k8s; the IdP resolves the session's own
        owner. Cached per raw token until ~30s before expiry."""
        hit = self._xchg.get(raw_token)
        if hit and hit[1] > time.time() + 30:
            return hit[0]
        try:
            r = await self._http.post(
                self._exchange_url,
                headers={"Authorization": f"Bearer {raw_token}", "Content-Type": "application/json"},
                json={},
            )
            r.raise_for_status()
            token, expires_at = gg.parse_exchange_result(r.json())
        except Exception as exc:
            log.warning("egress: token exchange failed: %s", exc)
            return ""
        if token:
            self._xchg[raw_token] = (token, expires_at or (time.time() + 600))
        return token

    async def _mint(self, ident: gg.SessionIdentity, repo_full: str, decision: gg.Decision) -> str:
        payload = gg.mint_call_payload(repo_full, decision)
        try:
            r = await self._http.post(
                self._mint_url + "/",
                json=payload,
                headers={
                    "Authorization": f"Bearer {ident.raw_jwt}",
                    "Content-Type": "application/json",
                    "Accept": "application/json, text/event-stream",
                },
            )
            r.raise_for_status()
            token, _expires = gg.parse_mint_result(gg.first_json_object(r.text))
            return token
        except Exception as exc:
            log.warning("egress: mint failed session=%s repo=%s: %s", ident.session_id, repo_full, exc)
            return ""

    async def _record(self, st: _Stream, pr_number: int, target_ref: str) -> None:
        ident = st.ident
        assert ident is not None
        url = (
            gg.url_for_scope(ident.session_scope, self._internal_url)
            + f"/api/internal/sessions/{ident.session_id}/control-actions"
        )
        body = gg.build_record_body(
            invocation_id=f"ctrl_{uuid.uuid4().hex}",
            source_tool="git" if st.action == "github.pull_request.merge" else "rest",
            action=st.action,
            status="succeeded",
            target_ref=target_ref,
            repo_owner=st.owner,
            repo_name=st.repo,
            pr_number=pr_number,
        )
        try:
            r = await self._http.post(
                url,
                json=body,
                headers={"Authorization": f"Bearer {ident.raw_jwt}", "Content-Type": "application/json"},
            )
            r.raise_for_status()
            log.info("egress: recorded %s pr=%s session=%s", st.action, pr_number, ident.session_id)
        except Exception as exc:
            # Best-effort: the request already succeeded; never fail it on a record miss.
            log.warning("egress: record failed %s pr=%s session=%s: %s", st.action, pr_number, ident.session_id, exc)
