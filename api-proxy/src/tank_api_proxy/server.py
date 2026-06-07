"""Envoy ext_proc service that injects managed OAuth into outbound requests.

The Envoy listener in front of the provider API calls this service via the
``ExternalProcessor.Process`` bidirectional stream once per HTTP transaction.
We act on two messages per stream:

  - ``request_headers``: if the inbound ``authorization`` header is the
    bootstrap placeholder ``Bearer managed-by-tank-operator``, swap it for
    the current real OAuth access token from cache. If it's anything else
    (typically a worker_jwt for the v2 remote-control bridge — see below),
    pass the request through untouched.
  - ``response_headers``: peek ``:status``. On 401 *for a request we
    actually injected into*, kick off a background refresh — but only if
    one isn't already in flight (single-flight guard prevents thundering-
    herd rotation storms when many concurrent requests all see 401).
    Envoy's per-route retry policy resends the request; the retry's
    request_headers callback awaits the in-flight refresh task before
    injecting, so the retry always sees a fresh token.

Why two distinct credentials in claude-code:
  * OAuth access token: long-lived (8h), pod-resident only as a placeholder,
    real value lives in this proxy's cache. Used for /v1/messages,
    /v1/code/sessions, /v1/code/sessions/{id}/bridge, /archive, /events,
    /api/oauth/*, /api/claude_code/*, etc.
  * worker_jwt: short-lived, returned by the /bridge POST response, kept
    only in claude-code memory. Used for the v2 remote-control bridge
    endpoints: GET /v1/code/sessions/{id}/worker, PUT same, GET
    /v1/code/sessions/{id}/worker/events/stream. These endpoints reject
    the OAuth Bearer outright with 401, so it's load-bearing that the
    proxy NOT clobber the Authorization on these calls.

State:
  - ``_cached_access`` / ``_cached_refresh``: in-memory copy of the latest
    tokens. Initialized lazily from the mounted credentials.json the first
    time we need them, refreshed in place by ``_refresh()``.
  - ``_refresh_task``: most recent refresh task. Its presence + not done()
    is the single-flight token; ``_get_access_token`` awaits it when
    ``_access_invalidated`` is set.
  - ``_lock``: serializes the actual rotation HTTP call so concurrent waiters
    all see the same fresh token (and so a stale-file reload in ``_refresh``
    is atomic with the rotate).

KV write failure is non-fatal: see the comment on ``_persist_to_kv``.

Anthropic-side gating gotcha (burned us during rollout verification):
api.anthropic.com's /v1/messages endpoint refuses subscription OAuth tokens
unless TWO request headers are set, and the error messages are misleading:

  - Without ``anthropic-beta: oauth-2025-04-20`` →
    401 "OAuth authentication is currently not supported" (it IS supported,
    just gated behind the beta opt-in).
  - Without ``anthropic-dangerous-direct-browser-access: true`` →
    401 "Invalid authentication credentials" even on a freshly-minted token.

claude-code itself sends both headers natively, so traffic from session
pods works without this proxy adding them. We do NOT inject these here:
doing so would mask a future claude-code header change rather than
surfacing it. If you're synthetically curling api.anthropic.com to test
this proxy, set both headers; if a real claude-code session 401s when
synthetic curls succeed, the gate has moved and claude-code needs a bump.
The beta string above is hardcoded in claude-code's bundled JS as of
April 2026 — Anthropic rotating it would silently break direct callers
but not claude-code, which ships the matching value.

Codex uses the same proxy primitive with a different token authority and a
different pod-side auth shape. Session pods write a synthetic
``~/.codex/auth.json`` with ``auth_mode=chatgptAuthTokens`` and
``access_token=managed-by-tank-operator``. In current Codex, that mode does
not proactively refresh from auth.json; it simply emits the bearer in API
headers. This proxy swaps the placeholder for the real ChatGPT access token,
overwrites ``ChatGPT-Account-ID`` from the centrally mounted auth.json, and
single-flight-refreshes against auth.openai.com on upstream 401.
"""
from __future__ import annotations

import asyncio
import base64
from dataclasses import dataclass
from datetime import datetime, timezone
import json
import logging
import os
import time
from typing import Any, AsyncIterator

import grpc
import httpx
from azure.identity.aio import DefaultAzureCredential
from azure.keyvault.secrets.aio import SecretClient

from envoy.service.ext_proc.v3 import external_processor_pb2 as ext_proc_pb2
from envoy.service.ext_proc.v3 import external_processor_pb2_grpc as ext_proc_grpc
from envoy.config.core.v3 import base_pb2
from envoy.type.v3 import http_status_pb2

from .metrics import (
    record_ext_proc_request,
    record_kv_persist,
    record_refresh,
    record_single_flight_wait,
    record_upstream_status,
)

log = logging.getLogger(__name__)

# Hardcoded into Claude Code's bundled JS. Two distinct client_ids ship in
# the bundle: 22422756-... is paired with the older console.anthropic.com
# endpoint, 9d1c250a-... with platform.claude.com (our token URL). Tied
# here by the MANUAL_REDIRECT_URL/TOKEN_URL pairing in cli.js. The token
# URL is intentionally NOT routed through the proxy itself — the proxy
# fronts api.anthropic.com, not platform.claude.com.
ANTHROPIC_CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
ANTHROPIC_TOKEN_URL = "https://platform.claude.com/v1/oauth/token"

# Codex ChatGPT OAuth constants from openai/codex's login crate:
# codex-rs/login/src/auth/manager.rs.
CODEX_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
CODEX_TOKEN_URL = "https://auth.openai.com/oauth/token"

# Antigravity (Gemini-Ultra via Google's `agy` CLI) authenticates with a
# standard Google OAuth2 "consumer" chain. The client_id below is the consumer
# OAuth client embedded in the public agy binary (a second, enterprise client
# also ships but is not the consumer flow). Confirmed by refreshing a live
# consumer refresh_token against Google's token endpoint. The client_secret is
# an installed-app secret (also embedded in the public binary, hence not
# confidential) but is sourced from env so it stays out of source control.
# Unlike the Claude/Codex custom OAuth servers, Google's /token endpoint
# requires application/x-www-form-urlencoded, not JSON (token_request_form).
GOOGLE_CLIENT_ID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
GOOGLE_TOKEN_URL = "https://oauth2.googleapis.com/token"

# The session launchers write this placeholder into
# ~/.claude/.credentials.json's accessToken (and matching refreshToken).
# Used as the discriminator for "this is a request that wants OAuth-
# Bearer injection" — anything else (worker_jwt, missing, future
# unknowns) passes through with its Authorization untouched.
PLACEHOLDER_BEARER = "Bearer managed-by-tank-operator"

# Proactive refresh keeper tuning. Refresh when the access token is within
# REFRESH_SKEW_MS of expiry so the first/next request never rides an expired
# token, and re-check at most every PROACTIVE_REFRESH_POLL_SECONDS (the keeper
# also wakes immediately on an upstream 401). This is what makes the proxy
# survive its own restart on a low-traffic provider: without continuous
# traffic to trigger a reactive 401-refresh, the keeper warms the token on
# boot and keeps it warm, and it runs the refresh + KV write in a long-lived
# task that a short-lived agy request stream cannot cancel mid-flight.
REFRESH_SKEW_MS = 10 * 60 * 1000
PROACTIVE_REFRESH_POLL_SECONDS = 60

@dataclass(frozen=True)
class ProxyConfig:
    provider: str
    credentials_file: str
    token_url: str
    client_id: str
    kv_secret_name: str
    client_secret: str | None = None
    account_header: str | None = None
    fedramp_header: str | None = None
    patch_last_refresh: bool = False
    # When True, POST the token refresh as application/x-www-form-urlencoded
    # (Google's OAuth2 /token contract) instead of JSON (Claude/Codex).
    token_request_form: bool = False


def _config_from_env() -> ProxyConfig:
    provider = _required_env("PROXY_PROVIDER").strip().lower()
    if provider == "codex":
        return ProxyConfig(
            provider="codex",
            credentials_file=_required_env("CODEX_CREDENTIALS_FILE"),
            token_url=os.environ.get("CODEX_TOKEN_URL", CODEX_TOKEN_URL),
            client_id=os.environ.get("CODEX_CLIENT_ID", CODEX_CLIENT_ID),
            kv_secret_name=_required_env("CODEX_CREDENTIALS_KV_KEY"),
            account_header="ChatGPT-Account-ID",
            fedramp_header="X-OpenAI-Fedramp",
            patch_last_refresh=True,
        )
    if provider == "antigravity":
        return ProxyConfig(
            provider="antigravity",
            credentials_file=_required_env("ANTIGRAVITY_CREDENTIALS_FILE"),
            token_url=os.environ.get("ANTIGRAVITY_TOKEN_URL", GOOGLE_TOKEN_URL),
            client_id=os.environ.get("ANTIGRAVITY_CLIENT_ID", GOOGLE_CLIENT_ID),
            client_secret=_required_env("ANTIGRAVITY_CLIENT_SECRET"),
            kv_secret_name=_required_env("ANTIGRAVITY_CREDENTIALS_KV_KEY"),
            token_request_form=True,
        )
    if provider != "claude":
        raise RuntimeError(f"unknown PROXY_PROVIDER={provider!r}")
    return ProxyConfig(
        provider="claude",
        credentials_file=_required_env("CLAUDE_CREDENTIALS_FILE"),
        token_url=os.environ.get("CLAUDE_TOKEN_URL", ANTHROPIC_TOKEN_URL),
        client_id=os.environ.get("CLAUDE_CLIENT_ID", ANTHROPIC_CLIENT_ID),
        kv_secret_name=_required_env("CLAUDE_CREDENTIALS_KV_KEY"),
    )


def _required_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def _walk_for(blob: Any, names: tuple[str, ...]) -> str | None:
    if not isinstance(blob, dict):
        return None
    for k, v in blob.items():
        if k in names and isinstance(v, str):
            return v
        if isinstance(v, dict):
            found = _walk_for(v, names)
            if found:
                return found
    return None


def _patch_blob(
    blob: dict[str, Any],
    new_access: str,
    new_refresh: str,
    expires_in: int,
    *,
    new_id: str | None = None,
    patch_last_refresh: bool = False,
) -> dict[str, Any]:
    expires_at_ms = int((time.time() + expires_in) * 1000)
    expiry_rfc3339 = (
        datetime.fromtimestamp(time.time() + expires_in, timezone.utc)
        .isoformat()
        .replace("+00:00", "Z")
    )
    last_refresh = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    out = json.loads(json.dumps(blob))

    def walk(node: Any) -> None:
        if not isinstance(node, dict):
            return
        for key in list(node.keys()):
            if key in ("accessToken", "access_token"):
                node[key] = new_access
            elif key in ("refreshToken", "refresh_token"):
                node[key] = new_refresh
            elif new_id is not None and key in ("idToken", "id_token"):
                node[key] = new_id
            elif key in ("expiresAt", "expires_at"):
                node[key] = expires_at_ms
            elif key == "expiry":
                # Google (antigravity) blob: RFC3339 string, not epoch ms.
                node[key] = expiry_rfc3339
            elif patch_last_refresh and key == "last_refresh":
                node[key] = last_refresh
            elif isinstance(node[key], dict):
                walk(node[key])

    walk(out)
    if patch_last_refresh:
        out["last_refresh"] = last_refresh
    return out


def _jwt_exp_ms(token: str | None) -> int | None:
    claims = _jwt_payload(token)
    exp = claims.get("exp")
    if isinstance(exp, (int, float)):
        return int(exp * 1000)
    return None


def _jwt_payload(token: str | None) -> dict[str, Any]:
    if not token:
        return {}
    parts = token.split(".")
    if len(parts) < 2:
        return {}
    payload = parts[1]
    padding = "=" * (-len(payload) % 4)
    try:
        decoded = base64.urlsafe_b64decode((payload + padding).encode())
        claims = json.loads(decoded)
    except Exception:
        return {}
    return claims if isinstance(claims, dict) else {}


def _iso_ms(value: str | None) -> int | None:
    if not value:
        return None
    try:
        normalized = value.replace("Z", "+00:00")
        dt = datetime.fromisoformat(normalized)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return int(dt.timestamp() * 1000)
    except ValueError:
        return None


# User-facing copy for known OAuth refresh-failure reasons. The /health
# endpoint surfaces these strings directly; the orchestrator's
# session.status:failed banner copies them verbatim into the transcript.
# Keep the strings actionable — every entry should answer "what does the
# user do next" since the action affordance ("Re-sign-in to ...") is
# generic.
_REFRESH_FAILURE_TEXT = {
    "refresh_token_reused": "Sign-in expired. The refresh token has already been used; re-authenticate to restore service.",
    "invalid_grant": "Sign-in expired. Re-authenticate to restore service.",
    "invalid_request": "Sign-in could not be refreshed. Re-authenticate to restore service.",
    "unauthorized_client": "Sign-in is not authorized to refresh. Re-authenticate to restore service.",
}


def _classify_refresh_failure(resp: httpx.Response) -> tuple[str, str]:
    """Extract a (reason, text) tuple from an OAuth /token error response.

    The upstream body shape is the standard OAuth error envelope:
        {"error": {"code": "refresh_token_reused", "message": "..."}}
    For non-JSON bodies (rare; upstream proxies misbehaving), fall back
    to the HTTP status as the reason. The reason field is what feeds
    the orchestrator's metric label and Layer 1 row; text is what shows
    in the transcript banner.
    """
    reason = ""
    text = ""
    try:
        body = resp.json()
    except Exception:
        body = None
    if isinstance(body, dict):
        err = body.get("error")
        if isinstance(err, dict):
            code = err.get("code")
            message = err.get("message")
            if isinstance(code, str) and code:
                reason = code
            if isinstance(message, str) and message:
                text = message
        elif isinstance(err, str) and err:
            reason = err
    if not reason:
        reason = f"http_{resp.status_code}"
    if not text:
        text = _REFRESH_FAILURE_TEXT.get(reason, "Sign-in could not be refreshed. Re-authenticate to restore service.")
    else:
        # If we got an upstream message AND the reason is one we have
        # canonical copy for, prefer the canonical copy — the upstream
        # message is often referrer-style ("Please try signing in again.")
        # and lands awkwardly in the SPA's banner.
        canonical = _REFRESH_FAILURE_TEXT.get(reason)
        if canonical:
            text = canonical
    return reason, text


class AuthInjector(ext_proc_grpc.ExternalProcessorServicer):
    def __init__(self, config: ProxyConfig | None = None) -> None:
        self._config = config or _config_from_env()
        self._cached_access: str | None = None
        self._cached_refresh: str | None = None
        self._cached_account_id: str | None = None
        self._cached_fedramp: bool = False
        self._cached_blob: dict[str, Any] | None = None
        self._lock = asyncio.Lock()
        # Set when an upstream 401 is observed for the current cached
        # token. The next request_headers callback awaits the in-flight
        # refresh task before injecting; once refresh updates _cached_access
        # we clear the flag so subsequent requests don't block.
        self._access_invalidated = False
        # Most recent refresh task, retained as a single-flight handle:
        # _on_response_headers consults `_refresh_task is None or .done()`
        # before scheduling a new one, and _get_access_token awaits it
        # when the cached token is invalidated. Without this dedupe, N
        # concurrent 401s would each schedule their own _refresh(), each
        # successive rotation would single-use-invalidate its predecessor's
        # refresh token, and the proxy logs would show a "rotation storm"
        # of five+ successful rotations in two seconds.
        self._refresh_task: asyncio.Task[None] | None = None
        # Proactive refresh keeper plumbing. The keeper is a long-lived task
        # (started in serve()) that warms the token on boot and before expiry,
        # so a request never rides an expired token (the cold-start race) and so
        # the refresh + KV write run in a task a short-lived request stream
        # cannot cancel mid-flight. _refresh_wakeup lets the reactive 401 path
        # nudge the keeper to run immediately instead of waiting for the poll.
        self._refresh_wakeup = asyncio.Event()
        self._keeper_task: asyncio.Task[None] | None = None
        self._stopping = False
        self._kv_url = os.environ.get("AZURE_KEYVAULT_URL", "")
        # Health snapshot — the durable provider-credential health surface
        # consumed by tank-operator's poller. The orchestrator polls
        # /health/<provider> on a 30s interval, debounces sustained
        # failures, and writes provider_credential_health rows + fans
        # session.status:failed events into every affected session's
        # transcript. See docs/features/transcript/contract.md for the
        # surface. The proxy is a stateless monitor; durability lives in
        # Postgres on the orchestrator side.
        self._health_last_attempted_at: float | None = None
        self._health_last_succeeded_at: float | None = None
        self._health_last_result: str = "unknown"
        self._health_last_reason: str = ""
        self._health_last_text: str = ""
        self._health_attempt_id: int = 0
        log.info(
            "starting %s auth injector (credentials=%s, kv_secret=%s)",
            self._config.provider,
            self._config.credentials_file,
            self._config.kv_secret_name,
        )

    async def Process(
        self,
        request_iterator: AsyncIterator[ext_proc_pb2.ProcessingRequest],
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[ext_proc_pb2.ProcessingResponse]:
        # Per-stream state: did we inject our OAuth Bearer on the request
        # side of this transaction? If not (e.g. the call carried a
        # worker_jwt), we must not interpret a 401 here as "our cached
        # token went stale" — it's about the inbound credential we
        # didn't touch, not ours.
        injected = False
        async for req in request_iterator:
            kind = req.WhichOneof("request")
            if kind == "request_headers":
                response, injected = await self._on_request_headers(req.request_headers)
                yield response
            elif kind == "response_headers":
                yield await self._on_response_headers(req.response_headers, injected)
            else:
                # Body / trailers / unknown: pass through unmodified. We
                # configured the filter to skip body streaming, so the only
                # path that lands here is the trailers message envoy emits
                # at end-of-stream.
                yield ext_proc_pb2.ProcessingResponse()

    async def _on_request_headers(
        self, msg: ext_proc_pb2.HttpHeaders
    ) -> tuple[ext_proc_pb2.ProcessingResponse, bool]:
        inbound = _peek_header(msg, "authorization")
        # Pass-through path: the caller is using a credential we didn't
        # mint and shouldn't touch. claude-code's v2 remote-control bridge
        # uses worker_jwt (sk-ant-si-…) on /v1/code/sessions/{id}/worker*
        # endpoints, returned to it from the prior /bridge POST response.
        # If we overwrite that Authorization with our OAuth Bearer the
        # /worker endpoint 401s — Anthropic rejects the OAuth token there.
        if inbound != PLACEHOLDER_BEARER:
            record_ext_proc_request("passthrough")
            return (
                ext_proc_pb2.ProcessingResponse(
                    request_headers=ext_proc_pb2.HeadersResponse()
                ),
                False,
            )
        token = await self._get_access_token()
        if token == "missing":
            record_ext_proc_request("missing_token")
        else:
            record_ext_proc_request("injected")
        set_headers = [
            base_pb2.HeaderValueOption(
                header=base_pb2.HeaderValue(
                    key="authorization", raw_value=f"Bearer {token}".encode()
                ),
                append_action=base_pb2.HeaderValueOption.OVERWRITE_IF_EXISTS_OR_ADD,
            )
        ]
        if self._config.account_header and self._cached_account_id:
            set_headers.append(
                base_pb2.HeaderValueOption(
                    header=base_pb2.HeaderValue(
                        key=self._config.account_header,
                        raw_value=self._cached_account_id.encode(),
                    ),
                    append_action=base_pb2.HeaderValueOption.OVERWRITE_IF_EXISTS_OR_ADD,
                )
            )
        if self._config.fedramp_header and self._cached_fedramp:
            set_headers.append(
                base_pb2.HeaderValueOption(
                    header=base_pb2.HeaderValue(
                        key=self._config.fedramp_header,
                        raw_value=b"true",
                    ),
                    append_action=base_pb2.HeaderValueOption.OVERWRITE_IF_EXISTS_OR_ADD,
                )
            )
        headers_resp = ext_proc_pb2.HeadersResponse(
            response=ext_proc_pb2.CommonResponse(
                header_mutation=ext_proc_pb2.HeaderMutation(
                    set_headers=set_headers,
                    # Whatever the pod sent for x-api-key would conflict
                    # with our Bearer auth and make the provider 401. Strip.
                    remove_headers=["x-api-key"],
                ),
            ),
        )
        return ext_proc_pb2.ProcessingResponse(request_headers=headers_resp), True

    async def _on_response_headers(
        self, msg: ext_proc_pb2.HttpHeaders, was_injected: bool
    ) -> ext_proc_pb2.ProcessingResponse:
        # Only treat 401 as a refresh trigger for requests we actually
        # injected. A 401 on a request that came in with a worker_jwt
        # we passed through is about that JWT (expired/revoked/etc.),
        # not about our cached OAuth token; rotating in response would
        # spuriously churn tokens and could trigger the storm pattern
        # if /worker endpoints loop on 401.
        if was_injected:
            status = _peek_status(msg)
            record_upstream_status(status)
            if status == 401:
                self._access_invalidated = True
                # Single-flight: only schedule a refresh if one isn't
                # already running. Subsequent 401s in the same burst
                # piggy-back on the in-flight task via
                # _get_access_token.await(self._refresh_task).
                if self._refresh_task is None or self._refresh_task.done():
                    self._refresh_task = asyncio.create_task(self._refresh())
                # Also wake the long-lived keeper. The create_task above lives
                # in this request handler's lineage and can be cancelled when
                # agy's short stream closes before the refresh + KV write land
                # (the antigravity cold-start failure). The keeper re-runs the
                # refresh from a task that outlives the stream, so the rotation
                # and KV persist always complete.
                self._refresh_wakeup.set()
        return ext_proc_pb2.ProcessingResponse(response_headers=ext_proc_pb2.HeadersResponse())

    async def _get_access_token(self) -> str:
        if self._cached_access is not None and not self._access_invalidated:
            return self._cached_access
        # The cache is poisoned (or empty). If a refresh is already in
        # flight, wait for it; awaiting the task guarantees we see the
        # fresh _cached_access on return. The earlier "acquire-and-release
        # the lock" pattern raced: if this coroutine grabbed the lock
        # before the queued _refresh task did, it returned a stale token
        # and the upstream re-401'd, scheduling yet another refresh.
        task = self._refresh_task
        if task is not None and not task.done():
            record_single_flight_wait()
            try:
                await task
            except Exception:
                # _refresh logs and swallows; we just fall through and
                # serve whatever's in cache (worst case: placeholder, and
                # envoy's retry-on-401 will trigger another refresh round).
                pass
        async with self._lock:
            if self._cached_access is None:
                self._reload_from_file()
        # _reload_from_file may have failed to find a token; surface as a
        # placeholder — Envoy will get 401 from upstream and retry, and
        # the retry path will trigger a refresh.
        return self._cached_access or "missing"

    def _blob_freshness_ms(self, blob: dict[str, Any]) -> int | None:
        """Return the best available freshness marker for a credential blob.

        Claude stores expiresAt milliseconds. Codex stores last_refresh in
        newer auth.json files and the access token itself is a JWT with exp.
        We use the greatest comparable marker so a newly re-seeded file can
        replace memory, while an old ESO mount cannot clobber a just-rotated
        in-memory refresh chain.
        """
        if not isinstance(blob, dict):
            return None
        candidates: list[int] = []

        def walk(node: Any) -> None:
            if not isinstance(node, dict):
                return
            for k, v in node.items():
                if k in ("expiresAt", "expires_at") and isinstance(v, int):
                    candidates.append(v)
                elif k in ("last_refresh", "expiry") and isinstance(v, str):
                    # last_refresh: Codex auth.json. expiry: Google/antigravity
                    # RFC3339 access-token expiry.
                    parsed = _iso_ms(v)
                    if parsed is not None:
                        candidates.append(parsed)
                elif k in ("accessToken", "access_token") and isinstance(v, str):
                    parsed = _jwt_exp_ms(v)
                    if parsed is not None:
                        candidates.append(parsed)
                elif isinstance(v, dict):
                    walk(v)

        walk(blob)
        return max(candidates) if candidates else None

    def _blob_fedramp(self, blob: dict[str, Any]) -> bool:
        for k, v in blob.items():
            if k == "chatgpt_account_is_fedramp" and isinstance(v, bool):
                return v
            if isinstance(v, dict):
                if self._blob_fedramp(v):
                    return True
            elif k in ("idToken", "id_token") and isinstance(v, str):
                payload = _jwt_payload(v)
                auth = payload.get("https://api.openai.com/auth")
                if isinstance(auth, dict) and auth.get("chatgpt_account_is_fedramp") is True:
                    return True
        return False

    def _blob_account_id(self, blob: dict[str, Any]) -> str | None:
        found = _walk_for(blob, ("account_id", "chatgpt_account_id"))
        if found:
            return found
        for k, v in blob.items():
            if isinstance(v, dict):
                found = self._blob_account_id(v)
                if found:
                    return found
            elif k in ("idToken", "id_token") and isinstance(v, str):
                payload = _jwt_payload(v)
                auth = payload.get("https://api.openai.com/auth")
                if isinstance(auth, dict):
                    account_id = auth.get("chatgpt_account_id")
                    if isinstance(account_id, str) and account_id:
                        return account_id
        return None

    def _cached_freshness_ms(self) -> int | None:
        return self._blob_freshness_ms(self._cached_blob) if self._cached_blob else None

    def _reload_from_file(self) -> None:
        """Pull the on-disk blob into the in-memory cache, but only if the
        file is strictly fresher than memory.

        Skipping a stale-file reload is the load-bearing invariant: if we
        just rotated in-process and KV+ESO haven't propagated back yet,
        the file holds pre-rotation tokens whose refresh has already been
        single-use-invalidated by Anthropic. Clobbering memory with that
        would make the next refresh 400 invalid_grant.
        """
        try:
            with open(self._config.credentials_file, "r", encoding="utf-8") as f:
                blob = json.load(f)
        except FileNotFoundError:
            log.error("credentials file %s not found; serving placeholder", self._config.credentials_file)
            return
        except Exception:
            log.exception("could not read credentials file %s", self._config.credentials_file)
            return
        file_access = _walk_for(blob, ("accessToken", "access_token"))
        file_refresh = _walk_for(blob, ("refreshToken", "refresh_token"))
        file_account_id = self._blob_account_id(blob)
        file_freshness = self._blob_freshness_ms(blob)
        cached_freshness = self._cached_freshness_ms()
        if (
            cached_freshness is not None
            and file_freshness is not None
            and file_freshness <= cached_freshness
        ):
            return  # memory is at least as fresh
        if self._cached_access is not None and file_access == self._cached_access:
            return  # tokens match; nothing to do
        if self._cached_access is not None and file_freshness is None:
            log.warning(
                "skipping %s credential reload with no freshness marker; keeping memory",
                self._config.provider,
            )
            return
        self._cached_blob = blob
        self._cached_access = file_access
        self._cached_refresh = file_refresh
        self._cached_account_id = file_account_id
        self._cached_fedramp = self._blob_fedramp(blob)
        log.info(
            "loaded %s credentials from file (access prefix=%s, account=%s)",
            self._config.provider,
            (file_access or "")[:12],
            file_account_id or "none",
        )

    def _should_refresh(self) -> bool:
        """Whether the keeper should rotate now.

        True when the cached token is invalidated by an upstream 401, missing,
        or within REFRESH_SKEW_MS of expiry. False when there is no refresh
        token (an unconfigured provider — don't spin) or the freshness marker
        is unknown (don't churn blindly; the reactive 401 path still covers
        genuine invalidation).
        """
        if self._cached_refresh is None:
            return False
        if self._access_invalidated or self._cached_access is None:
            return True
        fresh = self._cached_freshness_ms()
        if fresh is None:
            return False
        return fresh - int(time.time() * 1000) < REFRESH_SKEW_MS

    async def run_refresh_keeper(self) -> None:
        """Long-lived proactive refresh loop (started in serve()).

        Warms the token on boot and keeps it warm before expiry, and — because
        it lives for the whole process, not a single request stream — it is the
        path that guarantees the refresh *and* the KV write-back run to
        completion. A reactive 401-triggered refresh created inside a request
        handler can be cancelled when agy's short stream closes (the antigravity
        cold-start failure: turn 1 stranded the rotation, turn 2 stranded the KV
        persist); the keeper re-runs it here where nothing cancels it.
        """
        while not self._stopping:
            try:
                if self._cached_access is None and self._cached_refresh is None:
                    async with self._lock:
                        self._reload_from_file()
                if self._should_refresh():
                    await self._refresh()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("refresh keeper iteration failed")
            try:
                await asyncio.wait_for(
                    self._refresh_wakeup.wait(), PROACTIVE_REFRESH_POLL_SECONDS
                )
            except asyncio.TimeoutError:
                pass
            self._refresh_wakeup.clear()

    async def _refresh(self) -> None:
        async with self._lock:
            # Re-read the file under the lock: ESO may have mirrored a
            # newer KV value (e.g. someone re-seeded via "+ config sub")
            # and we should prefer that over rotating against the provider.
            self._reload_from_file()
            # The keeper and the reactive 401 path can both reach here. If the
            # token is already fresh and not invalidated (another refresh just
            # landed), skip the redundant provider round trip rather than
            # burning the refresh token a second time.
            if not self._access_invalidated:
                fresh = self._cached_freshness_ms()
                if fresh is not None and fresh - int(time.time() * 1000) > REFRESH_SKEW_MS:
                    return
            if self._cached_refresh is None:
                log.error("no refresh token available; cannot rotate")
                record_refresh("no_refresh_token")
                self._record_health_result("no_refresh_token", "no_refresh_token", "No refresh token available; the OAuth blob is missing or unreadable.")
                return
            log.info("calling %s to rotate %s token", self._config.token_url, self._config.provider)
            refresh_start = time.monotonic()
            self._health_last_attempted_at = time.time()
            self._health_attempt_id += 1
            try:
                async with httpx.AsyncClient(timeout=30.0) as http:
                    payload = {
                        "grant_type": "refresh_token",
                        "refresh_token": self._cached_refresh,
                        "client_id": self._config.client_id,
                    }
                    if self._config.client_secret is not None:
                        payload["client_secret"] = self._config.client_secret
                    if self._config.token_request_form:
                        # Google's OAuth2 /token endpoint requires
                        # application/x-www-form-urlencoded; httpx sets the
                        # content-type from data=.
                        resp = await http.post(self._config.token_url, data=payload)
                    else:
                        resp = await http.post(
                            self._config.token_url,
                            json=payload,
                            headers={"Content-Type": "application/json"},
                        )
            except Exception:
                log.exception("refresh request crashed; keeping existing tokens")
                record_refresh("request_failed", time.monotonic() - refresh_start)
                self._record_health_result("request_failed", "request_failed", "Upstream OAuth token endpoint unreachable.")
                return
            if resp.status_code != 200:
                log.error("refresh failed: status=%s body=%s", resp.status_code, resp.text[:500])
                record_refresh("http_error", time.monotonic() - refresh_start)
                reason, text = _classify_refresh_failure(resp)
                self._record_health_result("http_error", reason, text)
                return
            data = resp.json()
            new_access = data["access_token"]
            new_refresh = data.get("refresh_token") or self._cached_refresh
            new_id = data.get("id_token")
            expires_in = int(data.get("expires_in", 3600))
            # Update in-memory state FIRST so concurrent waiters see the
            # fresh access token without depending on KV+ESO+kubelet.
            self._cached_access = new_access
            self._cached_refresh = new_refresh
            if self._cached_blob is not None:
                self._cached_blob = _patch_blob(
                    self._cached_blob,
                    new_access,
                    new_refresh,
                    expires_in,
                    new_id=new_id,
                    patch_last_refresh=self._config.patch_last_refresh,
                )
                self._cached_account_id = self._blob_account_id(self._cached_blob)
                self._cached_fedramp = self._blob_fedramp(self._cached_blob)
            self._access_invalidated = False
            log.info(
                "rotated %s successfully (access prefix=%s, expires in %ds)",
                self._config.provider,
                new_access[:12],
                expires_in,
            )
            record_refresh("success", time.monotonic() - refresh_start)
            self._health_last_succeeded_at = time.time()
            self._record_health_result("success", "", "")
            await self._persist_to_kv(expires_in)

    def _record_health_result(self, result: str, reason: str, text: str) -> None:
        """Record the outcome of a refresh attempt for the /health endpoint.

        result is the high-level outcome ("success" / "http_error" /
        "request_failed" / "no_refresh_token"); reason is the
        fine-grained label (e.g. "refresh_token_reused"); text is the
        user-facing string the orchestrator copies into a
        session.status:failed banner.
        """
        self._health_last_result = result
        self._health_last_reason = reason
        self._health_last_text = text

    def health_snapshot(self) -> dict[str, Any]:
        """Return the current refresh-health snapshot for the /health
        endpoint. The orchestrator's poller reads this every 30s,
        debounces sustained failures, and writes Layer 1 rows. Times
        are unix-seconds floats (or None when no attempt yet).
        """
        return {
            "provider": self._config.provider,
            "result": self._health_last_result,
            "reason": self._health_last_reason,
            "text": self._health_last_text,
            "last_attempted_at": self._health_last_attempted_at,
            "last_succeeded_at": self._health_last_succeeded_at,
            "attempt_id": self._health_attempt_id,
        }

    async def usage_snapshot(self) -> dict[str, Any]:
        """Fetch provider-side usage/quota metadata with the managed OAuth token.

        This is the same trust boundary as ext_proc injection: the proxy owns
        the live access token and account headers, while tank-operator owns the
        UI normalization. A 401 invalidates and refreshes once before returning
        an error snapshot.
        """
        urls = self._usage_urls()
        if not urls:
            return {
                "provider": self._config.provider,
                "status": "unsupported",
                "error": "usage endpoint is not configured for provider",
            }
        token = await self._get_access_token()
        if token in ("", "missing"):
            return {
                "provider": self._config.provider,
                "status": "unavailable",
                "error": "managed OAuth token is unavailable",
            }

        headers = self._usage_headers(token)
        last_error = ""
        last_status = 0
        for attempt in range(2):
            for url in urls:
                try:
                    async with httpx.AsyncClient(timeout=15.0) as http:
                        resp = await http.get(url, headers=headers)
                except Exception as err:
                    log.warning("%s usage request failed: %s", self._config.provider, err)
                    last_error = f"request_failed: {err}"
                    continue
                last_status = resp.status_code
                if resp.status_code == 401 and attempt == 0:
                    self._access_invalidated = True
                    await self._refresh()
                    token = await self._get_access_token()
                    headers = self._usage_headers(token)
                    break
                if resp.status_code == 404 and len(urls) > 1:
                    last_error = "usage endpoint not found"
                    continue
                if resp.status_code < 200 or resp.status_code >= 300:
                    last_error = resp.text[:500]
                    continue
                try:
                    body = resp.json()
                except Exception as err:
                    return {
                        "provider": self._config.provider,
                        "status": "error",
                        "status_code": resp.status_code,
                        "error": f"usage response was not JSON: {err}",
                    }
                return {
                    "provider": self._config.provider,
                    "status": "ok",
                    "status_code": resp.status_code,
                    "observed_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                    "usage": body,
                }
        return {
            "provider": self._config.provider,
            "status": "error",
            "status_code": last_status or None,
            "error": last_error or "usage request failed",
        }

    def _usage_urls(self) -> list[str]:
        if self._config.provider == "claude":
            return [
                os.environ.get(
                    "CLAUDE_USAGE_URL",
                    "https://api.anthropic.com/api/oauth/usage",
                )
            ]
        if self._config.provider == "codex":
            primary = os.environ.get(
                "CODEX_USAGE_URL",
                "https://chatgpt.com/backend-api/codex/usage",
            )
            fallback = os.environ.get(
                "CODEX_USAGE_FALLBACK_URL",
                "https://chatgpt.com/api/codex/usage",
            )
            return [primary, fallback] if primary != fallback else [primary]
        return []

    def _usage_headers(self, token: str) -> dict[str, str]:
        headers = {
            "Authorization": f"Bearer {token}",
            "Accept": "application/json",
        }
        if self._config.provider == "claude":
            headers["anthropic-beta"] = "oauth-2025-04-20"
            headers["Content-Type"] = "application/json"
        if self._config.provider == "codex":
            headers["User-Agent"] = os.environ.get("CODEX_USAGE_USER_AGENT", "Codex CLI/0.131.0")
            if self._cached_account_id:
                headers["ChatGPT-Account-ID"] = self._cached_account_id
            if self._cached_fedramp:
                headers["X-OpenAI-Fedramp"] = "true"
        return headers

    async def _persist_to_kv(self, expires_in: int) -> None:
        """Best-effort write of the rotated blob back to KV.

        Failure mode (KV write errors after a successful provider refresh)
        used to be a chain-killer in the cron design — the provider had
        already invalidated the old refresh token, but KV still held it.
        Here it's tolerable: in-memory state already serves the fresh
        access token to ongoing requests, and a future restart (rare,
        and not concurrent with a refresh storm) re-reads from the
        slightly-stale Secret without losing service. ESO will eventually
        re-mirror after the next successful rotation. No alert needed —
        just log and move on.
        """
        if not self._kv_url or self._cached_blob is None:
            record_kv_persist("skipped")
            return
        try:
            cred = DefaultAzureCredential()
            try:
                async with SecretClient(vault_url=self._kv_url, credential=cred) as kv:
                    await kv.set_secret(self._config.kv_secret_name, json.dumps(self._cached_blob))
                log.info(
                    "wrote rotated blob to %s/%s (expires in %ds)",
                    self._kv_url,
                    self._config.kv_secret_name,
                    expires_in,
                )
                record_kv_persist("success")
            finally:
                await cred.close()
        except Exception:
            log.exception("KV write failed; tokens stay in memory only")
            record_kv_persist("failure")


def _peek_header(msg: ext_proc_pb2.HttpHeaders, name: str) -> str | None:
    name_lower = name.lower()
    for h in msg.headers.headers:
        if h.key.lower() == name_lower:
            value = h.raw_value.decode() if h.raw_value else h.value
            return value
    return None


def _peek_status(msg: ext_proc_pb2.HttpHeaders) -> int | None:
    for h in msg.headers.headers:
        if h.key == ":status":
            value = h.raw_value.decode() if h.raw_value else h.value
            try:
                return int(value)
            except ValueError:
                return None
    return None


async def serve(port: int) -> tuple[grpc.aio.Server, AuthInjector]:
    """Boot the ext_proc grpc server and return both the server and the
    AuthInjector instance. The injector is returned so __main__ can wire
    its health_snapshot() into the metrics-server's /health endpoint —
    the orchestrator's poller reads that snapshot to drive the
    transcript-surfaced provider-credential banner.
    """
    config = _config_from_env()
    server = grpc.aio.server()
    injector = AuthInjector(config)
    ext_proc_grpc.add_ExternalProcessorServicer_to_server(injector, server)
    server.add_insecure_port(f"0.0.0.0:{port}")
    await server.start()
    log.info("%s ext_proc listening on 0.0.0.0:%d", config.provider, port)
    # Start the proactive refresh keeper as a long-lived task so it outlives any
    # single request stream (see run_refresh_keeper / the antigravity cold-start
    # incident). Stored on the injector so __main__ can cancel it on shutdown.
    injector._keeper_task = asyncio.create_task(injector.run_refresh_keeper())
    return server, injector


# Suppress unused-import warning: the http_status import is kept so that
# downstream protobuf descriptor resolution doesn't require eager loading
# from grpc internals if the module is dlopen'd before the deps register.
_ = http_status_pb2  # noqa: F401
