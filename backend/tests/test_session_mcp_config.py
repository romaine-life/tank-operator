"""Tests for the MCP config mounted into session pods."""
from __future__ import annotations

import json
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]


def test_default_session_mcp_config_exposes_one_tank_surface() -> None:
    config = json.loads((REPO_ROOT / "k8s/session-config/mcp.json").read_text())

    servers = config["mcpServers"]
    assert servers["tank-operator"] == {
        "type": "http",
        "url": "http://127.0.0.1:9996/",
    }
    assert "tank" not in servers
