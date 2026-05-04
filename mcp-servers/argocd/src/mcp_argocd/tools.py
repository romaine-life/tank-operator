"""Read-only ArgoCD REST tools.

Defense in depth: tool surface is constrained to GET endpoints + the
`/sync` action. argocd-rbac-cm grants the MCP's SA exactly those verbs
(applications get/sync, projects get, repositories get, clusters get) — so
even if a tool wrapper were bypassed, the bearer can't write anything else.
"""

from __future__ import annotations

import os
from typing import Any

import httpx
from mcp.server.fastmcp import FastMCP

from .dex import ARGOCD_SERVER_URL, get_bearer


_TIMEOUT_SECONDS = 30


def _clamp_limit(limit: int | None, *, default: int, maximum: int = 500) -> int:
    if limit is None:
        return default
    return max(1, min(int(limit), maximum))


def _client() -> httpx.Client:
    return httpx.Client(
        base_url=ARGOCD_SERVER_URL,
        headers={"Authorization": f"Bearer {get_bearer()}"},
        timeout=_TIMEOUT_SECONDS,
    )


def _get(path: str, params: dict[str, Any] | None = None) -> Any:
    with _client() as c:
        resp = c.get(path, params=params)
    if resp.status_code != 200:
        raise RuntimeError(f"ArgoCD GET {path} -> {resp.status_code} {resp.text}")
    return resp.json()


def _post(path: str, json_body: dict[str, Any] | None = None) -> Any:
    with _client() as c:
        resp = c.post(path, json=json_body)
    if resp.status_code not in (200, 201):
        raise RuntimeError(f"ArgoCD POST {path} -> {resp.status_code} {resp.text}")
    return resp.json() if resp.text else {}


def register_tools(mcp: FastMCP) -> None:
    @mcp.tool()
    def list_applications(
        project: str | None = None,
        selector: str | None = None,
        name_contains: str | None = None,
        health_status: str | None = None,
        sync_status: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List ArgoCD Applications with sync status, health status, source, and revision.

        Use to find an app before checking resource trees, diffs, events, or
        triggering sync. `project` filters by AppProject;
        `selector` is a label selector ('app=foo,role=bar'). `name_contains`,
        `health_status`, `sync_status`, and `limit` further narrow output."""
        params: dict[str, Any] = {}
        if project:
            params["projects"] = project
        if selector:
            params["selector"] = selector
        body = _get("/api/v1/applications", params=params)
        out = []
        needle = name_contains.lower() if name_contains else None
        cap = _clamp_limit(limit, default=100)
        for app in body.get("items") or []:
            md = app.get("metadata", {})
            sp = app.get("spec", {})
            st = app.get("status", {})
            name = md.get("name")
            app_sync_status = st.get("sync", {}).get("status")
            app_health_status = st.get("health", {}).get("status")
            if needle and (not name or needle not in name.lower()):
                continue
            if health_status and app_health_status != health_status:
                continue
            if sync_status and app_sync_status != sync_status:
                continue
            out.append(
                {
                    "name": name,
                    "namespace": md.get("namespace"),
                    "project": sp.get("project"),
                    "destination": sp.get("destination"),
                    "source": sp.get("source"),
                    "syncStatus": app_sync_status,
                    "healthStatus": app_health_status,
                    "revision": st.get("sync", {}).get("revision"),
                }
            )
            if len(out) >= cap:
                break
        return out

    @mcp.tool()
    def get_application(name: str) -> dict[str, Any]:
        """Get one ArgoCD Application object including spec, status, health, sync, and operationState.

        Return the full Application object including spec + status +
        operationState. Use this when list_applications doesn't have the
        detail you need (resource tree, sync result, conditions)."""
        return _get(f"/api/v1/applications/{name}")

    @mcp.tool()
    def get_application_resource_tree(name: str) -> dict[str, Any]:
        """Get the ArgoCD live Kubernetes resource tree for an Application.

        Return every
        K8s object ArgoCD is tracking, with health + sync per node. Useful
        for diagnosing why an app is Degraded without pulling each
        resource by hand."""
        return _get(f"/api/v1/applications/{name}/resource-tree")

    @mcp.tool()
    def get_application_managed_resources(name: str) -> dict[str, Any]:
        """Get ArgoCD managed resources and live-vs-target diffs for an Application.

        This is
        what the UI's "App Diff" view uses."""
        return _get(f"/api/v1/applications/{name}/managed-resources")

    @mcp.tool()
    def get_application_events(name: str) -> dict[str, Any]:
        """Get ArgoCD Application events for sync operations, health changes, and hooks.

        Return events ArgoCD has recorded for the Application — sync
        operations, health transitions, hook execution."""
        return _get(f"/api/v1/applications/{name}/events")

    @mcp.tool()
    def sync_application(
        name: str,
        revision: str | None = None,
        prune: bool = False,
        dry_run: bool = False,
    ) -> dict[str, Any]:
        """Sync an ArgoCD Application to its target revision, optionally dry-run or prune.

        Trigger an ArgoCD sync. revision defaults to the Application's
        configured targetRevision. dry_run=True returns the diff without
        applying. prune=True deletes resources removed from git — leave
        False unless you specifically want a destructive sync."""
        body: dict[str, Any] = {"prune": prune, "dryRun": dry_run}
        if revision:
            body["revision"] = revision
        return _post(f"/api/v1/applications/{name}/sync", json_body=body)

    @mcp.tool()
    def list_projects(name_contains: str | None = None, limit: int | None = 100) -> list[dict[str, Any]]:
        """List ArgoCD AppProjects with source repository and destination permissions.

        `name_contains` filters project names client-side and `limit` caps
        returned rows.
        """
        body = _get("/api/v1/projects")
        rows: list[dict[str, Any]] = []
        needle = name_contains.lower() if name_contains else None
        cap = _clamp_limit(limit, default=100)
        for p in body.get("items") or []:
            name = p.get("metadata", {}).get("name")
            if needle and (not name or needle not in name.lower()):
                continue
            rows.append(
                {
                    "name": name,
                    "description": p.get("spec", {}).get("description"),
                    "sourceRepos": p.get("spec", {}).get("sourceRepos"),
                    "destinations": p.get("spec", {}).get("destinations"),
                }
            )
            if len(rows) >= cap:
                break
        return rows

    @mcp.tool()
    def list_repositories(
        repo_contains: str | None = None,
        name_contains: str | None = None,
        type: str | None = None,
        connection_status: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List Git repositories and Helm repositories configured in ArgoCD, optionally filtered.

        Connection state included so you
        can spot a repo whose creds rotted. `repo_contains`, `name_contains`,
        `type`, `connection_status`, and `limit` narrow large installations."""
        body = _get("/api/v1/repositories")
        rows: list[dict[str, Any]] = []
        repo_needle = repo_contains.lower() if repo_contains else None
        name_needle = name_contains.lower() if name_contains else None
        cap = _clamp_limit(limit, default=100)
        for r in body.get("items") or []:
            repo = r.get("repo")
            name = r.get("name")
            status = r.get("connectionState", {}).get("status")
            if repo_needle and (not repo or repo_needle not in repo.lower()):
                continue
            if name_needle and (not name or name_needle not in name.lower()):
                continue
            if type and r.get("type") != type:
                continue
            if connection_status and status != connection_status:
                continue
            rows.append(
                {
                    "repo": repo,
                    "type": r.get("type"),
                    "name": name,
                    "connectionState": status,
                    "connectionMessage": r.get("connectionState", {}).get("message"),
                }
            )
            if len(rows) >= cap:
                break
        return rows

    @mcp.tool()
    def list_clusters(
        name_contains: str | None = None,
        server_contains: str | None = None,
        connection_status: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List Kubernetes clusters registered in ArgoCD, optionally filtered.

        In-cluster (kubernetes.default.svc)
        is always present; remote clusters appear here once registered.
        `name_contains`, `server_contains`, `connection_status`, and `limit`
        narrow large cluster lists."""
        body = _get("/api/v1/clusters")
        rows: list[dict[str, Any]] = []
        name_needle = name_contains.lower() if name_contains else None
        server_needle = server_contains.lower() if server_contains else None
        cap = _clamp_limit(limit, default=100)
        for c in body.get("items") or []:
            name = c.get("name")
            server = c.get("server")
            status = c.get("connectionState", {}).get("status")
            if name_needle and (not name or name_needle not in name.lower()):
                continue
            if server_needle and (not server or server_needle not in server.lower()):
                continue
            if connection_status and status != connection_status:
                continue
            rows.append(
                {
                    "name": name,
                    "server": server,
                    "connectionState": status,
                    "serverVersion": c.get("serverVersion"),
                }
            )
            if len(rows) >= cap:
                break
        return rows

    @mcp.tool()
    def server_version() -> dict[str, Any]:
        """Get ArgoCD server version information.

        Handy when comparing API
        behaviour across upgrades."""
        return _get("/api/version")
