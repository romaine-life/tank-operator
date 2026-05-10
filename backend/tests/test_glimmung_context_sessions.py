import asyncio
import datetime
import json
import sys
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from tank_operator.profiles import ActiveRunStore, SessionRegistryStore
from tank_operator.sessions import GLIMMUNG_CONTEXT_ANNOTATION, SessionManager


def _pod_spec(manifest: dict) -> dict:
    return manifest["spec"]


def _claude_env(manifest: dict) -> dict[str, str]:
    containers = _pod_spec(manifest)["containers"]
    claude = next(c for c in containers if c["name"] == "claude")
    return {e["name"]: e["value"] for e in claude["env"]}


def _claude_container(manifest: dict) -> dict:
    return next(c for c in _pod_spec(manifest)["containers"] if c["name"] == "claude")


def _session_pod(
    session_id: str,
    containers: list[str],
    *,
    created_at: datetime.datetime | None = None,
    ready_at: datetime.datetime | None = None,
) -> SimpleNamespace:
    return SimpleNamespace(
        metadata=SimpleNamespace(
            name=f"session-{session_id}",
            labels={
                "app.kubernetes.io/managed-by": "tank-operator",
                "tank-operator/session-id": session_id,
                "tank-operator/mode": "subscription",
            },
            annotations={},
            creation_timestamp=created_at,
        ),
        spec=SimpleNamespace(
            containers=[SimpleNamespace(name=name) for name in containers]
        ),
        status=SimpleNamespace(
            phase="Running" if ready_at else "Pending",
            container_statuses=[],
            conditions=[
                SimpleNamespace(
                    type="Ready",
                    status="True",
                    last_transition_time=ready_at,
                )
            ]
            if ready_at
            else [],
        ),
    )


class _FakeCore:
    def __init__(self, pods: list[SimpleNamespace]) -> None:
        self._pods = pods

    async def list_namespaced_pod(self, **_kwargs: object) -> SimpleNamespace:
        return SimpleNamespace(items=self._pods)


class _ReaperSessionManager(SessionManager):
    def __init__(
        self,
        pods: list[SimpleNamespace],
        registry: SessionRegistryStore | None = None,
    ) -> None:
        super().__init__(registry=registry)
        self._core = _FakeCore(pods)
        self.deleted: list[str] = []

    async def _delete_session_runtime(self, pod: SimpleNamespace) -> None:
        self.deleted.append(pod.metadata.name)


class _MissingPodSessionManager(_ReaperSessionManager):
    async def _read_owned_pod(self, owner: str, session_id: str) -> SimpleNamespace:
        from tank_operator.sessions import SessionNotFound

        raise SessionNotFound(session_id)


def test_session_config_is_mounted_from_configmap() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription",
    )

    assert any(
        volume["name"] == "session-config"
        and volume["configMap"]["name"] == "tank-session-config"
        for volume in _pod_spec(manifest)["volumes"]
    )
    mounts = {
        (mount["mountPath"], mount["subPath"])
        for mount in _claude_container(manifest)["volumeMounts"]
        if mount["name"] == "session-config" and "subPath" in mount
    }
    assert ("/workspace/.mcp.json", "mcp.json") in mounts
    assert ("/workspace/CLAUDE.md", "default-claude.md") in mounts
    assert ("/workspace/AGENTS.md", "default-claude.md") in mounts
    assert (
        "/opt/tank/write-glimmung-context.sh",
        "write-glimmung-context.sh",
    ) in mounts
    assert ("/opt/tank/bootstrap.sh", "tank-bootstrap.sh") in mounts
    assert ("/opt/tank/headless-run.sh", "headless-run.sh") in mounts
    assert any(
        mount["mountPath"] == "/opt/tank/session-config" and "subPath" not in mount
        for mount in _claude_container(manifest)["volumeMounts"]
        if mount["name"] == "session-config"
    )

    proxy = next(
        c for c in _pod_spec(manifest)["containers"] if c["name"] == "mcp-auth-proxy"
    )
    assert any(
        mount["name"] == "session-config"
        and mount["mountPath"] == "/workspace/.mcp.json"
        for mount in proxy["volumeMounts"]
    )
    terminal_proxy = next(
        c for c in _pod_spec(manifest)["containers"] if c["name"] == "terminal-proxy"
    )
    assert any(port["name"] == "terminal" for port in terminal_proxy["ports"])
    assert any(
        volume["name"] == "terminal-proxy-config"
        for volume in _pod_spec(manifest)["volumes"]
    )


def test_idle_reaper_leaves_legacy_session_pods() -> None:
    manager = _ReaperSessionManager(
        [
            _session_pod("legacy", ["mcp-auth-proxy", "claude"]),
            _session_pod("terminald", ["mcp-auth-proxy", "terminal-proxy", "claude"]),
        ]
    )
    manager._activity = {"legacy": -10_000, "terminald": -10_000}

    asyncio.run(manager._reap_idle())

    assert manager.deleted == ["session-terminald"]
    assert "legacy" in manager._activity


def test_session_list_uses_registry_and_adopts_only_terminald_pods() -> None:
    registry = SessionRegistryStore()
    manager = _ReaperSessionManager(
        [
            _session_pod("legacy", ["mcp-auth-proxy", "claude"]),
            _session_pod("terminald", ["mcp-auth-proxy", "terminal-proxy", "claude"]),
        ],
        registry=registry,
    )

    listed = asyncio.run(manager.list(owner="operator@example.test"))

    assert [session.id for session in listed] == ["terminald"]
    assert asyncio.run(registry.get("operator@example.test", "legacy")) is None
    assert asyncio.run(registry.get("operator@example.test", "terminald")) is not None


def test_session_registry_scope_isolates_environments() -> None:
    prod = SessionRegistryStore(scope="default")
    slot = SessionRegistryStore(scope="tank-slot-1")

    asyncio.run(
        prod.upsert(
            email="operator@example.test",
            session_id="shared-id",
            mode="subscription",
            pod_name="session-prod",
        )
    )
    asyncio.run(
        slot.upsert(
            email="operator@example.test",
            session_id="shared-id",
            mode="claude_gui",
            pod_name="session-slot",
        )
    )

    prod_list = asyncio.run(prod.list("operator@example.test"))
    slot_list = asyncio.run(slot.list("operator@example.test"))

    assert [session.pod_name for session in prod_list] == ["session-prod"]
    assert [session.pod_name for session in slot_list] == ["session-slot"]
    assert asyncio.run(prod.get("operator@example.test", "shared-id")).pod_name == "session-prod"
    assert asyncio.run(slot.get("operator@example.test", "shared-id")).pod_name == "session-slot"


def test_active_run_store_tracks_edge_status_without_heartbeats() -> None:
    store = ActiveRunStore()

    started = asyncio.run(
        store.start(
            email="Operator@Example.Test",
            session_id="abc123",
            run_id="run-1",
            pod_name="session-abc123",
            provider="claude",
            stream_path="/tmp/tank-run-run-1.stream",
            pid_path="/tmp/tank-run-run-1.pid",
        )
    )
    active = asyncio.run(store.get_active("abc123"))

    assert started.email == "operator@example.test"
    assert active is not None
    assert active.run_id == "run-1"
    assert active.status == "running"

    asyncio.run(store.mark_stale("abc123", "run-1"))

    assert asyncio.run(store.get_active("abc123")) is None


def test_session_list_reports_request_creation_and_ready_timestamps() -> None:
    requested_at = "2026-05-04T20:00:00+00:00"
    created_at = datetime.datetime.fromisoformat("2026-05-04T20:00:03+00:00")
    ready_at = datetime.datetime.fromisoformat("2026-05-04T20:00:41+00:00")
    registry = SessionRegistryStore()
    asyncio.run(
        registry.upsert(
            email="operator@example.test",
            session_id="timed",
            mode="subscription",
            pod_name="session-timed",
            requested_at=requested_at,
            created_at=created_at.isoformat(),
        )
    )
    manager = _ReaperSessionManager(
        [
            _session_pod(
                "timed",
                ["mcp-auth-proxy", "terminal-proxy", "claude"],
                created_at=created_at,
                ready_at=ready_at,
            )
        ],
        registry=registry,
    )

    listed = asyncio.run(manager.list(owner="operator@example.test"))

    assert len(listed) == 1
    assert listed[0].requested_at == requested_at
    assert listed[0].created_at == created_at.isoformat()
    assert listed[0].ready_at == ready_at.isoformat()


def test_delete_hides_registry_session_when_runtime_pod_is_gone() -> None:
    registry = SessionRegistryStore()
    asyncio.run(
        registry.upsert(
            email="operator@example.test",
            session_id="missing",
            mode="subscription",
            pod_name="session-missing",
        )
    )
    manager = _MissingPodSessionManager([], registry=registry)

    asyncio.run(manager.delete(owner="operator@example.test", session_id="missing"))

    assert asyncio.run(registry.list("operator@example.test")) == []


def test_glimmung_context_is_stamped_on_session_pod() -> None:
    context = {
        "glimmung_run_ref": "run-1",
        "glimmung_issue_ref": "issue-1",
        "glimmung_touchpoint_ref": "pr-1",
        "validation_url": "https://preview.example.test",
        "caller_email": "operator@example.test",
    }

    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription",
        glimmung_context=context,
    )

    annotations = manifest["metadata"]["annotations"]
    assert json.loads(annotations[GLIMMUNG_CONTEXT_ANNOTATION]) == context

    env = _claude_env(manifest)
    assert json.loads(env["TANK_GLIMMUNG_CONTEXT_JSON"]) == context
    assert env["TANK_GLIMMUNG_RUN_REF"] == "run-1"
    assert env["TANK_GLIMMUNG_ISSUE_REF"] == "issue-1"
    assert env["TANK_GLIMMUNG_TOUCHPOINT_REF"] == "pr-1"
    assert env["TANK_GLIMMUNG_VALIDATION_URL"] == "https://preview.example.test"
    assert _claude_container(manifest)["command"] == [
        "bash",
        "-lc",
        "if command -v sandbox-agent >/dev/null 2>&1; then sandbox_agent_cmd=sandbox-agent; else sandbox_agent_cmd='npx -y @sandbox-agent/cli@0.4.2'; fi; $sandbox_agent_cmd server --host 0.0.0.0 --port 2468 --no-token --no-telemetry >/tmp/sandbox-agent.log 2>&1 & exec tank-terminald",
    ]
    ports = _claude_container(manifest)["ports"]
    assert {"name": "sandbox-agent", "containerPort": 2468} in ports


def test_plain_session_has_no_glimmung_context_annotation() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription",
    )

    assert GLIMMUNG_CONTEXT_ANNOTATION not in manifest["metadata"]["annotations"]
    assert _claude_env(manifest)["TANK_GLIMMUNG_CONTEXT_JSON"] == ""


def test_pi_cli_uses_pi_image_and_mounts_codex_credentials() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_cli",
    )

    assert _claude_container(manifest)["image"].endswith("/pi-container:latest")
    assert all(
        mount["name"] != "pi-creds"
        for mount in _claude_container(manifest).get("volumeMounts", [])
    )
    assert any(
        mount["name"] == "codex-creds" and mount["mountPath"] == "/etc/codex-creds"
        for mount in _claude_container(manifest).get("volumeMounts", [])
    )
    assert any(
        volume["name"] == "codex-creds"
        and volume["secret"]["secretName"] == "codex-credentials"
        for volume in _pod_spec(manifest).get("volumes", [])
    )


def test_headless_codex_uses_codex_image_and_mounts_credentials() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="codex_gui",
    )

    assert _claude_container(manifest)["image"].endswith("/codex-container:latest")
    assert any(
        mount["name"] == "codex-creds" and mount["mountPath"] == "/etc/codex-creds"
        for mount in _claude_container(manifest).get("volumeMounts", [])
    )
    assert any(
        volume["name"] == "codex-creds"
        and volume["secret"]["secretName"] == "codex-credentials"
        for volume in _pod_spec(manifest).get("volumes", [])
    )


def test_headless_claude_keeps_anthropic_proxy_plumbing() -> None:
    manager = SessionManager()
    manager._oauth_gateway_ip = "10.0.0.10"
    manager._api_proxy_ip = "10.0.0.20"

    manifest = manager._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="claude_gui",
    )

    assert _pod_spec(manifest)["hostAliases"] == [
        {"ip": "10.0.0.10", "hostnames": ["platform.claude.com"]},
        {"ip": "10.0.0.20", "hostnames": ["api.anthropic.com"]},
    ]
    mount_names = {
        mount["name"] for mount in _claude_container(manifest)["volumeMounts"]
    }
    assert "oauth-gateway-ca" in mount_names


def test_pi_cli_only_hijacks_anthropic_api_proxy() -> None:
    manager = SessionManager()
    manager._oauth_gateway_ip = "10.0.0.10"
    manager._api_proxy_ip = "10.0.0.20"

    manifest = manager._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_cli",
    )

    assert _pod_spec(manifest)["hostAliases"] == [
        {"ip": "10.0.0.20", "hostnames": ["api.anthropic.com"]}
    ]
    mount_names = {
        mount["name"] for mount in _claude_container(manifest)["volumeMounts"]
    }
    assert "session-config" in mount_names
    assert "oauth-gateway-ca" in mount_names


def test_pi_config_uses_pi_image_without_credential_mount() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_config",
    )

    assert _claude_container(manifest)["image"].endswith("/pi-container:latest")
    assert all(
        mount["name"] != "pi-creds"
        for mount in _claude_container(manifest).get("volumeMounts", [])
    )
