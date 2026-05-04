"""kubectl + helm tools.

Defense in depth: every command is a hard-coded subcommand (get, describe,
logs, top, helm get/list/status/history). The pod's SA gets read-mostly
RBAC — these wrappers keep the tool surface aligned so the agent can't
accidentally request something the SA isn't permitted to do.

Two intentional write verbs:
  - delete_pod — useful when a controller is wedged but its parent
    StatefulSet/Deployment is fine. Pod deletion is recoverable: the
    parent recreates it.
  - rollout_restart — patches a workload's pod-template annotation to
    trigger a rolling restart. Same semantics as
    `kubectl rollout restart`.

Both are paired with explicit RBAC additions in
infra-bootstrap/k8s-mcp-k8s/templates/cluster-reader.yaml.
"""

from __future__ import annotations

import json
import subprocess
from typing import Any

from mcp.server.fastmcp import FastMCP


_TIMEOUT_SECONDS = 30


def _clamp_limit(limit: int | None, *, default: int | None = None, maximum: int = 500) -> int | None:
    if limit is None:
        return default
    return max(1, min(int(limit), maximum))


def _run(cmd: list[str], *, parse_json: bool = False) -> Any:
    """Run a binary, return stdout. Surfaces stderr to the caller on failure."""
    try:
        proc = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=_TIMEOUT_SECONDS,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError(f"command timed out after {_TIMEOUT_SECONDS}s: {' '.join(cmd)}") from exc

    if proc.returncode != 0:
        # stderr usually carries the useful diagnostic; stdout is rarely set
        # on failure. Strip both so the agent sees a clean message.
        msg = (proc.stderr or proc.stdout or "").strip()
        raise RuntimeError(f"{cmd[0]} exited {proc.returncode}: {msg}")

    if parse_json:
        return json.loads(proc.stdout)
    return proc.stdout


def register_tools(mcp: FastMCP) -> None:
    @mcp.tool()
    def list_namespaces(
        label_selector: str | None = None,
        field_selector: str | None = None,
        name_contains: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List Kubernetes namespaces in the cluster, optionally filtered.

        Use to discover namespace names before listing pods, services,
        deployments, events, Helm releases, or other namespaced resources.
        `label_selector` and `field_selector` map to kubectl selectors,
        `name_contains` filters client-side, and `limit` caps returned rows.
        """
        cmd = ["kubectl", "get", "namespaces", "-o", "json"]
        if label_selector:
            cmd += ["-l", label_selector]
        if field_selector:
            cmd += ["--field-selector", field_selector]
        body = _run(cmd, parse_json=True)
        needle = name_contains.lower() if name_contains else None
        cap = _clamp_limit(limit, default=100)
        rows: list[dict[str, Any]] = []
        for item in body.get("items", []):
            name = item["metadata"]["name"]
            if needle and needle not in name.lower():
                continue
            rows.append(
                {
                    "name": name,
                    "phase": item.get("status", {}).get("phase"),
                    "created": item["metadata"].get("creationTimestamp"),
                }
            )
            if cap is not None and len(rows) >= cap:
                break
        return rows

    @mcp.tool()
    def list_resources(
        kind: str,
        namespace: str | None = None,
        all_namespaces: bool = False,
        label_selector: str | None = None,
        field_selector: str | None = None,
        name_contains: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List Kubernetes resources by kind, namespace, and optional label selector.

        Use as `kubectl get` for pods, deployments, services, jobs, CRDs, nodes,
        namespaces, and custom resources. Cluster-scoped kinds (Node, Namespace,
        ClusterRole, etc.) ignore namespace. Use all_namespaces=True for namespaced
        kinds across the cluster. label_selector is a standard '-l' string
        ('app=foo,role=bar'). `field_selector`, `name_contains`, and `limit`
        further narrow large result sets."""
        cmd = ["kubectl", "get", kind, "-o", "json"]
        if all_namespaces:
            cmd.append("--all-namespaces")
        elif namespace:
            cmd += ["-n", namespace]
        if label_selector:
            cmd += ["-l", label_selector]
        if field_selector:
            cmd += ["--field-selector", field_selector]
        body = _run(cmd, parse_json=True)
        out: list[dict[str, Any]] = []
        needle = name_contains.lower() if name_contains else None
        cap = _clamp_limit(limit, default=100)
        for item in body.get("items", []):
            md = item.get("metadata", {})
            name = md.get("name")
            if needle and (not name or needle not in name.lower()):
                continue
            out.append(
                {
                    "name": name,
                    "namespace": md.get("namespace"),
                    "kind": item.get("kind"),
                    "apiVersion": item.get("apiVersion"),
                    "labels": md.get("labels") or {},
                    "created": md.get("creationTimestamp"),
                }
            )
            if cap is not None and len(out) >= cap:
                break
        return out

    @mcp.tool()
    def get_resource(kind: str, name: str, namespace: str | None = None) -> dict[str, Any]:
        """Get one Kubernetes resource as full JSON.

        Use as `kubectl get KIND NAME -o json` when you need spec, status,
        labels, annotations, owner references, or controller fields.
        """
        cmd = ["kubectl", "get", kind, name, "-o", "json"]
        if namespace:
            cmd += ["-n", namespace]
        return _run(cmd, parse_json=True)

    @mcp.tool()
    def describe_resource(kind: str, name: str, namespace: str | None = None) -> str:
        """Describe one Kubernetes resource with `kubectl describe`.

        Useful when you want events + computed fields
        rather than the raw spec — e.g. to see why a pod is Pending."""
        cmd = ["kubectl", "describe", kind, name]
        if namespace:
            cmd += ["-n", namespace]
        return _run(cmd)

    @mcp.tool()
    def get_pod_logs(
        name: str,
        namespace: str,
        container: str | None = None,
        tail_lines: int = 200,
        previous: bool = False,
    ) -> str:
        """Read Kubernetes pod logs from a container.

        Use as `kubectl logs` for debugging running, failed, or crash-looping
        pods. previous=True reads the previous container instance
        (useful when the current one is in CrashLoopBackOff). tail_lines caps
        output to keep responses tractable."""
        cmd = [
            "kubectl",
            "logs",
            name,
            "-n",
            namespace,
            f"--tail={tail_lines}",
        ]
        if container:
            cmd += ["-c", container]
        if previous:
            cmd.append("-p")
        return _run(cmd)

    @mcp.tool()
    def list_events(
        namespace: str | None = None,
        all_namespaces: bool = False,
        field_selector: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List recent Kubernetes events, optionally filtered by namespace or involved object.

        Use to diagnose Pending pods, failed scheduling, image pull errors,
        failed mounts, unhealthy controllers, or rollout problems.
        `field_selector` example: `involvedObject.name=session-foo`.
        `limit` caps returned rows after timestamp sorting.
        """
        cmd = ["kubectl", "get", "events", "-o", "json", "--sort-by=.lastTimestamp"]
        if all_namespaces:
            cmd.append("--all-namespaces")
        elif namespace:
            cmd += ["-n", namespace]
        if field_selector:
            cmd += ["--field-selector", field_selector]
        body = _run(cmd, parse_json=True)
        cap = _clamp_limit(limit, default=100)
        return [
            {
                "namespace": e.get("metadata", {}).get("namespace"),
                "type": e.get("type"),
                "reason": e.get("reason"),
                "message": e.get("message"),
                "involved": {
                    "kind": e.get("involvedObject", {}).get("kind"),
                    "name": e.get("involvedObject", {}).get("name"),
                },
                "count": e.get("count"),
                "lastTimestamp": e.get("lastTimestamp"),
            }
            for e in body.get("items", [])[:cap]
        ]

    @mcp.tool()
    def top_pods(
        namespace: str | None = None,
        all_namespaces: bool = False,
        label_selector: str | None = None,
        sort_by: str | None = None,
        limit: int | None = 50,
    ) -> str:
        """Show Kubernetes pod CPU and memory usage with `kubectl top pods`.

        Use to investigate resource pressure, evictions, high memory, or hot
        workloads. Requires metrics-server. `label_selector` narrows pods,
        `sort_by` is cpu or memory, and `limit` caps output rows.
        """
        cmd = ["kubectl", "top", "pods"]
        if all_namespaces:
            cmd.append("--all-namespaces")
        elif namespace:
            cmd += ["-n", namespace]
        if label_selector:
            cmd += ["-l", label_selector]
        if sort_by:
            cmd += [f"--sort-by={sort_by}"]
        out = _run(cmd)
        cap = _clamp_limit(limit, default=50)
        if cap is None:
            return out
        lines = out.splitlines()
        if len(lines) <= cap + 1:
            return out
        return "\n".join(lines[: cap + 1]) + "\n"

    @mcp.tool()
    def top_nodes() -> str:
        """Show Kubernetes node CPU and memory usage with `kubectl top nodes`.

        Use to investigate cluster capacity, node pressure, and scheduling
        issues. Requires metrics-server.
        """
        return _run(["kubectl", "top", "nodes"])

    @mcp.tool()
    def helm_list(
        namespace: str | None = None,
        all_namespaces: bool = True,
        filter: str | None = None,
        selector: str | None = None,
        status: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List Helm releases in one namespace or all namespaces.

        Use to discover release names before helm_status, helm_get_values,
        helm_get_manifest, or helm_history. Defaults to all namespaces — narrow with namespace
        when looking for one specific release. `filter` is Helm's release-name
        regex, `selector` filters labels where supported, `status` narrows
        release state, and `limit` caps returned rows."""
        cmd = ["helm", "list", "-o", "json"]
        if all_namespaces and not namespace:
            cmd.append("-A")
        elif namespace:
            cmd += ["-n", namespace]
        if filter:
            cmd += ["--filter", filter]
        if selector:
            cmd += ["--selector", selector]
        if status:
            cmd.append(f"--{status}")
        rows = _run(cmd, parse_json=True)
        cap = _clamp_limit(limit, default=100)
        return rows if cap is None else rows[:cap]

    @mcp.tool()
    def helm_get_values(release: str, namespace: str, all_values: bool = True) -> dict[str, Any]:
        """Get Helm release values for a release in a namespace.

        Use to inspect chart configuration and overrides. all_values=True merges chart defaults with
        user overrides; False returns only the user overrides."""
        cmd = ["helm", "get", "values", release, "-n", namespace, "-o", "json"]
        if all_values:
            cmd.append("-a")
        return _run(cmd, parse_json=True) or {}

    @mcp.tool()
    def helm_get_manifest(release: str, namespace: str) -> str:
        """Get rendered Kubernetes manifest YAML for a Helm release.

        Use to inspect what Helm deployed when debugging chart rendering,
        selectors, env vars, image tags, RBAC, or hooks. Big — use sparingly.
        """
        return _run(["helm", "get", "manifest", release, "-n", namespace])

    @mcp.tool()
    def helm_status(release: str, namespace: str) -> dict[str, Any]:
        """Get Helm release status including revision, deployed time, and last action."""
        return _run(
            ["helm", "status", release, "-n", namespace, "-o", "json"],
            parse_json=True,
        )

    @mcp.tool()
    def helm_history(release: str, namespace: str) -> list[dict[str, Any]]:
        """Get Helm release revision history for rollback and deployment timeline analysis."""
        return _run(
            ["helm", "history", release, "-n", namespace, "-o", "json"],
            parse_json=True,
        )

    @mcp.tool()
    def delete_pod(name: str, namespace: str, grace_period_seconds: int | None = None) -> str:
        """Delete a Kubernetes Pod so its controller can recreate it.

        Destructive but usually recoverable. Useful when a controller pod is wedged but its parent
        StatefulSet/Deployment/DaemonSet is healthy — the parent will recreate
        the pod. grace_period_seconds=0 forces immediate delete (skips
        terminationGracePeriod)."""
        cmd = ["kubectl", "delete", "pod", name, "-n", namespace]
        if grace_period_seconds is not None:
            cmd += [f"--grace-period={int(grace_period_seconds)}"]
        return _run(cmd)

    @mcp.tool()
    def rollout_restart(kind: str, name: str, namespace: str) -> str:
        """Restart a Kubernetes workload by triggering a rolling restart.

        Use for Deployment, StatefulSet, or DaemonSet restarts.
        Equivalent to `kubectl rollout restart`: patches the pod template's
        `kubectl.kubernetes.io/restartedAt` annotation so the controller
        schedules new pods. kind must be one of: deployment, statefulset,
        daemonset."""
        allowed = {"deployment", "statefulset", "daemonset"}
        canonical = kind.lower().rstrip("s")
        if canonical not in allowed:
            raise ValueError(f"kind must be one of {sorted(allowed)}, got {kind!r}")
        return _run(["kubectl", "rollout", "restart", canonical, name, "-n", namespace])

    @mcp.tool()
    def api_resources(
        api_group: str | None = None,
        api_version: str | None = None,
        kind_contains: str | None = None,
        name_contains: str | None = None,
        namespaced: bool | None = None,
        verb: str | None = None,
        limit: int | None = 100,
    ) -> list[dict[str, Any]]:
        """List Kubernetes API resources and CRDs known to the cluster, optionally filtered.

        Use to discover available kinds, short names, namespaced scope, and verbs — useful for discovering CRDs
        like applications.argoproj.io or httproutes.gateway.networking.k8s.io.
        `api_group`, `api_version`, kind/name contains filters, `namespaced`,
        `verb`, and `limit` keep discovery responses small."""
        # `kubectl api-resources` doesn't have a -o json mode; parse the
        # default columnar output. Skip the header row.
        out = _run(
            [
                "kubectl",
                "api-resources",
                "--no-headers",
                "-o",
                "wide",
            ]
        )
        rows: list[dict[str, Any]] = []
        for line in out.splitlines():
            parts = line.split()
            if len(parts) < 5:
                continue
            # Layout: NAME [SHORTNAMES] APIVERSION NAMESPACED KIND VERBS...
            # SHORTNAMES is optional; detect by checking column widths via
            # the trailing fields, which are always [namespaced, kind, verbs...].
            name = parts[0]
            namespaced = parts[-3].lower() == "true"
            kind = parts[-2]
            verbs_field = parts[-1]
            api_version = parts[-4]
            shortnames = parts[1:-4] if len(parts) > 5 else []
            rows.append(
                {
                    "name": name,
                    "shortnames": shortnames,
                    "apiVersion": api_version,
                    "namespaced": namespaced,
                    "kind": kind,
                    "verbs": verbs_field.split(","),
                }
            )
        kind_needle = kind_contains.lower() if kind_contains else None
        name_needle = name_contains.lower() if name_contains else None
        cap = _clamp_limit(limit, default=100)
        filtered: list[dict[str, Any]] = []
        for row in rows:
            version = row["apiVersion"]
            group = version.split("/", 1)[0] if "/" in version else ""
            if api_group is not None and group != api_group:
                continue
            if api_version is not None and version != api_version:
                continue
            if kind_needle and kind_needle not in row["kind"].lower():
                continue
            if name_needle and name_needle not in row["name"].lower():
                continue
            if namespaced is not None and row["namespaced"] is not namespaced:
                continue
            if verb and verb not in row["verbs"]:
                continue
            filtered.append(row)
            if cap is not None and len(filtered) >= cap:
                break
        return filtered
