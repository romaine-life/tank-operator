"""Guarded Azure cleanup tools.

The regular Microsoft azure-mcp server is intentionally broad and mostly
read-only in this cluster. This companion server owns a tiny destructive
surface for cleanup tasks that are awkward or impossible through that image.
"""

from __future__ import annotations

import os
import time
from typing import Any

import requests
from azure.identity import DefaultAzureCredential, WorkloadIdentityCredential
from mcp.server.fastmcp import FastMCP


ARM = "https://management.azure.com"
ARM_SCOPE = "https://management.azure.com/.default"
STATIC_SITE_API_VERSION = "2024-04-01"
RESOURCE_GROUP_API_VERSION = "2024-11-01"
AKS_API_VERSION = "2024-10-01"
POLL_TIMEOUT_SECONDS = 600


def _subscription(subscription: str | None) -> str:
    sub = subscription or os.environ.get("AZURE_SUBSCRIPTION_ID")
    if not sub:
        raise ValueError("subscription is required when AZURE_SUBSCRIPTION_ID is not set")
    return sub


def _credential() -> WorkloadIdentityCredential | DefaultAzureCredential:
    client_id = os.environ.get("AZURE_CLIENT_ID")
    tenant_id = os.environ.get("AZURE_TENANT_ID")
    token_file = os.environ.get("AZURE_FEDERATED_TOKEN_FILE")
    if client_id and tenant_id and token_file:
        return WorkloadIdentityCredential(
            client_id=client_id,
            tenant_id=tenant_id,
            token_file_path=token_file,
        )
    return DefaultAzureCredential(exclude_interactive_browser_credential=True)


def _headers() -> dict[str, str]:
    token = _credential().get_token(ARM_SCOPE).token
    return {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }


def _request(
    method: str,
    path: str,
    *,
    ok: set[int],
    json: dict[str, Any] | None = None,
) -> requests.Response:
    resp = requests.request(method, f"{ARM}{path}", headers=_headers(), json=json, timeout=30)
    if resp.status_code not in ok:
        detail = resp.text.strip()
        raise RuntimeError(f"Azure ARM {method} {path} failed with {resp.status_code}: {detail}")
    return resp


def _poll(location: str, *, timeout_seconds: int = POLL_TIMEOUT_SECONDS) -> dict[str, Any]:
    deadline = time.monotonic() + timeout_seconds
    while True:
        resp = requests.get(location, headers=_headers(), timeout=30)
        if resp.status_code not in {200, 201, 202, 204}:
            raise RuntimeError(f"Azure ARM poll failed with {resp.status_code}: {resp.text.strip()}")
        if resp.status_code == 204 or not resp.text:
            return {"status": "Succeeded"}

        payload = resp.json()
        status = str(payload.get("status") or payload.get("properties", {}).get("provisioningState") or "")
        if status.lower() in {"succeeded", "failed", "canceled", "cancelled"}:
            if status.lower() != "succeeded":
                raise RuntimeError(f"Azure operation ended with {status}: {payload}")
            return payload

        if time.monotonic() >= deadline:
            raise TimeoutError(f"Azure operation did not finish within {timeout_seconds}s")
        time.sleep(5)


def _operation_url(resp: requests.Response) -> str | None:
    return resp.headers.get("Azure-AsyncOperation") or resp.headers.get("Location")


def _run_command_logs(payload: dict[str, Any]) -> str:
    properties = payload.get("properties") if isinstance(payload.get("properties"), dict) else {}
    for key in ("logs", "output", "result"):
        value = properties.get(key) or payload.get(key)
        if isinstance(value, str):
            return value
    return ""


def _require_confirmation(value: str, confirmation: str | None, label: str) -> None:
    if confirmation != value:
        raise ValueError(f"{label} confirmation must exactly equal {value!r}")


def register_tools(mcp: FastMCP) -> None:
    @mcp.tool()
    def delete_static_web_app(
        resource_group: str,
        name: str,
        confirm_name: str,
        subscription: str | None = None,
    ) -> dict[str, Any]:
        """Delete an Azure Static Web App resource by resource group and name.

        Destructive Azure cleanup tool. Use for removing disposable Static Web
        Apps when the regular Azure MCP cannot perform deletion. Destructive
        guard: confirm_name must exactly match name.
        """
        _require_confirmation(name, confirm_name, "static web app name")
        sub = _subscription(subscription)
        path = (
            f"/subscriptions/{sub}/resourceGroups/{resource_group}"
            f"/providers/Microsoft.Web/staticSites/{name}"
            f"?api-version={STATIC_SITE_API_VERSION}"
        )
        resp = _request("DELETE", path, ok={200, 202, 204})
        if resp.status_code == 202 and (operation_url := _operation_url(resp)):
            _poll(operation_url)
        return {
            "deleted": True,
            "subscription": sub,
            "resource_group": resource_group,
            "name": name,
            "type": "Microsoft.Web/staticSites",
        }

    @mcp.tool()
    def delete_resource_group(
        resource_group: str,
        confirm_resource_group: str,
        subscription: str | None = None,
    ) -> dict[str, Any]:
        """Delete an Azure resource group and every resource contained in it.

        Highly destructive Azure cleanup tool. Use only after listing/verifying
        the group is disposable. Destructive guard: confirm_resource_group must exactly match
        resource_group. Use only after verifying the group is disposable.
        """
        _require_confirmation(resource_group, confirm_resource_group, "resource group")
        sub = _subscription(subscription)
        path = (
            f"/subscriptions/{sub}/resourcegroups/{resource_group}"
            f"?api-version={RESOURCE_GROUP_API_VERSION}"
        )
        resp = _request("DELETE", path, ok={200, 202, 204})
        if resp.status_code == 202 and (operation_url := _operation_url(resp)):
            _poll(operation_url)
        return {
            "deleted": True,
            "subscription": sub,
            "resource_group": resource_group,
        }

    @mcp.tool()
    def run_aks_command(
        resource_group: str,
        cluster: str,
        command: str,
        subscription: str | None = None,
        context: str = "",
        timeout_seconds: int = POLL_TIMEOUT_SECONDS,
    ) -> dict[str, Any]:
        """Run a command against an AKS cluster through Azure Run Command.

        Use for targeted kubectl inspection or one-off migration operations
        against a cluster that is not the caller's in-cluster Kubernetes API.
        `context`, when provided, must be a base64 encoded zip file accepted by
        AKS Run Command. Requires the MCP admin identity to have Azure
        permissions for Microsoft.ContainerService/managedClusters/runCommand/action.
        """
        if not command.strip():
            raise ValueError("command is required")
        if timeout_seconds < 30 or timeout_seconds > 1800:
            raise ValueError("timeout_seconds must be between 30 and 1800")

        sub = _subscription(subscription)
        path = (
            f"/subscriptions/{sub}/resourceGroups/{resource_group}"
            f"/providers/Microsoft.ContainerService/managedClusters/{cluster}/runCommand"
            f"?api-version={AKS_API_VERSION}"
        )
        body = {
            "command": command,
            "context": context,
        }
        resp = _request("POST", path, ok={200, 201, 202}, json=body)

        payload: dict[str, Any]
        if resp.status_code == 202 and (operation_url := _operation_url(resp)):
            payload = _poll(operation_url, timeout_seconds=timeout_seconds)
        elif resp.text:
            payload = resp.json()
        else:
            payload = {"status": "Accepted"}

        return {
            "subscription": sub,
            "resource_group": resource_group,
            "cluster": cluster,
            "command": command,
            "logs": _run_command_logs(payload),
            "result": payload,
        }
