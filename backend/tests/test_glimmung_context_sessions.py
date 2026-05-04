import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from tank_operator.sessions import GLIMMUNG_CONTEXT_ANNOTATION, SessionManager


def _pod_spec(manifest: dict) -> dict:
    return manifest["spec"]["template"]["spec"]


def _claude_env(manifest: dict) -> dict[str, str]:
    containers = _pod_spec(manifest)["containers"]
    claude = next(c for c in containers if c["name"] == "claude")
    return {e["name"]: e["value"] for e in claude["env"]}


def _claude_container(manifest: dict) -> dict:
    return next(c for c in _pod_spec(manifest)["containers"] if c["name"] == "claude")


def test_session_config_is_mounted_from_configmap() -> None:
    manifest = SessionManager()._deployment_manifest(
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
        if mount["name"] == "session-config"
    }
    assert ("/workspace/.mcp.json", "mcp.json") in mounts
    assert ("/workspace/CLAUDE.md", "default-claude.md") in mounts
    assert ("/workspace/AGENTS.md", "default-claude.md") in mounts
    assert ("/opt/tank/bootstrap.sh", "tank-bootstrap.sh") in mounts
    assert ("/home/node/.claude/skills/done/SKILL.md", "skills.done.SKILL.md") in mounts
    assert ("/home/node/.codex/skills/rollout/SKILL.md", "skills.rollout.SKILL.md") in mounts

    proxy = next(c for c in _pod_spec(manifest)["containers"] if c["name"] == "mcp-auth-proxy")
    assert any(
        mount["name"] == "session-config" and mount["mountPath"] == "/workspace/.mcp.json"
        for mount in proxy["volumeMounts"]
    )


def test_glimmung_context_is_stamped_on_session_deployment() -> None:
    context = {
        "glimmung_run_id": "run-1",
        "glimmung_issue_id": "issue-1",
        "glimmung_pr_id": "pr-1",
        "validation_url": "https://preview.example.test",
        "caller_email": "operator@example.test",
    }

    manifest = SessionManager()._deployment_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription",
        glimmung_context=context,
    )

    annotations = manifest["metadata"]["annotations"]
    assert json.loads(annotations[GLIMMUNG_CONTEXT_ANNOTATION]) == context

    env = _claude_env(manifest)
    assert json.loads(env["TANK_GLIMMUNG_CONTEXT_JSON"]) == context
    assert env["TANK_GLIMMUNG_RUN_ID"] == "run-1"
    assert env["TANK_GLIMMUNG_ISSUE_ID"] == "issue-1"
    assert env["TANK_GLIMMUNG_PR_ID"] == "pr-1"
    assert env["TANK_GLIMMUNG_VALIDATION_URL"] == "https://preview.example.test"


def test_plain_session_has_no_glimmung_context_annotation() -> None:
    manifest = SessionManager()._deployment_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription",
    )

    assert GLIMMUNG_CONTEXT_ANNOTATION not in manifest["metadata"]["annotations"]
    assert _claude_env(manifest)["TANK_GLIMMUNG_CONTEXT_JSON"] == ""


def test_pi_subscription_uses_pi_image_and_mounts_codex_credentials() -> None:
    manifest = SessionManager()._deployment_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_subscription",
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


def test_pi_subscription_only_hijacks_anthropic_api_proxy() -> None:
    manager = SessionManager()
    manager._oauth_gateway_ip = "10.0.0.10"
    manager._api_proxy_ip = "10.0.0.20"

    manifest = manager._deployment_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_subscription",
    )

    assert _pod_spec(manifest)["hostAliases"] == [
        {"ip": "10.0.0.20", "hostnames": ["api.anthropic.com"]}
    ]
    mount_names = {mount["name"] for mount in _claude_container(manifest)["volumeMounts"]}
    assert "session-config" in mount_names
    assert "oauth-gateway-ca" in mount_names


def test_pi_config_uses_pi_image_without_credential_mount() -> None:
    manifest = SessionManager()._deployment_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_config",
    )

    assert _claude_container(manifest)["image"].endswith("/pi-container:latest")
    assert all(
        mount["name"] != "pi-creds"
        for mount in _claude_container(manifest).get("volumeMounts", [])
    )
