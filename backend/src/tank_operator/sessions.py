import asyncio
import contextlib
import datetime
import hashlib
import json
import logging
import os
import shlex
import socket
import time
from dataclasses import asdict, dataclass
from typing import Any, AsyncIterator

from kubernetes_asyncio import client, config

from .profiles import ActiveRunStore, SessionRecord, SessionRegistryStore

log = logging.getLogger(__name__)

SESSIONS_NAMESPACE = os.environ.get("SESSIONS_NAMESPACE", "tank-operator-sessions")
SESSION_IMAGE = os.environ.get(
    "SESSION_IMAGE", "romainecr.azurecr.io/claude-container:latest"
)
CODEX_SESSION_IMAGE = os.environ.get(
    "CODEX_SESSION_IMAGE", "romainecr.azurecr.io/codex-container:latest"
)
PI_SESSION_IMAGE = os.environ.get(
    "PI_SESSION_IMAGE", "romainecr.azurecr.io/pi-container:latest"
)
SESSION_SERVICE_ACCOUNT = os.environ.get("SESSION_SERVICE_ACCOUNT", "claude-session")
SESSION_CONFIGMAP = os.environ.get("SESSION_CONFIGMAP", "tank-session-config")
GITHUB_APP_SECRET = os.environ.get("GITHUB_APP_SECRET", "github-app-creds")
SANDBOX_AGENT_PORT = int(os.environ.get("SANDBOX_AGENT_PORT", "2468"))
# OAuth gateway: in-cluster service that impersonates platform.claude.com.
# Session pods reach it via a hostAlias mapping platform.claude.com to this
# Service's ClusterIP — hostAliases requires an IP, not a DNS name, so we
# resolve once at startup and stamp the IP onto every session Pod manifest.
OAUTH_GATEWAY_HOST = os.environ.get(
    "CLAUDE_OAUTH_GATEWAY_HOST",
    "claude-oauth-gateway.tank-operator.svc.cluster.local",
)
OAUTH_GATEWAY_CA_CONFIGMAP = os.environ.get(
    "CLAUDE_OAUTH_GATEWAY_CA_CONFIGMAP", "claude-oauth-ca"
)
# In-cluster proxy that fronts api.anthropic.com. Same hostAlias trick as
# the OAuth gateway (DNS resolution at orchestrator startup, IP literal
# stamped onto each session Pod manifest). Pods send their requests to
# api.anthropic.com normally; the proxy strips their placeholder
# Authorization header, injects the current real OAuth Bearer, and
# refreshes against platform.claude.com on upstream 401.
API_PROXY_HOST = os.environ.get(
    "CLAUDE_API_PROXY_HOST",
    "claude-api-proxy.tank-operator.svc.cluster.local",
)
# Stamping these on each session Pod makes ArgoCD claim it into the
# tank-operator-sessions Application's resource tree (visible alongside the
# orchestrator's chart-managed resources). That app has no auto-sync, so
# Argo never tries to reconcile / prune the dynamic runtime objects — pure
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


class SessionTerminalUnavailable(Exception):
    pass


class PodNotReady(Exception):
    pass


API_KEY_MODE = "api_key"
CLAUDE_CLI_MODE = "claude_cli"
CLAUDE_GUI_MODE = "claude_gui"
CODEX_CLI_MODE = "codex_cli"
CODEX_GUI_MODE = "codex_gui"
PI_CLI_MODE = "pi_cli"
SESSION_MODE_ALIASES = {
    "subscription": CLAUDE_CLI_MODE,
    "subscription_headless": CLAUDE_GUI_MODE,
    "codex_subscription": CODEX_CLI_MODE,
    "codex_headless": CODEX_GUI_MODE,
    "pi_subscription": PI_CLI_MODE,
}
SESSION_MODES = (
    API_KEY_MODE,
    CLAUDE_CLI_MODE,
    CLAUDE_GUI_MODE,
    "config",
    "codex_config",
    CODEX_CLI_MODE,
    CODEX_GUI_MODE,
    "pi_config",
    PI_CLI_MODE,
)
ACCEPTED_SESSION_MODES = SESSION_MODES + tuple(SESSION_MODE_ALIASES)
DEFAULT_SESSION_MODE = CLAUDE_CLI_MODE
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
# rewrite — Secret volumes are read-only) and launches `codex`.
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
CODEX_SUBSCRIPTION_MODE = CODEX_CLI_MODE
CODEX_HEADLESS_MODE = CODEX_GUI_MODE
CODEX_CREDS_SECRET = os.environ.get("CODEX_CREDS_SECRET", "codex-credentials")
SUBSCRIPTION_HEADLESS_MODE = CLAUDE_GUI_MODE
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
PI_SUBSCRIPTION_MODE = PI_CLI_MODE
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
        CODEX_HEADLESS_MODE,
        PI_CONFIG_MODE,
    }
)
CODEX_MODES = frozenset(
    {CODEX_CONFIG_MODE, CODEX_SUBSCRIPTION_MODE, CODEX_HEADLESS_MODE}
)
PI_MODES = frozenset({PI_CONFIG_MODE, PI_SUBSCRIPTION_MODE})
HEADLESS_MODES = frozenset({SUBSCRIPTION_HEADLESS_MODE, CODEX_HEADLESS_MODE})
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
# Pod so it survives orchestrator restarts without a separate store.
# K8s allows up to ~256 KB of annotations per object — we cap inbound names
# well below that for UI sanity.
NAME_ANNOTATION = "tank-operator/display-name"


def normalize_session_mode(mode: str | None) -> str:
    """Return the canonical mode name while accepting legacy API/pod values."""
    raw = mode or DEFAULT_SESSION_MODE
    return SESSION_MODE_ALIASES.get(raw, raw)


MAX_NAME_LENGTH = 80
GLIMMUNG_CONTEXT_ANNOTATION = "tank-operator/glimmung-context"
TEST_STATE_ANNOTATION = "tank-operator/test-state"
ROLLOUT_STATE_ANNOTATION = "tank-operator/rollout-state"
MAX_TEST_URL_LENGTH = 512


SESSION_CONFIG_MOUNTS = (
    ("mcp.json", "/workspace/.mcp.json"),
    ("default-claude.md", "/workspace/CLAUDE.md"),
    ("default-claude.md", "/workspace/AGENTS.md"),
    ("write-glimmung-context.sh", "/opt/tank/write-glimmung-context.sh"),
    ("tank-bootstrap.sh", "/opt/tank/bootstrap.sh"),
    ("headless-run.sh", "/opt/tank/headless-run.sh"),
)
SESSION_CONFIG_DIR_MOUNT = "/opt/tank/session-config"


@dataclass
class SessionInfo:
    id: str
    pod_name: str | None
    owner: str
    status: str
    mode: str
    # ISO timestamp captured by the backend when it begins handling the
    # session create request. This is the user's perceived startup clock.
    requested_at: str | None = None
    # ISO timestamp from the Pod's creation time. The frontend uses
    # this to show a small "running for" indicator on session rows.
    created_at: str | None = None
    # ISO timestamp from the Pod Ready condition. The frontend derives a
    # compact startup indicator from requested_at to this point.
    ready_at: str | None = None
    # User-provided friendly name. None when unset; the frontend falls back
    # to the session id slug. The slug stays canonical in URLs and the
    # Pod name — this is purely a display label.
    name: str | None = None
    test_state: dict[str, Any] | None = None
    rollout_state: dict[str, Any] | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def _now_iso() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def _owner_label(email: str) -> str:
    # K8s label values must match [a-z0-9A-Z._-]{0,63}; email addresses contain `@`.
    digest = hashlib.sha256(email.encode()).hexdigest()[:16]
    return f"u-{digest}"


def _session_config_mounts() -> list[dict[str, Any]]:
    mounts = [
        {
            "name": "session-config",
            "mountPath": mount_path,
            "subPath": key,
            "readOnly": True,
        }
        for key, mount_path in SESSION_CONFIG_MOUNTS
    ]
    mounts.append(
        {
            "name": "session-config",
            "mountPath": SESSION_CONFIG_DIR_MOUNT,
            "readOnly": True,
        }
    )
    return mounts


def _session_config_volume() -> dict[str, Any]:
    return {"name": "session-config", "configMap": {"name": SESSION_CONFIGMAP}}


def _test_state_from_annotations(
    annotations: dict[str, str] | None,
) -> dict[str, Any] | None:
    if not annotations:
        return None
    raw = annotations.get(TEST_STATE_ANNOTATION)
    if not raw:
        return None
    try:
        parsed = json.loads(raw)
    except (TypeError, json.JSONDecodeError):
        return None
    if not isinstance(parsed, dict):
        return None
    return parsed


def _rollout_state_from_annotations(
    annotations: dict[str, str] | None,
) -> dict[str, Any] | None:
    if not annotations:
        return None
    raw = annotations.get(ROLLOUT_STATE_ANNOTATION)
    if not raw:
        return None
    try:
        parsed = json.loads(raw)
    except (TypeError, json.JSONDecodeError):
        return None
    if not isinstance(parsed, dict):
        return None
    return parsed


class SessionManager:
    """Manages session lifecycle as one Pod per session.

    A session Pod is the runtime identity: workspace, agent process, and
    browser terminal transport all live there. If the Pod dies, the session is
    failed rather than silently recreated as a new empty runtime.
    """

    def __init__(
        self,
        registry: SessionRegistryStore | None = None,
        events: Any | None = None,
        active_runs: ActiveRunStore | None = None,
    ) -> None:
        self._api: client.ApiClient | None = None
        self._apps: client.AppsV1Api | None = None
        self._core: client.CoreV1Api | None = None
        self._registry = registry
        self._events = events
        self._active_runs = active_runs
        # In-memory connection tracking for the idle reaper. Single replica
        # only (values.yaml pins replicas: 1) — stateful, restart-tolerant
        # via the "adopt with now" branch in _reap_idle.
        self._ws_count: dict[str, int] = {}
        self._activity: dict[str, float] = {}
        self._reaper_task: asyncio.Task[None] | None = None
        self._local_session_counter = 0
        self._local_session_counter_lock = asyncio.Lock()
        # ClusterIP of the OAuth gateway Service — resolved once at startup
        # and stamped onto each Pod as a hostAlias, since K8s
        # hostAliases require an IP literal, not a DNS name.
        self._oauth_gateway_ip: str | None = None
        # Same idea for the api.anthropic.com proxy — see API_PROXY_HOST.
        self._api_proxy_ip: str | None = None

    def set_registry(self, registry: SessionRegistryStore) -> None:
        self._registry = registry

    def set_active_runs(self, active_runs: ActiveRunStore) -> None:
        self._active_runs = active_runs

    def _publish_changed(self, owner: str) -> None:
        if self._events is not None:
            self._events.publish(owner)

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
            log.warning(
                "could not resolve %s %s; sessions will boot without it", label, host
            )
            return None

    async def shutdown(self) -> None:
        if self._reaper_task is not None:
            self._reaper_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await self._reaper_task
        if self._api is not None:
            await self._api.close()

    def _pod_manifest(
        self,
        session_id: str,
        owner: str,
        mode: str,
        glimmung_context: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        mode = normalize_session_mode(mode)
        owner_label = _owner_label(owner)
        pod_name = f"session-{session_id}"
        argocd_tracking_id = (
            f"{ARGOCD_TRACKING_APP}:/Pod:{SESSIONS_NAMESPACE}/{pod_name}"
        )
        context_json = ""
        if glimmung_context is not None:
            context_json = json.dumps(
                glimmung_context, sort_keys=True, separators=(",", ":")
            )
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
                    "command": [
                        "bash",
                        "-lc",
                        (
                            "if command -v sandbox-agent >/dev/null 2>&1; then "
                            "sandbox_agent_cmd=sandbox-agent; "
                            "else sandbox_agent_cmd='npx -y @sandbox-agent/cli@0.4.2'; fi; "
                            f"exec $sandbox_agent_cmd server --host 0.0.0.0 --port {SANDBOX_AGENT_PORT} "
                            "--no-token --no-telemetry"
                        ),
                    ],
                    "ports": [
                        {"name": "sandbox-agent", "containerPort": SANDBOX_AGENT_PORT},
                    ],
                    "env": [
                        {
                            "name": "SANDBOX_AGENT_PORT",
                            "value": str(SANDBOX_AGENT_PORT),
                        },
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
                            "name": "TANK_GLIMMUNG_RUN_REF",
                            "value": str(
                                (glimmung_context or {}).get("glimmung_run_ref") or ""
                            ),
                        },
                        {
                            "name": "TANK_GLIMMUNG_ISSUE_REF",
                            "value": str(
                                (glimmung_context or {}).get("glimmung_issue_ref") or ""
                            ),
                        },
                        {
                            "name": "TANK_GLIMMUNG_TOUCHPOINT_REF",
                            "value": str(
                                (glimmung_context or {}).get("glimmung_touchpoint_ref") or ""
                            ),
                        },
                        {
                            "name": "TANK_GLIMMUNG_VALIDATION_URL",
                            "value": str(
                                (glimmung_context or {}).get("validation_url") or ""
                            ),
                        },
                        # Force claude (and anything else using the
                        # `supports-hyperlinks` npm lib) to emit OSC 8
                        # hyperlinks when a CLI surface is attached.
                        {"name": "FORCE_HYPERLINK", "value": "1"},
                        # Switch claude's TUI to the alternate-screen-buffer
                        # renderer (vim/htop-style) instead of the default
                        # in-place redraw. Fixes the documented Ink
                        # SIGWINCH redraw-leak (anthropics/claude-code#49086)
                        # and full-buffer redraw drift (#29937) — both of
                        # which manifest as ghost lines and post-resize text
                        # collisions in browser-hosted terminal renderers.
                        {"name": "CLAUDE_CODE_NO_FLICKER", "value": "1"},
                    ],
                    "envFrom": [
                        {"secretRef": {"name": GITHUB_APP_SECRET}},
                    ],
                    "volumeMounts": _session_config_mounts(),
                },
            ],
            "volumes": [
                _session_config_volume(),
            ],
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
        if mode not in NO_CLAUDE_HIJACK_MODES and (
            self._oauth_gateway_ip or self._api_proxy_ip
        ):
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
        if mode in (CODEX_SUBSCRIPTION_MODE, CODEX_HEADLESS_MODE):
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
            "apiVersion": "v1",
            "kind": "Pod",
            "metadata": {
                "name": pod_name,
                "namespace": SESSIONS_NAMESPACE,
                "labels": {
                    "app.kubernetes.io/managed-by": "tank-operator",
                    "app.kubernetes.io/instance": ARGOCD_TRACKING_APP,
                    "tank-operator/owner": owner_label,
                    "tank-operator/session-id": session_id,
                    "tank-operator/mode": mode,
                    "azure.workload.identity/use": "true",
                },
                "annotations": annotations,
            },
            "spec": pod_spec,
        }

    async def create(
        self,
        owner: str,
        mode: str = DEFAULT_SESSION_MODE,
        glimmung_context: dict[str, Any] | None = None,
        requested_at: str | None = None,
    ) -> SessionInfo:
        assert self._core is not None
        mode = normalize_session_mode(mode)
        if mode not in SESSION_MODES:
            raise ValueError(f"unknown session mode: {mode!r}")
        request_started_at = requested_at or _now_iso()
        # Lazy retry of in-cluster Service resolution — handles the
        # chart-install race where the orchestrator pod starts before its
        # sibling Services exist. After first success the IP is cached;
        # if a Service is ever recreated (rare), restart the orchestrator.
        if self._oauth_gateway_ip is None:
            self._oauth_gateway_ip = await self._resolve_oauth_gateway_ip()
        if self._api_proxy_ip is None:
            self._api_proxy_ip = await self._resolve_service_ip(
                API_PROXY_HOST, "API proxy"
            )
        # No credential refresh on the create path: the api-proxy
        # (api-proxy/src/tank_api_proxy/server.py) owns rotation now,
        # triggered by upstream 401s on real api.anthropic.com calls.
        # Session pods carry a placeholder Bearer; the proxy strips it
        # and injects the real one, refreshing against platform.claude.com
        # behind the scenes when it observes a 401.
        session_id = await self._next_session_id()
        created = await self._core.create_namespaced_pod(
            namespace=SESSIONS_NAMESPACE,
            body=self._pod_manifest(session_id, owner, mode, glimmung_context),
        )
        # Seed activity so the reaper gives the session a full
        # IDLE_TIMEOUT to receive its first WS before being eligible
        # for deletion.
        self._activity[session_id] = time.monotonic()
        self._ws_count[session_id] = 0
        created_at = None
        if created.metadata and created.metadata.creation_timestamp:
            created_at = created.metadata.creation_timestamp.isoformat()
        info = SessionInfo(
            id=session_id,
            pod_name=created.metadata.name if created.metadata else None,
            owner=owner,
            status="Pending",
            mode=mode,
            requested_at=request_started_at,
            created_at=created_at,
            ready_at=_pod_ready_at(created),
        )
        if self._registry is not None:
            await self._registry.upsert(
                email=owner,
                session_id=session_id,
                mode=mode,
                pod_name=info.pod_name,
                requested_at=request_started_at,
                created_at=created_at,
            )
        self._publish_changed(owner)
        return info

    async def _next_session_id(self) -> str:
        if self._registry is not None:
            return await self._registry.next_session_id()
        async with self._local_session_counter_lock:
            self._local_session_counter += 1
            return str(self._local_session_counter)

    async def dispatch_headless(
        self,
        owner: str,
        session_id: str,
        prompt: str,
        *,
        follow_up: bool,
        model: str,
        permission_mode: str,
    ) -> None:
        """Fire-and-forget launch of headless-run.sh on a session pod.

        Used by the HTTP handoff endpoints (POST /api/sessions/run, POST
        /api/sessions/{id}/messages). Returns once the command has been
        spawned on the pod — does not wait for the agent run to complete.
        Conversation output lands in claude-code's session JSONL, which the
        existing /api/sessions/{id}/run/history endpoint reads back.
        Output is written to the same /tmp/tank-run-<id>.stream and pid-file
        shape used by live WebSocket runs, so the browser can discover and
        attach to a fire-and-forget follow-up after it starts.
        """
        # Late import to dodge an api.py → sessions.py → api.py cycle. The
        # script-shape helpers live next to the WS endpoint that introduced
        # them, but dispatch_headless reuses them so both paths produce the
        # exact same on-pod command.
        from .api import (
            _build_headless_script,
            _build_live_run_script,
            _new_prompt_path,
            _run_pid_path,
            _run_stream_path,
            _validate_headless_arg,
            _validate_run_id,
        )
        from .exec_proxy import exec_launch_detached, exec_write_file

        session = await self.get_session(owner=owner, session_id=session_id)
        mode = normalize_session_mode(session.mode)
        if mode not in HEADLESS_MODES:
            raise ValueError(
                f"session mode {session.mode!r} does not support headless dispatch"
            )
        pod_name = await self.get_pod_name(owner=owner, session_id=session_id)

        safe_model = _validate_headless_arg(model)
        safe_pm = _validate_headless_arg(permission_mode)
        if model and not safe_model:
            raise ValueError("invalid model")
        if permission_mode and not safe_pm:
            raise ValueError("invalid permission_mode")

        provider = "codex" if mode == CODEX_HEADLESS_MODE else "claude"
        prompt_path = _new_prompt_path()
        run_id = _validate_run_id(None)
        stream_path = _run_stream_path(run_id)
        pid_path = _run_pid_path(run_id)
        await exec_write_file(
            SESSIONS_NAMESPACE, pod_name, prompt_path, prompt.encode()
        )
        script = _build_headless_script(
            provider=provider,
            prompt_path=prompt_path,
            follow_up=follow_up,
            model=safe_model,
            permission_mode=safe_pm,
        )
        await exec_launch_detached(
            namespace=SESSIONS_NAMESPACE,
            pod_name=pod_name,
            command=_build_live_run_script(script, pid_path),
            log_path=stream_path,
        )
        if provider == "claude":
            asyncio.create_task(
                self._schedule_headless_wakeup(
                    owner=owner,
                    session_id=session_id,
                    pod_name=pod_name,
                    stream_path=stream_path,
                    pid_path=pid_path,
                    model=safe_model or "",
                    permission_mode=safe_pm or "",
                ),
                name=f"wakeup-watcher-{run_id}",
            )
        if self._active_runs is not None:
            try:
                await self._active_runs.start(
                    email=owner,
                    session_id=session_id,
                    run_id=run_id,
                    pod_name=pod_name,
                    provider=provider,
                    stream_path=stream_path,
                    pid_path=pid_path,
                )
            except Exception as exc:
                log.warning("failed to persist active run %s: %s", run_id, exc)
        self._publish_changed(owner)

    async def _schedule_headless_wakeup(
        self,
        *,
        owner: str,
        session_id: str,
        pod_name: str,
        stream_path: str,
        pid_path: str,
        model: str,
        permission_mode: str,
    ) -> None:
        """Background task: once a headless run finishes, fire ScheduleWakeup if the agent requested one.

        Polls for pid-file removal (run exit), reads the completed stream JSONL,
        and if a ScheduleWakeup tool_use is found, sleeps for the requested delay
        then calls dispatch_headless with follow_up=True.
        """
        from .exec_proxy import exec_capture

        quoted_pid = shlex.quote(pid_path)
        quoted_stream = shlex.quote(stream_path)

        # Poll until the pid file is gone (the agent process has exited).
        # Max ~4 hours at 10s intervals; gives up quietly on any exec error.
        for _ in range(1440):
            try:
                out = await exec_capture(
                    SESSIONS_NAMESPACE,
                    pod_name,
                    ["bash", "-c", f"test -f {quoted_pid} && echo alive || echo done"],
                )
                if b"done" in out:
                    break
            except Exception:
                return
            await asyncio.sleep(10)

        # Read the completed stream JSONL.
        try:
            stream_bytes = await exec_capture(
                SESSIONS_NAMESPACE,
                pod_name,
                ["bash", "-c", f"cat {quoted_stream} 2>/dev/null || true"],
            )
        except Exception as exc:
            log.warning("wakeup watcher could not read stream %s: %s", stream_path, exc)
            return

        # Scan for the last ScheduleWakeup tool_use in the assistant stream.
        delay_seconds: int | None = None
        wakeup_prompt: str | None = None
        for raw_line in stream_bytes.decode(errors="replace").splitlines():
            line = raw_line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if not isinstance(event, dict) or event.get("type") != "assistant":
                continue
            message = event.get("message")
            if not isinstance(message, dict):
                continue
            content = message.get("content")
            if not isinstance(content, list):
                continue
            for block in content:
                if not isinstance(block, dict) or block.get("type") != "tool_use":
                    continue
                if (block.get("name") or "").lower() != "schedulewakeup":
                    continue
                inp = block.get("input")
                if not isinstance(inp, dict):
                    continue
                raw_delay = inp.get("delaySeconds")
                raw_prompt = inp.get("prompt")
                if raw_delay is not None and raw_prompt is not None:
                    try:
                        delay_seconds = max(0, int(raw_delay))
                        wakeup_prompt = str(raw_prompt)
                    except (TypeError, ValueError):
                        pass

        if delay_seconds is None or wakeup_prompt is None:
            return

        log.info(
            "scheduling wakeup for session %s in %ds", session_id, delay_seconds
        )
        if delay_seconds > 0:
            await asyncio.sleep(delay_seconds)

        try:
            await self.dispatch_headless(
                owner=owner,
                session_id=session_id,
                prompt=wakeup_prompt,
                follow_up=True,
                model=model,
                permission_mode=permission_mode,
            )
            log.info("wakeup dispatched for session %s", session_id)
        except Exception as exc:
            log.warning("wakeup dispatch failed for session %s: %s", session_id, exc)

    async def list(self, owner: str) -> list[SessionInfo]:
        assert self._core is not None
        owner_label = _owner_label(owner)
        pods = await self._core.list_namespaced_pod(
            namespace=SESSIONS_NAMESPACE,
            label_selector=f"tank-operator/owner={owner_label}",
        )
        pods_by_id = {_session_id_from_pod(p): p for p in pods.items}
        if self._registry is None:
            return [
                _session_info_from_pod(owner, p)
                for p in pods.items
                if _pod_has_sandbox_agent(p)
            ]

        for session_id, pod in pods_by_id.items():
            if not _pod_has_sandbox_agent(pod):
                continue
            if await self._registry.get(owner, session_id) is not None:
                continue
            await self._registry.upsert(
                email=owner,
                session_id=session_id,
                mode=normalize_session_mode(
                    pod.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
                ),
                pod_name=pod.metadata.name,
                name=(pod.metadata.annotations or {}).get(NAME_ANNOTATION),
                requested_at=_pod_created_at(pod),
                created_at=_pod_created_at(pod),
            )

        records = await self._registry.list(owner)
        return [
            _session_info_from_record(owner, record, pods_by_id.get(record.id))
            for record in records
        ]

    async def get_session(self, owner: str, session_id: str) -> SessionInfo:
        """Look up a single session by id, verifying ownership.

        Cheaper than get_pod_name because it doesn't wait for pod-Ready —
        just reads the Pod to get mode/status. Use this when you
        only need session metadata (e.g. checking mode before allowing an
        action), and get_pod_name when you need to actually exec into the
        pod.
        """
        pod = await self._read_owned_pod(owner, session_id)
        mode = normalize_session_mode(
            pod.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        )
        return SessionInfo(
            id=session_id,
            pod_name=pod.metadata.name,
            owner=owner,
            status=_pod_status(pod),
            mode=mode,
            requested_at=_pod_created_at(pod),
            created_at=_pod_created_at(pod),
            ready_at=_pod_ready_at(pod),
            name=(pod.metadata.annotations or {}).get(NAME_ANNOTATION),
            test_state=_test_state_from_annotations(pod.metadata.annotations or {}),
            rollout_state=_rollout_state_from_annotations(
                pod.metadata.annotations or {}
            ),
        )

    async def set_name(
        self, owner: str, session_id: str, name: str | None
    ) -> SessionInfo:
        """Set or clear the friendly display name on a session.

        Stored as an annotation on the Pod, so it survives
        orchestrator restarts and is visible to anyone who can read the
        Pod (the owner, via the existing label-scoped list).
        Pass `None` (or empty string after trim) to clear.

        Strategic-merge-patch semantics: a `None` annotation value tells
        the apiserver to remove the key.
        """
        assert self._core is not None
        pod = await self._read_owned_pod(owner, session_id)
        record = (
            await self._registry.get(owner, session_id)
            if self._registry is not None
            else None
        )
        normalized = name.strip() if name else ""
        annotation_value: str | None = (
            normalized[:MAX_NAME_LENGTH] if normalized else None
        )
        patched = await self._core.patch_namespaced_pod(
            name=pod.metadata.name,
            namespace=SESSIONS_NAMESPACE,
            body={"metadata": {"annotations": {NAME_ANNOTATION: annotation_value}}},
        )
        mode = normalize_session_mode(
            patched.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        )
        info = SessionInfo(
            id=session_id,
            pod_name=patched.metadata.name,
            owner=owner,
            status=_pod_status(patched),
            mode=mode,
            requested_at=(record.requested_at if record else None)
            or _pod_created_at(patched),
            created_at=_pod_created_at(patched),
            ready_at=_pod_ready_at(patched),
            name=annotation_value,
        )
        if self._registry is not None:
            await self._registry.upsert(
                email=owner,
                session_id=session_id,
                mode=mode,
                pod_name=patched.metadata.name,
                name=annotation_value,
                requested_at=info.requested_at,
                created_at=info.created_at,
            )
        self._publish_changed(owner)
        return info

    async def set_test_state(
        self,
        owner: str,
        session_id: str,
        *,
        active: bool = True,
        slot_index: int | None = None,
        url: str | None = None,
    ) -> SessionInfo:
        """Set or clear the GUI test-environment state for a session."""
        assert self._core is not None
        pod = await self._read_owned_pod(owner, session_id)
        record = (
            await self._registry.get(owner, session_id)
            if self._registry is not None
            else None
        )
        state: dict[str, Any] | None
        if active:
            state = {"active": True}
            if slot_index is not None:
                state["slot_index"] = slot_index
            clean_url = (url or "").strip()
            if clean_url:
                state["url"] = clean_url[:MAX_TEST_URL_LENGTH]
            annotation_value: str | None = json.dumps(
                state, sort_keys=True, separators=(",", ":")
            )
        else:
            state = None
            annotation_value = None
        annotations = {TEST_STATE_ANNOTATION: annotation_value}
        if active:
            annotations[ROLLOUT_STATE_ANNOTATION] = None
        patched = await self._core.patch_namespaced_pod(
            name=pod.metadata.name,
            namespace=SESSIONS_NAMESPACE,
            body={"metadata": {"annotations": annotations}},
        )
        mode = normalize_session_mode(
            patched.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        )
        info = SessionInfo(
            id=session_id,
            pod_name=patched.metadata.name,
            owner=owner,
            status=_pod_status(patched),
            mode=mode,
            requested_at=(record.requested_at if record else None)
            or _pod_created_at(patched),
            created_at=_pod_created_at(patched),
            ready_at=_pod_ready_at(patched),
            name=(patched.metadata.annotations or {}).get(NAME_ANNOTATION),
            test_state=state,
            rollout_state=None if active else _rollout_state_from_annotations(
                patched.metadata.annotations or {}
            ),
        )
        self._publish_changed(owner)
        return info

    async def set_rollout_state(
        self,
        owner: str,
        session_id: str,
        *,
        active: bool = True,
    ) -> SessionInfo:
        """Set or clear the GUI rollout state for a session."""
        assert self._core is not None
        pod = await self._read_owned_pod(owner, session_id)
        record = (
            await self._registry.get(owner, session_id)
            if self._registry is not None
            else None
        )
        state: dict[str, Any] | None
        if active:
            state = {"active": True}
            annotation_value: str | None = json.dumps(
                state, sort_keys=True, separators=(",", ":")
            )
        else:
            state = None
            annotation_value = None
        annotations = {ROLLOUT_STATE_ANNOTATION: annotation_value}
        if active:
            annotations[TEST_STATE_ANNOTATION] = None
        patched = await self._core.patch_namespaced_pod(
            name=pod.metadata.name,
            namespace=SESSIONS_NAMESPACE,
            body={"metadata": {"annotations": annotations}},
        )
        mode = normalize_session_mode(
            patched.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        )
        info = SessionInfo(
            id=session_id,
            pod_name=patched.metadata.name,
            owner=owner,
            status=_pod_status(patched),
            mode=mode,
            requested_at=(record.requested_at if record else None)
            or _pod_created_at(patched),
            created_at=_pod_created_at(patched),
            ready_at=_pod_ready_at(patched),
            name=(patched.metadata.annotations or {}).get(NAME_ANNOTATION),
            test_state=(
                None
                if active
                else _test_state_from_annotations(patched.metadata.annotations or {})
            ),
            rollout_state=state,
        )
        self._publish_changed(owner)
        return info

    async def get_pod_name(
        self, owner: str, session_id: str, timeout: float = 90.0
    ) -> str:
        """Look up the pod backing a session, waiting up to `timeout` seconds for it to be Ready."""
        deadline = asyncio.get_event_loop().time() + timeout
        while asyncio.get_event_loop().time() < deadline:
            pod = await self._read_owned_pod(owner, session_id)
            if _pod_ready(pod):
                return pod.metadata.name
            await asyncio.sleep(1)
        raise PodNotReady(session_id)

    async def get_terminal_endpoint(
        self, owner: str, session_id: str, timeout: float = 90.0
    ) -> tuple[str, int]:
        """Return the current session pod IP and sandbox-agent port."""
        deadline = asyncio.get_event_loop().time() + timeout
        while asyncio.get_event_loop().time() < deadline:
            pod = await self._read_owned_pod(owner, session_id)
            if _pod_ready(pod) and pod.status and pod.status.pod_ip:
                if not _pod_has_sandbox_agent(pod):
                    raise SessionTerminalUnavailable(session_id)
                return pod.status.pod_ip, SANDBOX_AGENT_PORT
            await asyncio.sleep(1)
        raise PodNotReady(session_id)

    async def delete(self, owner: str, session_id: str) -> None:
        assert self._core is not None
        try:
            pod = await self._read_owned_pod(owner, session_id)
        except SessionNotFound:
            if self._registry is not None:
                record = await self._registry.get(owner, session_id)
                if record is not None:
                    await self._registry.mark_deleted(owner, session_id)
                    self._ws_count.pop(session_id, None)
                    self._activity.pop(session_id, None)
                    self._publish_changed(owner)
                    return
            raise
        await self._delete_session_runtime(pod)
        if self._registry is not None:
            await self._registry.mark_deleted(owner, session_id)
        self._ws_count.pop(session_id, None)
        self._activity.pop(session_id, None)
        self._publish_changed(owner)

    async def touch(self, owner: str, session_id: str) -> None:
        await self._read_owned_pod(owner, session_id)
        self._activity[session_id] = time.monotonic()

    async def find_pod_by_ip(self, pod_ip: str) -> Any | None:
        """Look up a session pod by its current ``status.podIP``.

        Used by the internal ``/api/internal/resolve-caller`` endpoint
        (#57 stage 3) to map an inbound MCP request's source IP back to
        the session pod that issued it. Returns ``None`` when no pod in
        the sessions namespace currently has that IP (raced deletion,
        non-session caller, stale cache on the proxy side).

        We list managed-by=tank-operator pods rather than filtering on
        ``status.podIP=`` because that field selector isn't index-backed
        on apiserver and the namespace stays small.
        """
        if not pod_ip or self._core is None:
            return None
        pods = await self._core.list_namespaced_pod(
            namespace=SESSIONS_NAMESPACE,
            label_selector="app.kubernetes.io/managed-by=tank-operator",
        )
        for pod in pods.items:
            status = getattr(pod, "status", None)
            if status and getattr(status, "pod_ip", None) == pod_ip:
                return pod
        return None

    async def _read_owned_pod(self, owner: str, session_id: str) -> Any:
        assert self._core is not None
        owner_label = _owner_label(owner)
        try:
            pod = await self._core.read_namespaced_pod(
                name=f"session-{session_id}", namespace=SESSIONS_NAMESPACE
            )
        except client.ApiException as e:
            if e.status == 404:
                pods = await self._core.list_namespaced_pod(
                    namespace=SESSIONS_NAMESPACE,
                    label_selector=f"tank-operator/session-id={session_id}",
                )
                if not pods.items:
                    raise SessionNotFound(session_id) from e
                pod = pods.items[0]
            else:
                raise
        if pod.metadata.labels.get("tank-operator/owner") != owner_label:
            raise SessionNotOwned(session_id)
        return pod

    async def _delete_session_runtime(self, pod: Any) -> None:
        assert self._core is not None
        deployment_name = _legacy_deployment_owner(pod)
        if deployment_name and self._apps is not None:
            try:
                await self._apps.delete_namespaced_deployment(
                    name=deployment_name,
                    namespace=SESSIONS_NAMESPACE,
                    propagation_policy="Foreground",
                )
                return
            except client.ApiException as e:
                if e.status != 404:
                    raise
        await self._core.delete_namespaced_pod(
            name=pod.metadata.name,
            namespace=SESSIONS_NAMESPACE,
        )

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
        assert self._core is not None
        now = time.monotonic()
        pods = await self._core.list_namespaced_pod(
            namespace=SESSIONS_NAMESPACE,
            label_selector="app.kubernetes.io/managed-by=tank-operator",
        )
        for pod in pods.items:
            session_id = pod.metadata.labels.get("tank-operator/session-id")
            if not session_id:
                continue
            if not _pod_has_sandbox_agent(pod):
                # Pre-sandbox-agent sessions cannot prove liveness through the
                # browser terminal/run WebSocket anymore. Leave them for
                # explicit deletion so kubectl-attached users are not
                # disconnected by the reaper.
                self._activity[session_id] = now
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
                await self._delete_session_runtime(pod)
            except client.ApiException:
                log.exception("failed to delete idle session %s", session_id)
                continue
            self._ws_count.pop(session_id, None)
            self._activity.pop(session_id, None)


def _pod_status(pod: Any) -> str:
    """Map a Pod's status to the same vocabulary the frontend already uses."""
    status = pod.status
    if status is None:
        return "Pending"
    if status.phase == "Running" and _pod_ready(pod):
        return "Active"
    if status.phase in ("Failed", "Succeeded"):
        return "Failed"
    statuses = status.container_statuses or []
    if any(
        cs.state and cs.state.waiting and cs.state.waiting.reason == "CrashLoopBackOff"
        for cs in statuses
    ):
        return "Failed"
    return "Pending"


def _pod_created_at(pod: Any) -> str | None:
    if not pod.metadata or not pod.metadata.creation_timestamp:
        return None
    return pod.metadata.creation_timestamp.isoformat()


def _pod_ready_at(pod: Any) -> str | None:
    status_obj = getattr(pod, "status", None)
    conditions = getattr(status_obj, "conditions", None) or []
    for condition in conditions:
        if (
            getattr(condition, "type", None) == "Ready"
            and getattr(condition, "status", None) == "True"
            and getattr(condition, "last_transition_time", None)
        ):
            return condition.last_transition_time.isoformat()
    return None


def _session_id_from_pod(pod: Any) -> str:
    return pod.metadata.labels.get(
        "tank-operator/session-id", pod.metadata.name.removeprefix("session-")
    )


def _session_info_from_pod(owner: str, pod: Any) -> SessionInfo:
    return SessionInfo(
        id=_session_id_from_pod(pod),
        pod_name=pod.metadata.name,
        owner=owner,
        status=_pod_status(pod),
        mode=normalize_session_mode(
            pod.metadata.labels.get("tank-operator/mode", DEFAULT_SESSION_MODE)
        ),
        requested_at=_pod_created_at(pod),
        created_at=_pod_created_at(pod),
        ready_at=_pod_ready_at(pod),
        name=(pod.metadata.annotations or {}).get(NAME_ANNOTATION),
        test_state=_test_state_from_annotations(pod.metadata.annotations or {}),
        rollout_state=_rollout_state_from_annotations(pod.metadata.annotations or {}),
    )


def _session_info_from_record(
    owner: str, record: SessionRecord, pod: Any | None
) -> SessionInfo:
    if pod is not None:
        return SessionInfo(
            id=record.id,
            pod_name=pod.metadata.name,
            owner=owner,
            status=_pod_status(pod),
            mode=normalize_session_mode(record.mode),
            requested_at=record.requested_at
            or record.created_at
            or _pod_created_at(pod),
            created_at=record.created_at or _pod_created_at(pod),
            ready_at=_pod_ready_at(pod),
            name=record.name,
            test_state=_test_state_from_annotations(pod.metadata.annotations or {}),
            rollout_state=_rollout_state_from_annotations(
                pod.metadata.annotations or {}
            ),
        )
    return SessionInfo(
        id=record.id,
        pod_name=record.pod_name,
        owner=owner,
        status="Failed",
        mode=normalize_session_mode(record.mode),
        requested_at=record.requested_at or record.created_at,
        created_at=record.created_at,
        ready_at=None,
        name=record.name,
    )


def _legacy_deployment_owner(pod: Any) -> str | None:
    """Return the pre-pod-runtime Deployment owner for old session pods, if any."""
    for owner in pod.metadata.owner_references or []:
        if owner.kind != "ReplicaSet" or not owner.name:
            continue
        session_id = pod.metadata.labels.get("tank-operator/session-id")
        expected_prefix = f"session-{session_id}-" if session_id else "session-"
        if owner.name.startswith(expected_prefix):
            return (
                f"session-{session_id}" if session_id else owner.name.rsplit("-", 1)[0]
            )
    return None


def _pod_ready(pod: Any) -> bool:
    if not pod.status or pod.status.phase != "Running":
        return False
    statuses = pod.status.container_statuses or []
    return bool(statuses) and all(cs.ready for cs in statuses)


def _pod_has_sandbox_agent(pod: Any) -> bool:
    spec = getattr(pod, "spec", None)
    for container in getattr(spec, "containers", None) or []:
        if getattr(container, "name", None) != "claude":
            continue
        return any(
            getattr(port, "name", None) == "sandbox-agent"
            for port in (getattr(container, "ports", None) or [])
        )
    return False
