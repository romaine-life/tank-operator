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
    def list_namespaces() -> list[dict[str, Any]]:
        """List Kubernetes namespaces in the cluster.

        Use to discover namespace names before listing pods, services,
        deployments, events, Helm releases, or other namespaced resources.
        """
        body = _run(["kubectl", "get", "namespaces", "-o", "json"], parse_json=True)
        return [
            {
                "name": item["metadata"]["name"],
                "phase": item.get("status", {}).get("phase"),
                "created": item["metadata"].get("creationTimestamp"),
            }
            for item in body.get("items", [])
        ]

    @mcp.tool()
    def list_resources(
        kind: str,
        namespace: str | None = None,
        all_namespaces: bool = False,
        label_selector: str | None = None,
    ) -> list[dict[str, Any]]:
        """List Kubernetes resources by kind, namespace, and optional label selector.

        Use as `kubectl get` for pods, deployments, services, jobs, CRDs, nodes,
        namespaces, and custom resources. Cluster-scoped kinds (Node, Namespace,
        ClusterRole, etc.) ignore namespace. Use all_namespaces=True for namespaced
        kinds across the cluster. label_selector is a standard '-l' string
        ('app=foo,role=bar')."""
        cmd = ["kubectl", "get", kind, "-o", "json"]
        if all_namespaces:
            cmd.append("--all-namespaces")
        elif namespace:
            cmd += ["-n", namespace]
        if label_selector:
            cmd += ["-l", label_selector]
        body = _run(cmd, parse_json=True)
        out: list[dict[str, Any]] = []
        for item in body.get("items", []):
            md = item.get("metadata", {})
            out.append(
                {
                    "name": md.get("name"),
                    "namespace": md.get("namespace"),
                    "kind": item.get("kind"),
                    "apiVersion": item.get("apiVersion"),
                    "labels": md.get("labels") or {},
                    "created": md.get("creationTimestamp"),
                }
            )
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
    ) -> list[dict[str, Any]]:
        """List recent Kubernetes events, optionally filtered by namespace or involved object.

        Use to diagnose Pending pods, failed scheduling, image pull errors,
        failed mounts, unhealthy controllers, or rollout problems.
        `field_selector` example: `involvedObject.name=session-foo`.
        """
        cmd = ["kubectl", "get", "events", "-o", "json", "--sort-by=.lastTimestamp"]
        if all_namespaces:
            cmd.append("--all-namespaces")
        elif namespace:
            cmd += ["-n", namespace]
        if field_selector:
            cmd += ["--field-selector", field_selector]
        body = _run(cmd, parse_json=True)
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
            for e in body.get("items", [])
        ]

    @mcp.tool()
    def top_pods(namespace: str | None = None, all_namespaces: bool = False) -> str:
        """Show Kubernetes pod CPU and memory usage with `kubectl top pods`.

        Use to investigate resource pressure, evictions, high memory, or hot
        workloads. Requires metrics-server.
        """
        cmd = ["kubectl", "top", "pods"]
        if all_namespaces:
            cmd.append("--all-namespaces")
        elif namespace:
            cmd += ["-n", namespace]
        return _run(cmd)

    @mcp.tool()
    def top_nodes() -> str:
        """Show Kubernetes node CPU and memory usage with `kubectl top nodes`.

        Use to investigate cluster capacity, node pressure, and scheduling
        issues. Requires metrics-server.
        """
        return _run(["kubectl", "top", "nodes"])

    @mcp.tool()
    def helm_list(namespace: str | None = None, all_namespaces: bool = True) -> list[dict[str, Any]]:
        """List Helm releases in one namespace or all namespaces.

        Use to discover release names before helm_status, helm_get_values,
        helm_get_manifest, or helm_history. Defaults to all namespaces — narrow with namespace
        when looking for one specific release."""
        cmd = ["helm", "list", "-o", "json"]
        if all_namespaces and not namespace:
            cmd.append("-A")
        elif namespace:
            cmd += ["-n", namespace]
        return _run(cmd, parse_json=True)

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
    def api_resources() -> list[dict[str, Any]]:
        """List Kubernetes API resources and CRDs known to the cluster.

        Use to discover available kinds, short names, namespaced scope, and verbs — useful for discovering CRDs
        like applications.argoproj.io or httproutes.gateway.networking.k8s.io."""
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
        return rows
