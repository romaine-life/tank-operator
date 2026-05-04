import asyncio
import contextlib
import hashlib
import json
import logging
import os
import socket
import time
import uuid
from dataclasses import asdict, dataclass
from typing import Any, AsyncIterator

from kubernetes_asyncio import client, config

log = logging.getLogger(__name__)

SESSIONS_NAMESPACE = os.environ.get("SESSIONS_NAMESPACE", "tank-operator-sessions")
SESSION_IMAGE = os.environ.get("SESSION_IMAGE", "romainecr.azurecr.io/claude-container:latest")
CODEX_SESSION_IMAGE = os.environ.get(
    "CODEX_SESSION_IMAGE", "romainecr.azurecr.io/codex-container:latest"
)
PI_SESSION_IMAGE = os.environ.get(
    "PI_SESSION_IMAGE", "romainecr.azurecr.io/pi-container:latest"
)
SESSION_SERVICE_ACCOUNT = os.environ.get("SESSION_SERVICE_ACCOUNT", "claude-session")
SESSION_CONFIGMAP = os.environ.get("SESSION_CONFIGMAP", "tank-session-config")
GITHUB_APP_SECRET = os.environ.get("GITHUB_APP_SECRET", "github-app-creds")
# OAuth gateway: in-cluster service that impersonates platform.claude.com.
# Session pods reach it via a hostAlias mapping platform.claude.com to this
# Service's ClusterIP — hostAliases requires an IP, not a DNS name, so we
# resolve once at startup and stamp the IP onto every Deployment manifest.
OAUTH_GATEWAY_HOST = os.environ.get(
    "CLAUDE_OAUTH_GATEWAY_HOST",
    "claude-oauth-gateway.tank-operator.svc.cluster.local",
)
OAUTH_GATEWAY_CA_CONFIGMAP = os.environ.get("CLAUDE_OAUTH_GATEWAY_CA_CONFIGMAP", "claude-oauth-ca")
# In-cluster proxy that fronts api.anthropic.com. Same hostAlias trick as
# the OAuth gateway (DNS resolution at orchestrator startup, IP literal
# stamped onto each Deployment manifest). Pods send their requests to
# api.anthropic.com normally; the proxy strips their placeholder
# Authorization header, injects the current real OAuth Bearer, and
# refreshes against platform.claude.com on upstream 401.
API_PROXY_HOST = os.environ.get(
    "CLAUDE_API_PROXY_HOST",
    "claude-api-proxy.tank-operator.svc.cluster.local",
)
# Stamping these on each session Deployment makes ArgoCD claim it into the
# tank-operator-sessions Application's resource tree (visible alongside the
# orchestrator's chart-managed resources). That app has no auto-sync, so
# Argo never tries to reconcile / prune the dynamic deployments — pure
# visualization.
ARGOCD_TRACKING_APP = os.environ.get("ARGOCD_TRACKING_APP", "tank-operator-sessions")
# Reaper config: a session with no open WS for IDLE_TIMEOUT_SECONDS gets
# deleted by the periodic sweep. The 5-min default gives a comfortable
# window for tab reloads / brief network blips while still honoring the
# README's "killed when the tab closes" promise.
IDLE_TIMEOUT_SECONDS = int(os.environ.get("IDLE_TIMEOUT_SECONDS", "300"))
REAPER_INTERVAL_SECONDS = int(os.environ.get("REAPER_INTERVAL_SECONDS", "60"))


class SessionNotFound(Exception):
    pass


class SessionNotOwned(Exception):
    pass


class PodNotReady(Exception):
    pass


SESSION_MODES = (
    "api_key",
    "subscription",
    "config",
    "codex_config",
    "codex_subscription",
    "pi_config",
    "pi_subscription",
)
DEFAULT_SESSION_MODE = "subscription"
# Config mode: a one-shot pod the user logs into via `claude /login` to seed
# the OAuth credentials in KV. Differs from regular sessions in three ways:
# (1) no credentials are pre-seeded into the pod (we're harvesting, not
# consuming); (2) no platform.claude.com hostAlias override (claude needs to
# reach the real Anthropic for OAuth); (3) no bypassPermissions (the user
# is doing one interactive thing, not running an agent). After the user
# completes /login, the orchestrator's POST /api/sessions/{id}/save-credentials
# reads ~/.claude/.credentials.json out of the pod via exec and writes it to
# Key Vault.
CONFIG_MODE = "config"
# Codex-config: same role as `config` but for the OpenAI codex CLI. User
# runs `codex login --device-auth` interactively (terminal-friendly OAuth:
# prints a URL + one-time code instead of opening localhost:1455 — see
# https://developers.openai.com/codex/auth). After login, the
# save-credentials button harvests ~/.codex/auth.json and writes it to KV
# under `codex-credentials`. ESO mirrors KV → a Secret in the sessions
# namespace that codex_subscription pods mount.
CODEX_CONFIG_MODE = "codex_config"
# Codex-subscription: consume mode for codex. Mounts the ESO-mirrored
# `codex-credentials` Secret as a file volume; the bootstrap copies it to
# ~/.codex/auth.json (so codex's in-place refresh has somewhere writable to
# rewrite — Secret volumes are read-only) and launches `codex` via tmux.
# No proxy hijack — codex talks to api.openai.com directly and rotates the
# token bundle in-pod (per OpenAI's CI/CD auth doc: refresh-on-401 plus
# the ~8-day last_refresh window, written back to auth.json).
#
# KNOWN GAP — multi-pod refresh: in-pod rotation mutates auth.json but does
# not propagate back to KV. With two concurrent codex_subscription pods,
# both inherit the same auth.json from KV, both eventually trigger a
# refresh, and (if OpenAI rotates refresh_tokens on use, which is the
# default for modern OAuth) the loser's refresh_token is invalidated. This
# is the multi-pod sharing case — exactly what we paid the KV indirection
# to enable, so fixing it isn't optional. Phase 2: either a write-back
# sidecar (sufficient if OpenAI doesn't rotate refresh_tokens) or a
# centralized codex-api-proxy that single-flights refresh (mandatory if it
# does). Determined by observing rotation behavior in a codex_config pod.
CODEX_SUBSCRIPTION_MODE = "codex_subscription"
CODEX_CREDS_SECRET = os.environ.get("CODEX_CREDS_SECRET", "codex-credentials")
# Pi-config is a disposable Pi login sandbox. Pi-subscription curates
# Tank-backed Claude/Codex subscriptions into Pi's auth.json at pod startup so
# the launcher only needs one Pi option while Pi still sees multiple providers.
#
# Product note: keeping this code alive is intentional, but we have not found a
# strong day-to-day use for Pi when the available hosted providers are only
# Codex and Claude. Codex's own TUI is the better first-party OpenAI surface,
# and Anthropic subscription auth inside Pi is third-party harness usage billed
# through extra usage, not Claude plan limits. Pi's likely value is future
# multi-provider work: Qwen/Kimi/DeepSeek, OpenRouter, Bedrock/Vertex/Azure
# gateways, or self-hosted OpenAI-compatible endpoints.
PI_CONFIG_MODE = "pi_config"
PI_SUBSCRIPTION_MODE = "pi_subscription"
# Modes that must reach the real internet directly (no platform.claude.com /
# api.anthropic.com hijack). Adding a Claude hostAlias to a config or codex
# pod would 404 the OAuth endpoints they're trying to reach. pi_subscription
# intentionally does use the api.anthropic.com proxy so Pi's Anthropic provider
# can reuse Tank's Claude subscription token injection.
NO_CLAUDE_HIJACK_MODES = frozenset(
    {
        CONFIG_MODE,
        CODEX_CONFIG_MODE,
        CODEX_SUBSCRIPTION_MODE,
        PI_CONFIG_MODE,
    }
)
CODEX_MODES = frozenset({CODEX_CONFIG_MODE, CODEX_SUBSCRIPTION_MODE})
PI_MODES = frozenset({PI_CONFIG_MODE, PI_SUBSCRIPTION_MODE})
# Remote-control: there used to be a dedicated `remote_control` session mode
# whose bootstrap auto-launched `claude '/remote-control'` to put the bridge
# URL in the TUI on session start. That cold-start raced claude's slash
# command registry init (the binary fetches GrowthBook flags / org info
# before the registry is fully populated) and printed "Unknown command"
# instead of running. The mode is gone. Subscription sessions get a
# "Remote control" button in the tab bar (frontend/src/App.tsx) that
# injects "/remote-control\r" into the live WS — the user clicks once
# claude is visibly ready, no race possible.


# Friendly-name annotation set by PATCH /api/sessions/{id}. Stored on the
# Deployment so it survives orchestrator restarts without a separate store.
# K8s allows up to ~256 KB of annotations per object — we cap inbound names
# well below that for UI sanity.
NAME_ANNOTATION = "tank-operator/display-name"
MAX_NAME_LENGTH = 80
GLIMMUNG_CONTEXT_ANNOTATION = "tank-operator/glimmung-context"


SESSION_CONFIG_MOUNTS = (
    ("mcp.json", "/workspace/.mcp.json"),
    ("default-claude.md", "/workspace/CLAUDE.md"),
    ("default-claude.md", "/workspace/AGENTS.md"),
    ("tank-bootstrap.sh", "/opt/tank/bootstrap.sh"),
    ("skills.done.SKILL.md", "/home/node/.claude/skills/done/SKILL.md"),
    ("skills.rollout.SKILL.md", "/home/node/.claude/skills/rollout/SKILL.md"),
    (
        "skills.rollout.agents.openai.yaml",
        "/home/node/.claude/skills/rollout/agents/openai.yaml",
    ),
    ("skills.rollout.SKILL.md", "/home/node/.codex/skills/rollout/SKILL.md"),
    (
        "skills.rollout.agents.openai.yaml",
        "/home/node/.codex/skills/rollout/agents/openai.yaml",
    ),
)


@dataclass
class SessionInfo:
    id: str
    pod_name: str | None
    owner: str
    status: str
    mode: str
    # ISO timestamp from the Deployment's creation time. The frontend uses
    # this to show a small "running for" indicator on session rows.
    created_at: str | None = None
    # User-provided friendly name. None when unset; the frontend falls back
    # to the session id slug. The slug stays canonical in URLs and the
    # Deployment/pod name — this is purely a display label.
    name: str | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def _owner_label(email: str) -> str:
    # K8s label values must match [a-z0-9A-Z._-]{0,63}; email addresses contain `@`.
    digest = hashlib.sha256(email.encode()).hexdigest()[:16]
    return f"u-{digest}"


def _session_config_mounts() -> list[dict[str, Any]]:
    return [
        {
            "name": "session-config",
            "mountPath": mount_path,
            "subPath": key,
            "readOnly": True,
        }
        for key, mount_path in SESSION_CONFIG_MOUNTS
    ]


def _session_config_volume() -> dict[str, Any]:
    return {"name": "session-config", "configMap": {"name": SESSION_CONFIGMAP}}


class SessionManager:
    """Manages session lifecycle as one Deployment per session.

    Deployment was chosen over Job because the workload is long-running until
    explicitly killed (claude CLI inside `sleep infinity`); Jobs are batch
    primitives. Bonus: ArgoCD's resource tree renders Deployments richly
    (ReplicaSet → Pod with health rollup), Jobs do not — useful once we
    surface the sessions namespace as orphaned resources.
    """

    def __init__(self) -> None:
        self._api: client.ApiClient | None = None
        self._apps: client.AppsV1Api | None = None
        self._core: client.CoreV1Api | None = None
        # In-memory connection tracking for the idle reaper. Single replica
        # only (values.yaml pins replicas: 1) — stateful, restart-tolerant
        # via the "adopt with now" branch in _reap_idle.
        self._ws_count: dict[str, int] = {}
        self._activity: dict[str, float] = {}
        self._reaper_task: asyncio.Task[None] | None = None
        # ClusterIP of the OAuth gateway Service — resolved once at startup
        # and stamped onto each Deployment as a hostAlias, since K8s
        # hostAliases require an IP literal, not a DNS name.
        self._oauth_gateway_ip: str | None = None
        # Same idea for the api.anthropic.com proxy — see API_PROXY_HOST.
        self._api_proxy_ip: str | None = None

    async def startup(self) -> None:
        try:
            config.load_incluster_config()
        except config.ConfigException:
            await config.load_kube_config()
        self._api = client.ApiClient()
        self._apps = client.AppsV1Api(self._api)
        self._core = client.CoreV1Api(self._api)
        self._oauth_gateway_ip = await self._resolve_oauth_gateway_ip()
        self._api_proxy_ip = await self._resolve_service_ip(API_PROXY_HOST, "API proxy")
        self._reaper_task = asyncio.create_task(self._reaper_loop())

    async def _resolve_oauth_gateway_ip(self) -> str | None:
        return await self._resolve_service_ip(OAUTH_GATEWAY_HOST, "OAuth gateway")

    async def _resolve_service_ip(self, host: str, label: str) -> str | None:
        """Resolve an in-cluster Service's ClusterIP via DNS.

        Returns None if resolution fails — callers should treat this as
        "service not deployed yet" and skip stamping the hostAlias
        rather than failing session creation. (Useful for first-install
        or local dev where the chart isn't fully reconciled.)
        """
        try:
            loop = asyncio.get_event_loop()
            infos = await loop.getaddrinfo(host, None, type=socket.SOCK_STREAM)
            return infos[0][4][0]
        except Exception:
            log.warning("could not resolve %s %s; sessions will boot without it", label, host)
            return None

    async def shutdown(self) -> None:
        if self._reaper_task is not None:
            self._reaper_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await self._reaper_task
        if self._api is not None:
            await self._api.close()

    def _deployment_manifest(
        self,
        session_id: str,
        owner: str,
        mode: str,
        glimmung_context: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        owner_label = _owner_label(owner)
        selector_labels = {"tank-operator/session-id": session_id}
        deployment_name = f"session-{session_id}"
        argocd_tracking_id = (
            f"{ARGOCD_TRACKING_APP}:apps/Deployment:{SESSIONS_NAMESPACE}/{deployment_name}"
        )
        context_json = ""
        if glimmung_context is not None:
            context_json = json.dumps(glimmung_context, sort_keys=True, separators=(",", ":"))
        if mode in CODEX_MODES:
            session_image = CODEX_SESSION_IMAGE
        elif mode in PI_MODES:
            session_image = PI_SESSION_IMAGE
        else:
            session_image = SESSION_IMAGE
        pod_spec: dict[str, Any] = {
            "serviceAccountName": SESSION_SERVICE_ACCOUNT,
            # The image's USER is claude (uid 1000). Reasserting it here
            # forces the kubelet to reject the pod if the image ever ships
            # back to root, and claude's safety check requires non-root for
            # bypassPermissions mode to take effect.
            "securityContext": {
                "runAsNonRoot": True,
                "runAsUser": 1000,
                "runAsGroup": 1000,
                "fsGroup": 1000,
            },
            "containers": [
                # Sidecar: localhost reverse proxy that injects fresh SA-token
                # bearer auth into outbound HTTP MCP calls. Same image as the
                # main container, different command. Required because the
                # projected SA token rotates in-place on disk every ~50min,
                # but env vars set from it at pod start go stale. The session
                # ConfigMap mounts .mcp.json with localhost URLs, and this
                # proxy reads the current bearer file per request.
                {
                    "name": "mcp-auth-proxy",
                    "image": session_image,
                    "imagePullPolicy": "Always",
                    "command": ["mcp-auth-proxy"],
                    "volumeMounts": _session_config_mounts(),
                },
                {
                    "name": "claude",
                    "image": session_image,
                    "imagePullPolicy": "Always",
                    "command": ["sleep", "infinity"],
                    "env": [
                        # Read by exec_proxy's bootstrap to pick the
                        # auth path. Sourced at the env level (not
                        # via secret) because the value is per-pod,
                        # not a shared secret.
                        {"name": "TANK_SESSION_MODE", "value": mode},
                        {
                            "name": "TANK_GLIMMUNG_CONTEXT_JSON",
                            "value": context_json,
                        },
                        {
                            "name": "TANK_GLIMMUNG_RUN_ID",
                            "value": str((glimmung_context or {}).get("glimmung_run_id") or ""),
                        },
                        {
                            "name": "TANK_GLIMMUNG_ISSUE_ID",
                            "value": str((glimmung_context or {}).get("glimmung_issue_id") or ""),
                        },
                        {
                            "name": "TANK_GLIMMUNG_PR_ID",
                            "value": str((glimmung_context or {}).get("glimmung_pr_id") or ""),
                        },
                        {
                            "name": "TANK_GLIMMUNG_VALIDATION_URL",
                            "value": str((glimmung_context or {}).get("validation_url") or ""),
                        },
                        # Force claude (and anything else using the
                        # `supports-hyperlinks` npm lib) to emit OSC 8
                        # hyperlinks. The library's terminal-sniff list
                        # doesn't recognise xterm.js, so without this
                        # claude falls back to plain text URLs and we'd
                        # have to detect wrapped URLs heuristically in
                        # frontend/src/wrappedLinkProvider.ts. With OSC 8
                        # the terminal gets explicit "this byte range is
                        # one link" markers regardless of newlines or
                        # auto-wrap, and xterm.js's built-in OSC 8
                        # support renders them natively.
                        {"name": "FORCE_HYPERLINK", "value": "1"},
                        # Switch claude's TUI to the alternate-screen-buffer
                        # renderer (vim/htop-style) instead of the default
                        # in-place redraw. Fixes the documented Ink
                        # SIGWINCH redraw-leak (anthropics/claude-code#49086)
                        # and full-buffer redraw drift (#29937) — both of
                        # which manifest as ghost lines and post-resize text
                        # collisions in xterm.js, since xterm.js is the same
                        # rendering-throughput-bound consumer class as the
                        # VS Code integrated terminal that the docs call out.
                        {"name": "CLAUDE_CODE_NO_FLICKER", "value": "1"},
                    ],
                    "envFrom": [
                        {"secretRef": {"name": GITHUB_APP_SECRET}},
                    ],
                    "stdin": True,
                    "tty": True,
                    "volumeMounts": _session_config_mounts(),
                }
            ],
            "volumes": [_session_config_volume()],
        }
        # OAuth gateway plumbing: add a hostAlias so platform.claude.com
        # resolves to the in-cluster gateway Service, mount the gateway's
        # CA cert (NOT the private key — that stays in the orchestrator
        # namespace), and set NODE_EXTRA_CA_CERTS so claude's Node runtime
        # trusts it. If the gateway IP couldn't be resolved at startup we
        # skip this whole stanza; the pod will boot but won't be able to
        # refresh — surfaces as a 401 the user can recover from by
        # recreating the session once the gateway is healthy.
        #
        # Config and codex modes skip this entirely. config / codex_config
        # need to reach real upstream OAuth endpoints (claude.ai or
        # auth.openai.com) for first-time login; codex_subscription doesn't
        # use platform.claude.com or api.anthropic.com at all (it talks to
        # OpenAI). Pointing any of them at our in-cluster Claude gateway
        # would 404 the auth endpoints (or, in codex_subscription's case,
        # be just irrelevant overhead).
        if mode not in NO_CLAUDE_HIJACK_MODES and (self._oauth_gateway_ip or self._api_proxy_ip):
            host_aliases: list[dict[str, Any]] = []
            if mode != PI_SUBSCRIPTION_MODE and self._oauth_gateway_ip:
                host_aliases.append(
                    {"ip": self._oauth_gateway_ip, "hostnames": ["platform.claude.com"]}
                )
            # api.anthropic.com is hijacked to the in-cluster proxy. The
            # proxy's leaf cert is signed by the same `claude-oauth-ca` the
            # session pod already trusts via NODE_EXTRA_CA_CERTS, so no
            # extra trust-store wiring is needed.
            if self._api_proxy_ip:
                host_aliases.append(
                    {"ip": self._api_proxy_ip, "hostnames": ["api.anthropic.com"]}
                )
            pod_spec["hostAliases"] = host_aliases
            # Index by name, not position — the sidecar lives at [0] now,
            # but only the claude container needs the OAuth gateway CA.
            container = next(c for c in pod_spec["containers"] if c["name"] == "claude")
            container["env"].append(
                {"name": "NODE_EXTRA_CA_CERTS", "value": "/etc/oauth-gateway-ca/ca.crt"}
            )
            container.setdefault("volumeMounts", []).append(
                {
                    "name": "oauth-gateway-ca",
                    "mountPath": "/etc/oauth-gateway-ca",
                    "readOnly": True,
                }
            )
            pod_spec.setdefault("volumes", []).append(
                {
                    "name": "oauth-gateway-ca",
                    "configMap": {"name": OAUTH_GATEWAY_CA_CONFIGMAP},
                }
            )
        # codex_subscription pods mount the ESO-mirrored codex-credentials
        # Secret as a read-only file at /etc/codex-creds/auth.json. The
        # bootstrap copies it into ~/.codex/auth.json (writable, mode 600)
        # so codex's in-place token rotation has somewhere to rewrite —
        # Secret volumes are read-only by construction. `optional: true`
        # lets the pod boot before any codex_config harvest has run; the
        # bootstrap surfaces a clear error in that case instead of the
        # kubelet getting stuck on a missing-Secret mount.
        if mode == CODEX_SUBSCRIPTION_MODE:
            container = next(c for c in pod_spec["containers"] if c["name"] == "claude")
            container.setdefault("volumeMounts", []).append(
                {
                    "name": "codex-creds",
                    "mountPath": "/etc/codex-creds",
                    "readOnly": True,
                }
            )
            pod_spec.setdefault("volumes", []).append(
                {
                    "name": "codex-creds",
                    "secret": {
                        "secretName": CODEX_CREDS_SECRET,
                        "optional": True,
                    },
                }
            )
        # pi_subscription pods mount existing Codex credentials. Bootstrap
        # translates them into Pi auth.json and adds Tank's Claude proxy
        # placeholder auth.
        if mode == PI_SUBSCRIPTION_MODE:
            container = next(c for c in pod_spec["containers"] if c["name"] == "claude")
            container.setdefault("volumeMounts", []).append(
                {
                    "name": "codex-creds",
                    "mountPath": "/etc/codex-creds",
                    "readOnly": True,
                }
            )
            pod_spec.setdefault("volumes", []).append(
                {
                    "name": "codex-creds",
                    "secret": {
                        "secretName": CODEX_CREDS_SECRET,
                        "optional": True,
                    },
                }
            )
        annotations = {
            "tank-operator/owner-email": owner,
            "argocd.argoproj.io/tracking-id": argocd_tracking_id,
        }
        if context_json:
            annotations[GLIMMUNG_CONTEXT_ANNOTATION] = context_json
        return {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {
                "name": deployment_name,
                "namespace": SESSIONS_NAMESPACE,
                "labels": {
                    "app.kubernetes.io/managed-by": "tank-operator",
                    "app.kubernetes.io/instance": ARGOCD_TRACKING_APP,
                    "tank-operator/owner": owner_label,
                    "tank-operator/session-id": session_id,
                    "tank-operator/mode": mode,
                },
                "annotations": annotations,
            },
            "spec": {
                "replicas": 1,
                # No old ReplicaSets — this Deployment is never updated, only
                # created and deleted, so history is just clutter in Argo.
                "revisionHistoryLimit": 0,
                "selector": {"matchLabels": selector_labels},
                "template": {
                    "metadata": {
                        "labels": {
                            "app.kubernetes.io/managed-by": "tank-operator",
                            "tank-operator/owner": owner_label,
                            "tank-operator/session-id": session_id,
                            "tank-operator/mode": mode,
                            "azure.workload.identity/use": "true",
                        },
                    },
                    "spec": pod_spec,
                },
            },
        }

    async def create(
        self,
        owner: str,
        mode: str = DEFAULT_SESSION_MODE,
        glimmung_context: dict[str, Any] | None = None,
    ) -> SessionInfo:
        assert self._apps is not None
        if mode not in SESSION_MODES:
            raise ValueError(f"unknown session mode: {mode!r}")
        # Lazy retry of in-cluster Service resolution — handles the
        # chart-install race where the orchestrator pod starts before its
        # sibling Services exist. After first success the IP is cached;
        # if a Service is ever recreated (rare), restart the orchestrator.
        if self._oauth_gateway_ip is None:
            self._oauth_gateway_ip = await self._resolve_oauth_gateway_ip()
        if self._api_proxy_ip is None:
            self._api_proxy_ip = await self._resolve_service_ip(API_PROXY_HOST, "API proxy")
        # No credential refresh on the create path: the api-proxy
        # (api-proxy/src/tank_api_proxy/server.py) owns rotation now,
        # triggered by upstream 401s on real api.anthropic.com calls.
        # Session pods carry a placeholder Bearer; the proxy strips it
        # and injects the real one, refreshing against platform.claude.com
        # behind the scenes when it observes a 401.
        session_id = uuid.uuid4().hex[:10]
        created = await self._apps.create_namespaced_deployment(
            namespace=SESSIONS_NAMESPACE,
            body=self._deployment_manifest(session_id, owner, mode, glimmung_context),
        )
        # Seed activity so the reaper gives the session a full
        # IDLE_TIMEOUT to receive its first WS before being eligible
        # for deletion.
        self._activity[session_id] = time.monotonic()
        self._ws_count[session_id] = 0
        created_at = None
        if created.metadata and created.metadata.creation_timestamp:
            created_at = created.metadata.creation_timestamp.isoformat()
        return SessionInfo(
            id=session_id,
            pod_name=None,
            owner=owner,
            status="Pending",
            mode=mode,
            created_at=created_at,
        )

    async def list(self, owner: str) -> list[SessionInfo]:
        assert self._apps is not None
        owner_label = _owner_label(owner)
        deployments = await self._apps.list_namespaced_deployment(
            namespace=SESSIONS_NAMESPACE,
            label_selector=f"tank-operator/owner={owner_label}",
        )
        return [
            SessionInfo(
                id=d.metadata.labels.get(
                    "tank-operator/session-id", d.metadata.name
                ),
                pod_name=None,
                owner=owner,
                status=_deployment_status(d),
                mode=d.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE),
                created_at=_deployment_created_at(d),
                name=(d.metadata.annotations or {}).get(NAME_ANNOTATION),
            )
            for d in deployments.items
        ]

    async def get_session(self, owner: str, session_id: str) -> SessionInfo:
        """Look up a single session by id, verifying ownership.

        Cheaper than get_pod_name because it doesn't wait for pod-Ready —
        just reads the Deployment to get mode/status. Use this when you
        only need session metadata (e.g. checking mode before allowing an
        action), and get_pod_name when you need to actually exec into the
        pod.
        """
        assert self._apps is not None
        owner_label = _owner_label(owner)
        try:
            deployment = await self._apps.read_namespaced_deployment(
                name=f"session-{session_id}", namespace=SESSIONS_NAMESPACE
            )
        except client.ApiException as e:
            if e.status == 404:
                raise SessionNotFound(session_id) from e
            raise
        if deployment.metadata.labels.get("tank-operator/owner") != owner_label:
            raise SessionNotOwned(session_id)
        mode = deployment.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        return SessionInfo(
            id=session_id,
            pod_name=None,
            owner=owner,
            status=_deployment_status(deployment),
            mode=mode,
            created_at=_deployment_created_at(deployment),
            name=(deployment.metadata.annotations or {}).get(NAME_ANNOTATION),
        )

    async def set_name(
        self, owner: str, session_id: str, name: str | None
    ) -> SessionInfo:
        """Set or clear the friendly display name on a session.

        Stored as an annotation on the Deployment, so it survives
        orchestrator restarts and is visible to anyone who can read the
        Deployment (the owner, via the existing label-scoped list).
        Pass `None` (or empty string after trim) to clear.

        Strategic-merge-patch semantics: a `None` annotation value tells
        the apiserver to remove the key.
        """
        assert self._apps is not None
        owner_label = _owner_label(owner)
        deployment_name = f"session-{session_id}"
        try:
            deployment = await self._apps.read_namespaced_deployment(
                name=deployment_name, namespace=SESSIONS_NAMESPACE
            )
        except client.ApiException as e:
            if e.status == 404:
                raise SessionNotFound(session_id) from e
            raise
        if deployment.metadata.labels.get("tank-operator/owner") != owner_label:
            raise SessionNotOwned(session_id)
        normalized = name.strip() if name else ""
        annotation_value: str | None = normalized[:MAX_NAME_LENGTH] if normalized else None
        await self._apps.patch_namespaced_deployment(
            name=deployment_name,
            namespace=SESSIONS_NAMESPACE,
            body={"metadata": {"annotations": {NAME_ANNOTATION: annotation_value}}},
        )
        mode = deployment.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        return SessionInfo(
            id=session_id,
            pod_name=None,
            owner=owner,
            status=_deployment_status(deployment),
            mode=mode,
            created_at=_deployment_created_at(deployment),
            name=annotation_value,
        )

    async def get_pod_name(self, owner: str, session_id: str, timeout: float = 90.0) -> str:
        """Look up the pod backing a session, waiting up to `timeout` seconds for it to be Ready."""
        assert self._apps is not None and self._core is not None
        owner_label = _owner_label(owner)
        name = f"session-{session_id}"
        try:
            deployment = await self._apps.read_namespaced_deployment(
                name=name, namespace=SESSIONS_NAMESPACE
            )
        except client.ApiException as e:
            if e.status == 404:
                raise SessionNotFound(session_id) from e
            raise
        if deployment.metadata.labels.get("tank-operator/owner") != owner_label:
            raise SessionNotOwned(session_id)

        deadline = asyncio.get_event_loop().time() + timeout
        while asyncio.get_event_loop().time() < deadline:
            pods = await self._core.list_namespaced_pod(
                namespace=SESSIONS_NAMESPACE,
                label_selector=f"tank-operator/session-id={session_id}",
            )
            for pod in pods.items:
                if _pod_ready(pod):
                    return pod.metadata.name
            await asyncio.sleep(1)
        raise PodNotReady(session_id)

    async def delete(self, owner: str, session_id: str) -> None:
        assert self._apps is not None
        owner_label = _owner_label(owner)
        name = f"session-{session_id}"
        try:
            deployment = await self._apps.read_namespaced_deployment(
                name=name, namespace=SESSIONS_NAMESPACE
            )
        except client.ApiException as e:
            if e.status == 404:
                raise SessionNotFound(session_id) from e
            raise
        if deployment.metadata.labels.get("tank-operator/owner") != owner_label:
            raise SessionNotOwned(session_id)
        await self._apps.delete_namespaced_deployment(
            name=name,
            namespace=SESSIONS_NAMESPACE,
            propagation_policy="Foreground",
        )
        self._ws_count.pop(session_id, None)
        self._activity.pop(session_id, None)

    @contextlib.asynccontextmanager
    async def track_ws(self, session_id: str) -> AsyncIterator[None]:
        """Increment the WS counter for the lifetime of the bridge.

        The reaper treats a session with `_ws_count > 0` as live; on exit we
        bump `_activity` so the IDLE_TIMEOUT clock starts from disconnect,
        not from the last sweep.
        """
        self._ws_count[session_id] = self._ws_count.get(session_id, 0) + 1
        self._activity[session_id] = time.monotonic()
        try:
            yield
        finally:
            self._ws_count[session_id] = max(0, self._ws_count.get(session_id, 1) - 1)
            self._activity[session_id] = time.monotonic()

    async def _reaper_loop(self) -> None:
        while True:
            try:
                await asyncio.sleep(REAPER_INTERVAL_SECONDS)
                await self._reap_idle()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("reaper sweep failed")

    async def _reap_idle(self) -> None:
        assert self._apps is not None
        now = time.monotonic()
        deployments = await self._apps.list_namespaced_deployment(
            namespace=SESSIONS_NAMESPACE,
            label_selector="app.kubernetes.io/managed-by=tank-operator",
        )
        for d in deployments.items:
            session_id = d.metadata.labels.get("tank-operator/session-id")
            if not session_id:
                continue
            if self._ws_count.get(session_id, 0) > 0:
                # Live connection — keep the activity clock current.
                self._activity[session_id] = now
                continue
            last = self._activity.get(session_id)
            if last is None:
                # Orchestrator restart: we don't know how long this session
                # has been idle. Adopt now; the next sweep that finds it
                # still idle will reap.
                self._activity[session_id] = now
                continue
            if now - last < IDLE_TIMEOUT_SECONDS:
                continue
            log.info("reaping idle session %s (idle %.0fs)", session_id, now - last)
            try:
                await self._apps.delete_namespaced_deployment(
                    name=d.metadata.name,
                    namespace=SESSIONS_NAMESPACE,
                    propagation_policy="Foreground",
                )
            except client.ApiException:
                log.exception("failed to delete idle session %s", session_id)
                continue
            self._ws_count.pop(session_id, None)
            self._activity.pop(session_id, None)


def _deployment_status(deployment: Any) -> str:
    """Map a Deployment's status to the same vocabulary the frontend already uses."""
    status = deployment.status
    if status is None:
        return "Pending"
    ready = status.ready_replicas or 0
    if ready >= 1:
        return "Active"
    if status.conditions:
        for c in status.conditions:
            # ReplicaFailure or Progressing=False with reason ProgressDeadlineExceeded
            # both indicate the rollout has given up. Treat as Failed so the UI
            # shows red and the user can delete + retry.
            if c.type == "ReplicaFailure" and c.status == "True":
                return "Failed"
            if c.type == "Progressing" and c.status == "False":
                return "Failed"
    return "Pending"


def _deployment_created_at(deployment: Any) -> str | None:
    if not deployment.metadata or not deployment.metadata.creation_timestamp:
        return None
    return deployment.metadata.creation_timestamp.isoformat()


def _pod_ready(pod: Any) -> bool:
    if not pod.status or pod.status.phase != "Running":
        return False
    statuses = pod.status.container_statuses or []
    return bool(statuses) and all(cs.ready for cs in statuses)
