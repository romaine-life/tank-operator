"""Entrypoint for the tank-operator orchestrator.

Listens on two ports when the OAuth gateway TLS material is present:
  - PORT (default 8000): plain HTTP, fronted by Envoy Gateway with letsencrypt
    for the public API at tank.romaine.life.
  - OAUTH_GATEWAY_PORT (default 8443): HTTPS with a self-signed leaf for
    `platform.claude.com`. Reached only by session pods inside the cluster
    via /etc/hosts override + NODE_EXTRA_CA_CERTS pointing at our CA.

If the cert files aren't mounted (e.g. local dev), only the HTTP port is
served, so `python -m tank_operator` still works without cert-manager.

If RELOAD=true (slot-env iter loop only), only the HTTP port is served and
uvicorn watches RELOAD_DIRS (default /opt/live-pkg) for source changes,
hot-reloading workers in-process. Avoids the kill-the-pid-1 dance for
backend live-cp iteration. Sessions that depend on the TLS port (claude
auth proxying) won't work in this mode — drop RELOAD when testing those.
"""
import asyncio
import os
from pathlib import Path

import uvicorn


def _http_config() -> uvicorn.Config:
    return uvicorn.Config(
        "tank_operator.api:app",
        host="0.0.0.0",
        port=int(os.environ.get("PORT", "8000")),
        log_level="info",
    )


def _tls_config() -> uvicorn.Config | None:
    cert = os.environ.get("OAUTH_GATEWAY_TLS_CERT", "/etc/oauth-gateway-tls/tls.crt")
    key = os.environ.get("OAUTH_GATEWAY_TLS_KEY", "/etc/oauth-gateway-tls/tls.key")
    if not (Path(cert).exists() and Path(key).exists()):
        return None
    return uvicorn.Config(
        "tank_operator.api:app",
        host="0.0.0.0",
        port=int(os.environ.get("OAUTH_GATEWAY_PORT", "8443")),
        ssl_certfile=cert,
        ssl_keyfile=key,
        log_level="info",
    )


async def _serve_all() -> None:
    configs = [_http_config()]
    tls = _tls_config()
    if tls is not None:
        configs.append(tls)
    servers = [uvicorn.Server(c) for c in configs]
    await asyncio.gather(*(s.serve() for s in servers))


def _reload_main() -> None:
    """Single-port HTTP server with watchfiles-driven hot reload.

    Used by slot envs that set RELOAD=true. uvicorn.run owns process
    lifecycle in this mode (subprocess workers, reload via watchfiles),
    so it doesn't compose with asyncio.gather over two ports — TLS is
    intentionally skipped here.
    """
    reload_dirs = os.environ.get("RELOAD_DIRS", "/opt/live-pkg").split(",")
    uvicorn.run(
        "tank_operator.api:app",
        host="0.0.0.0",
        port=int(os.environ.get("PORT", "8000")),
        log_level="info",
        reload=True,
        reload_dirs=[d for d in reload_dirs if d],
    )


def main() -> None:
    if os.environ.get("RELOAD", "").lower() in ("1", "true", "yes"):
        _reload_main()
    else:
        asyncio.run(_serve_all())


if __name__ == "__main__":
    main()
