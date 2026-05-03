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


def test_pi_subscription_uses_pi_image_and_mounts_credentials() -> None:
    manifest = SessionManager()._deployment_manifest(
        "abc123",
        owner="operator@example.test",
        mode="pi_subscription",
    )

    assert _claude_container(manifest)["image"].endswith("/pi-container:latest")
    assert any(
        mount["name"] == "pi-creds" and mount["mountPath"] == "/etc/pi-creds"
        for mount in _claude_container(manifest).get("volumeMounts", [])
    )
    assert any(
        volume["name"] == "pi-creds"
        and volume["secret"]["secretName"] == "pi-credentials"
        for volume in _pod_spec(manifest).get("volumes", [])
    )


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
