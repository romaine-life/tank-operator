"""Auth flow mirroring the platform pattern (kill-me/microsoft-routes.js).

1. Frontend uses MSAL.js to obtain an Entra ID token.
2. Frontend POSTs it to /api/auth/microsoft/login.
3. Backend validates the ID token via JWKS using the SPA client-id as audience.
4. Backend mints its own short-lived JWT (signed with JWT_SECRET) and returns
   it in the response body and as an httpOnly cookie. The cookie covers the
   WebSocket exec endpoint where Authorization headers can't be set.
5. Subsequent calls go through `current_user` which verifies the self-signed
   JWT off the Authorization header (REST) or `auth_token` cookie (WebSocket).

Allowed-email gating is done once at /api/auth/microsoft/login — only emails
in the configured ALLOWED_EMAILS allowlist get a session token; everyone else
gets 403.
"""
from __future__ import annotations

import asyncio
import hashlib
import os
import re
import time
from dataclasses import dataclass
from typing import Any

import jwt
from fastapi import Cookie, HTTPException, Header, WebSocket
from jwt import PyJWKClient

ENTRA_CLIENT_ID = os.environ.get("ENTRA_CLIENT_ID", "")
JWT_SECRET = os.environ.get("JWT_SECRET", "")
ALLOWED_EMAILS = frozenset(
    e.strip().lower() for e in os.environ.get("ALLOWED_EMAILS", "").split(",") if e.strip()
)

# Cluster-native auth path. Maps a kubernetes ServiceAccount subject
# (system:serviceaccount:<ns>:<sa>) to an email that the orchestrator
# treats as the session identity. Format: comma-separated
# "subject=email" pairs. Used by /api/internal/auth/k8s to mint a real
# session JWT for in-cluster automation (smoke tests, playwright
# probes) without needing the MSAL OAuth roundtrip.
K8S_AUTH_SUBJECT_TO_EMAIL: dict[str, str] = {}
for _entry in os.environ.get("K8S_AUTH_ALLOWED_SUBJECTS", "").split(","):
    if "=" not in _entry:
        continue
    _sa, _email = _entry.split("=", 1)
    _sa, _email = _sa.strip(), _email.strip().lower()
    if _sa and _email:
        K8S_AUTH_SUBJECT_TO_EMAIL[_sa] = _email

# Match kill-me's posture: `common` JWKS endpoint, regex issuer match. Lets
# any Microsoft user attempt to sign in; the email allowlist is the gate.
_JWKS_URL = "https://login.microsoftonline.com/common/discovery/v2.0/keys"
_ENTRA_ISSUER_PATTERN = re.compile(r"^https://login\.microsoftonline\.com/.+/v2\.0$")
_jwks_client = PyJWKClient(_JWKS_URL, cache_keys=True, lifespan=3600)

SESSION_TTL_SECONDS = 7 * 24 * 60 * 60  # 7 days, matches kill-me
COOKIE_NAME = "auth_token"
ALGORITHM_HS = "HS256"
ALGORITHM_RS = "RS256"

# Short-lived JWT used as the OAuth-style `state` parameter on GitHub App
# install redirects (#57 stage 2). Bound to the caller's email and a custom
# audience so it can't be cross-used as a session token. 10-minute window
# is enough for "click install → grant on GitHub → redirect back" without
# leaving a stale token usable on a future install attempt.
INSTALL_STATE_TTL_SECONDS = 10 * 60
INSTALL_STATE_AUDIENCE = "tank-operator/github-install"


def mint_install_state(email: str) -> str:
    """Sign a short-lived state token binding the install flow to `email`.

    Verified in /api/github/install/callback to reject installs that didn't
    originate from a session we just minted state for.
    """
    if not JWT_SECRET:
        raise HTTPException(status_code=500, detail="JWT_SECRET not configured")
    now = int(time.time())
    return jwt.encode(
        {
            "email": email.lower(),
            "aud": INSTALL_STATE_AUDIENCE,
            "iat": now,
            "exp": now + INSTALL_STATE_TTL_SECONDS,
        },
        JWT_SECRET,
        algorithm=ALGORITHM_HS,
    )


def verify_install_state(state: str) -> str:
    """Verify an install-state token and return the email it was minted for."""
    if not JWT_SECRET:
        raise HTTPException(status_code=500, detail="JWT_SECRET not configured")
    try:
        payload = jwt.decode(
            state,
            JWT_SECRET,
            algorithms=[ALGORITHM_HS],
            audience=INSTALL_STATE_AUDIENCE,
        )
    except jwt.PyJWTError as e:
        raise HTTPException(status_code=400, detail=f"invalid install state: {e}") from e
    email = str(payload.get("email", "")).lower()
    if not email:
        raise HTTPException(status_code=400, detail="install state has no email")
    return email


@dataclass
class User:
    sub: str
    email: str
    name: str


def gravatar_url(email: str, size: int = 64) -> str:
    normalized = email.strip().lower().encode("utf-8")
    digest = hashlib.md5(normalized, usedforsecurity=False).hexdigest()
    return f"https://www.gravatar.com/avatar/{digest}?s={size}&d=mp"


def _verify_entra_id_token(id_token: str) -> dict[str, Any]:
    """Validate an Entra ID token signature, audience, and issuer."""
    if not ENTRA_CLIENT_ID:
        raise HTTPException(status_code=500, detail="ENTRA_CLIENT_ID not configured")

    signing_key = _jwks_client.get_signing_key_from_jwt(id_token)
    try:
        payload = jwt.decode(
            id_token,
            signing_key.key,
            algorithms=[ALGORITHM_RS],
            audience=ENTRA_CLIENT_ID,
            options={"verify_iss": False},
        )
    except jwt.PyJWTError as e:
        raise HTTPException(status_code=401, detail=f"invalid Entra token: {e}") from e

    iss = payload.get("iss", "")
    if not _ENTRA_ISSUER_PATTERN.match(iss):
        raise HTTPException(status_code=401, detail=f"unexpected token issuer: {iss}")
    return payload


def mint_session_token_for_email(email: str, *, sub: str | None = None) -> str:
    """Mint a session JWT bound to an existing allow-listed email.

    Used to inject a per-pod token at session creation so the agent inside
    the pod can call orchestrator endpoints back as the owning user (e.g.
    via the mcp-tank stdio server). The token validates against the same
    `current_user` path as a browser session — so any future MCP call that
    has to know "which user is this on behalf of" reads the JWT, not the
    pod's shared SA token.
    """
    if not JWT_SECRET:
        raise HTTPException(status_code=500, detail="JWT_SECRET not configured")
    normalized = email.strip().lower()
    if normalized not in ALLOWED_EMAILS:
        raise HTTPException(status_code=403, detail="email not allowed")
    now = int(time.time())
    return jwt.encode(
        {
            "sub": sub or f"pod:{normalized}",
            "email": normalized,
            "name": normalized,
            "iat": now,
            "exp": now + SESSION_TTL_SECONDS,
        },
        JWT_SECRET,
        algorithm=ALGORITHM_HS,
    )


async def exchange_microsoft_token(id_token: str) -> tuple[str, User]:
    """Verify an Entra ID token and mint a backend session JWT for the allowed user.

    Returns (session_token, user). Raises 401/403 on rejection.
    """
    if not JWT_SECRET:
        raise HTTPException(status_code=500, detail="JWT_SECRET not configured")
    if not ALLOWED_EMAILS:
        raise HTTPException(status_code=500, detail="ALLOWED_EMAILS not configured")

    # Offload the (sync, network-bound) JWKS fetch + verify so we don't block the loop.
    payload = await asyncio.to_thread(_verify_entra_id_token, id_token)

    email = (payload.get("email") or payload.get("preferred_username") or "").lower()
    if not email:
        raise HTTPException(status_code=401, detail="token has no email or preferred_username claim")
    if email not in ALLOWED_EMAILS:
        raise HTTPException(status_code=403, detail="email not allowed")

    user = User(
        sub=str(payload.get("sub", "")),
        email=email,
        name=str(payload.get("name", "")),
    )
    now = int(time.time())
    session_token = jwt.encode(
        {
            "sub": user.sub,
            "email": user.email,
            "name": user.name,
            "iat": now,
            "exp": now + SESSION_TTL_SECONDS,
        },
        JWT_SECRET,
        algorithm=ALGORITHM_HS,
    )
    return session_token, user


def _decode_session_token(token: str) -> User:
    if not JWT_SECRET:
        raise HTTPException(status_code=500, detail="JWT_SECRET not configured")
    try:
        payload = jwt.decode(token, JWT_SECRET, algorithms=[ALGORITHM_HS])
    except jwt.PyJWTError as e:
        raise HTTPException(status_code=401, detail=f"invalid session token: {e}") from e
    email = str(payload.get("email", "")).lower()
    if email not in ALLOWED_EMAILS:
        # Allowlist may have changed since the token was issued; reject stale sessions.
        raise HTTPException(status_code=403, detail="email no longer allowed")
    return User(sub=str(payload["sub"]), email=email, name=str(payload.get("name", "")))


def _token_from_request(authorization: str | None, auth_cookie: str | None) -> str:
    if authorization and authorization.lower().startswith("bearer "):
        return authorization[7:]
    if auth_cookie:
        return auth_cookie
    raise HTTPException(status_code=401, detail="missing authentication")


def current_user(
    authorization: str | None = Header(default=None),
    auth_token: str | None = Cookie(default=None),
) -> User:
    """FastAPI dependency for REST endpoints."""
    return _decode_session_token(_token_from_request(authorization, auth_token))


def current_user_ws(ws: WebSocket) -> User:
    """Same check as current_user, but pulls headers/cookies off a raw WebSocket."""
    authorization = ws.headers.get("authorization")
    auth_cookie = ws.cookies.get(COOKIE_NAME)
    return _decode_session_token(_token_from_request(authorization, auth_cookie))


async def mint_session_token_for_k8s_subject(sa_token: str) -> tuple[str, User]:
    """Validate a k8s ServiceAccount projected token and mint a session JWT
    bound to the email mapped from that SA subject.

    Looked up via TokenReview against the cluster's auth API — the
    orchestrator's ServiceAccount needs `create tokenreviews` (the
    standard ClusterRole `system:auth-delegator` covers it). Any SA
    token the cluster issues can be presented; only subjects listed in
    the K8S_AUTH_ALLOWED_SUBJECTS env are accepted, and the mapped
    email must also be in ALLOWED_EMAILS.

    Designed for in-cluster automation (smoke tests, headless-browser
    probes) that needs to act as an authenticated user without going
    through MSAL.

    NOT for session pods acting on behalf of their owner — those carry a
    per-pod JWT minted via mint_session_token_for_email at create time
    (TANK_API_TOKEN env var). The shared `claude-session` SA can only map
    to one email here, so using this path from session pods would conflate
    every user's traffic under a single principal.
    """
    if not JWT_SECRET:
        raise HTTPException(status_code=500, detail="JWT_SECRET not configured")
    if not K8S_AUTH_SUBJECT_TO_EMAIL:
        raise HTTPException(
            status_code=503, detail="k8s auth not configured (K8S_AUTH_ALLOWED_SUBJECTS empty)"
        )
    # Imported here so module load doesn't depend on a kube config —
    # this path is only ever called inside the orchestrator pod.
    from kubernetes_asyncio import client as _k8s_client

    api = _k8s_client.AuthenticationV1Api()
    # Audience-scoped TokenReview. The smoke caller mints a projected
    # token with `audience: tank-operator` (instead of the default
    # cluster audience) so the same SA's other tokens — used for
    # different services — can't be replayed against tank-operator.
    body = _k8s_client.V1TokenReview(
        spec=_k8s_client.V1TokenReviewSpec(
            token=sa_token,
            audiences=["tank-operator"],
        )
    )
    try:
        result = await api.create_token_review(body=body)
    except _k8s_client.ApiException as exc:
        raise HTTPException(status_code=500, detail=f"TokenReview failed: {exc.status}") from exc
    if not result.status or not result.status.authenticated:
        raise HTTPException(status_code=401, detail="invalid k8s SA token")
    subject = result.status.user.username if result.status.user else None
    if not subject:
        raise HTTPException(status_code=401, detail="TokenReview returned no subject")
    email = K8S_AUTH_SUBJECT_TO_EMAIL.get(subject)
    if not email:
        raise HTTPException(
            status_code=403, detail=f"subject not allowed: {subject}"
        )
    if email not in ALLOWED_EMAILS:
        raise HTTPException(
            status_code=403, detail=f"mapped email not in ALLOWED_EMAILS: {email}"
        )
    user = User(sub=f"k8s:{subject}", email=email, name=email)
    now = int(time.time())
    token = jwt.encode(
        {
            "sub": user.sub,
            "email": user.email,
            "name": user.name,
            "iat": now,
            "exp": now + SESSION_TTL_SECONDS,
        },
        JWT_SECRET,
        algorithm=ALGORITHM_HS,
    )
    return token, user
