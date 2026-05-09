"""Per-user profile storage in Cosmos DB.

Document shape (id == email == partition key, all lowercased):

    {
      "id": "user@example.com",
      "email": "user@example.com",
      "github_login": null,
      "installation_id": null,
      "created_at": "<iso8601>",
      "updated_at": "<iso8601>"
    }

Auto-created on first login when /api/auth/microsoft/login mints a session
JWT for an allowed email. The multi-user GitHub App flow (#57 stage 2)
populates installation_id from the install callback; mcp-github multi-tenancy
(#57 stage 3) reads it to mint a per-caller installation token.

Auth: workload identity. The orchestrator pod's azure.workload.identity/use
label causes the WI webhook to inject a federated SA-token at
AZURE_FEDERATED_TOKEN_FILE; DefaultAzureCredential picks it up alongside
AZURE_CLIENT_ID + AZURE_TENANT_ID env vars (already set for the existing
KV write path) and exchanges for an Entra access token. The Cosmos account
has local_authentication_disabled = true, so this is the only auth path.
"""

from __future__ import annotations

import datetime
import logging
import os
from dataclasses import asdict, dataclass
from typing import Any

from azure.cosmos.aio import CosmosClient
from azure.cosmos.exceptions import CosmosResourceNotFoundError
from azure.identity.aio import DefaultAzureCredential

log = logging.getLogger(__name__)

COSMOS_ENDPOINT = os.environ.get("COSMOS_ENDPOINT", "")
COSMOS_DATABASE = os.environ.get("COSMOS_DATABASE", "tank-operator")
COSMOS_PROFILES_CONTAINER = os.environ.get("COSMOS_PROFILES_CONTAINER", "profiles")
COSMOS_ACTIVE_RUNS_CONTAINER = os.environ.get(
    "COSMOS_ACTIVE_RUNS_CONTAINER", "active-runs"
)
SESSION_REGISTRY_SCOPE = os.environ.get("SESSION_REGISTRY_SCOPE", "default").strip() or "default"


@dataclass
class Profile:
    email: str
    github_login: str | None = None
    installation_id: int | None = None
    created_at: str = ""
    updated_at: str = ""

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class SessionRecord:
    id: str
    email: str
    mode: str
    scope: str = "default"
    pod_name: str | None = None
    name: str | None = None
    visible: bool = True
    requested_at: str = ""
    created_at: str = ""
    updated_at: str = ""

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass
class ActiveRunRecord:
    session_id: str
    email: str
    run_id: str
    pod_name: str
    provider: str
    status: str = "running"
    stream_path: str = ""
    pid_path: str = ""
    started_at: str = ""
    updated_at: str = ""
    completed_at: str | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def _now_iso() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def _profile_from_doc(doc: dict) -> Profile:
    return Profile(
        email=doc["email"],
        github_login=doc.get("github_login"),
        installation_id=doc.get("installation_id"),
        created_at=doc.get("created_at", ""),
        updated_at=doc.get("updated_at", ""),
    )


def _session_from_doc(doc: dict) -> SessionRecord:
    return SessionRecord(
        id=doc.get("session_id") or doc["id"].removeprefix("session:"),
        email=doc["email"],
        mode=doc.get("mode", "claude_cli"),
        scope=doc.get("session_scope") or doc.get("scope") or "default",
        pod_name=doc.get("pod_name"),
        name=doc.get("name"),
        visible=doc.get("visible", True),
        requested_at=doc.get("requested_at", ""),
        created_at=doc.get("created_at", ""),
        updated_at=doc.get("updated_at", ""),
    )


def _active_run_from_doc(doc: dict) -> ActiveRunRecord:
    return ActiveRunRecord(
        session_id=doc["session_id"],
        email=doc["email"],
        run_id=doc["run_id"],
        pod_name=doc["pod_name"],
        provider=doc.get("provider", "claude"),
        status=doc.get("status", "running"),
        stream_path=doc.get("stream_path", ""),
        pid_path=doc.get("pid_path", ""),
        started_at=doc.get("started_at", ""),
        updated_at=doc.get("updated_at", ""),
        completed_at=doc.get("completed_at"),
    )


class ProfileStore:
    """Async Cosmos client wrapper for the profiles container.

    `_enabled` gates the whole class on COSMOS_ENDPOINT being set, so a
    cluster install where tofu hasn't yet provisioned Cosmos boots without
    crash-looping — get_or_create returns a stub Profile in that case.
    Once the env var lands, behavior switches to the real store on next
    pod restart. This is the same degraded-mode pattern sessions.py uses
    for unresolved Service hostnames.
    """

    def __init__(self) -> None:
        self._credential: DefaultAzureCredential | None = None
        self._client: CosmosClient | None = None
        self._container = None  # azure.cosmos.aio.ContainerProxy
        self._enabled = bool(COSMOS_ENDPOINT)

    async def startup(self) -> None:
        if not self._enabled:
            log.warning(
                "COSMOS_ENDPOINT unset; profile storage disabled "
                "(stub Profiles will be returned)"
            )
            return
        self._credential = DefaultAzureCredential()
        self._client = CosmosClient(COSMOS_ENDPOINT, credential=self._credential)
        database = self._client.get_database_client(COSMOS_DATABASE)
        self._container = database.get_container_client(COSMOS_PROFILES_CONTAINER)

    async def shutdown(self) -> None:
        if self._client is not None:
            await self._client.close()
        if self._credential is not None:
            await self._credential.close()

    async def get_or_create(self, email: str) -> Profile:
        """Return the profile for `email`, creating an empty row if missing.

        Called on /api/auth/microsoft/login so a profile row exists before
        any feature that needs one (install callback, multi-tenant
        mcp-github) tries to read it.
        """
        normalized = email.lower()
        if not self._enabled or self._container is None:
            return Profile(email=normalized)
        try:
            doc = await self._container.read_item(
                item=normalized, partition_key=normalized
            )
            return _profile_from_doc(doc)
        except CosmosResourceNotFoundError:
            now = _now_iso()
            doc = {
                "id": normalized,
                "email": normalized,
                "github_login": None,
                "installation_id": None,
                "created_at": now,
                "updated_at": now,
            }
            await self._container.create_item(body=doc)
            return _profile_from_doc(doc)

    async def get(self, email: str) -> Profile:
        """Return the profile for `email`. Equivalent to get_or_create today;
        kept as a separate name so future code that should never auto-create
        (e.g. install callback, which expects the row to already exist from
        login) can bind to a method that signals that intent.
        """
        return await self.get_or_create(email)

    async def update_installation(
        self, email: str, installation_id: int, github_login: str | None
    ) -> Profile:
        """Set the profile's GitHub App installation. Used by the install
        callback (#57 stage 2).

        Tolerates a missing row — if the user hits the callback without a
        prior login (stale tab, manual URL), we create the profile rather
        than 500. The state JWT verified by the caller is the auth anchor.
        """
        normalized = email.lower()
        if not self._enabled or self._container is None:
            return Profile(
                email=normalized,
                installation_id=installation_id,
                github_login=github_login,
            )
        now = _now_iso()
        try:
            doc = await self._container.read_item(
                item=normalized, partition_key=normalized
            )
        except CosmosResourceNotFoundError:
            doc = {
                "id": normalized,
                "email": normalized,
                "created_at": now,
            }
        doc["installation_id"] = installation_id
        doc["github_login"] = github_login
        doc["updated_at"] = now
        await self._container.upsert_item(body=doc)
        return _profile_from_doc(doc)


class SessionRegistryStore:
    """User-facing session registry backed by the Cosmos profiles container.

    Kubernetes remains the runtime source of truth, but this store owns product
    visibility: only registered, visible sessions appear in the UI. We reuse the
    existing `/email`-partitioned profiles container so this can roll out before
    any new infrastructure container exists. Deployments set a registry scope
    so prod and validation slots do not show or mutate each other's sessions.
    """

    def __init__(self, *, scope: str | None = None) -> None:
        self._credential: DefaultAzureCredential | None = None
        self._client: CosmosClient | None = None
        self._container = None
        self._enabled = bool(COSMOS_ENDPOINT)
        self._scope = (scope or SESSION_REGISTRY_SCOPE).strip() or "default"
        self._memory: dict[str, dict[str, dict[str, SessionRecord]]] = {}

    async def startup(self) -> None:
        if not self._enabled:
            log.warning(
                "COSMOS_ENDPOINT unset; session registry using in-memory storage"
            )
            return
        self._credential = DefaultAzureCredential()
        self._client = CosmosClient(COSMOS_ENDPOINT, credential=self._credential)
        database = self._client.get_database_client(COSMOS_DATABASE)
        self._container = database.get_container_client(COSMOS_PROFILES_CONTAINER)

    async def shutdown(self) -> None:
        if self._client is not None:
            await self._client.close()
        if self._credential is not None:
            await self._credential.close()

    async def upsert(
        self,
        *,
        email: str,
        session_id: str,
        mode: str,
        pod_name: str | None,
        name: str | None = None,
        requested_at: str | None = None,
        created_at: str | None = None,
        visible: bool = True,
    ) -> SessionRecord:
        normalized = email.lower()
        now = _now_iso()
        existing = await self.get(normalized, session_id)
        record = SessionRecord(
            id=session_id,
            email=normalized,
            mode=mode,
            scope=self._scope,
            pod_name=pod_name,
            name=name if name is not None else (existing.name if existing else None),
            visible=visible,
            requested_at=requested_at or (existing.requested_at if existing else now),
            created_at=created_at or (existing.created_at if existing else now),
            updated_at=now,
        )
        if not self._enabled or self._container is None:
            self._memory.setdefault(self._scope, {}).setdefault(normalized, {})[session_id] = record
            return record
        await self._container.upsert_item(body=_session_doc(record))
        return record

    async def get(self, email: str, session_id: str) -> SessionRecord | None:
        normalized = email.lower()
        if not self._enabled or self._container is None:
            return self._memory.get(self._scope, {}).get(normalized, {}).get(session_id)
        try:
            doc = await self._container.read_item(
                item=_session_doc_id(self._scope, session_id), partition_key=normalized
            )
        except CosmosResourceNotFoundError:
            if self._scope != "default":
                return None
            try:
                doc = await self._container.read_item(
                    item=f"session:{session_id}", partition_key=normalized
                )
            except CosmosResourceNotFoundError:
                return None
        if doc.get("type") != "session":
            return None
        record = _session_from_doc(doc)
        if record.scope != self._scope:
            return None
        return record

    async def list(self, email: str) -> list[SessionRecord]:
        normalized = email.lower()
        if not self._enabled or self._container is None:
            return [
                r
                for r in self._memory.get(self._scope, {}).get(normalized, {}).values()
                if r.visible
            ]
        if self._scope == "default":
            query = (
                "SELECT * FROM c WHERE c.email = @email "
                "AND c.type = 'session' AND c.visible = true "
                "AND (NOT IS_DEFINED(c.session_scope) OR c.session_scope = 'default')"
            )
            params = [{"name": "@email", "value": normalized}]
        else:
            query = (
                "SELECT * FROM c WHERE c.email = @email "
                "AND c.type = 'session' AND c.visible = true "
                "AND c.session_scope = @scope"
            )
            params = [
                {"name": "@email", "value": normalized},
                {"name": "@scope", "value": self._scope},
            ]
        items = self._container.query_items(
            query=query,
            parameters=params,
            partition_key=normalized,
        )
        records = [_session_from_doc(item) async for item in items]
        records.sort(key=lambda r: r.created_at)
        return records

    async def set_name(
        self, email: str, session_id: str, name: str | None
    ) -> SessionRecord | None:
        record = await self.get(email, session_id)
        if record is None:
            return None
        return await self.upsert(
            email=email,
            session_id=session_id,
            mode=record.mode,
            pod_name=record.pod_name,
            name=name,
            requested_at=record.requested_at,
            created_at=record.created_at,
            visible=record.visible,
        )

    async def mark_deleted(self, email: str, session_id: str) -> None:
        normalized = email.lower()
        record = await self.get(normalized, session_id)
        if record is None:
            return
        record.visible = False
        record.updated_at = _now_iso()
        if not self._enabled or self._container is None:
            self._memory.setdefault(self._scope, {}).setdefault(normalized, {})[session_id] = record
            return
        await self._container.upsert_item(body=_session_doc(record))


def _session_doc(record: SessionRecord) -> dict[str, Any]:
    return {
        "id": _session_doc_id(record.scope, record.id),
        "type": "session",
        "email": record.email,
        "session_scope": record.scope,
        "session_id": record.id,
        "mode": record.mode,
        "pod_name": record.pod_name,
        "name": record.name,
        "visible": record.visible,
        "requested_at": record.requested_at,
        "created_at": record.created_at,
        "updated_at": record.updated_at,
    }


def _session_doc_id(scope: str, session_id: str) -> str:
    if scope == "default":
        return f"session:{session_id}"
    return f"session:{scope}:{session_id}"


class ActiveRunStore:
    """Durable pointer to the currently believed-active run per session.

    The pod remains the liveness source of truth. This store is only a durable
    breadcrumb so a restarted backend can discover which run_id/pid/stream to
    verify on the session pod. It deliberately writes only on lifecycle edges,
    not as a heartbeat.
    """

    ACTIVE_STATUSES = {"starting", "running"}

    def __init__(self) -> None:
        self._credential: DefaultAzureCredential | None = None
        self._client: CosmosClient | None = None
        self._container = None
        self._enabled = bool(COSMOS_ENDPOINT)
        self._memory: dict[str, ActiveRunRecord] = {}

    async def startup(self) -> None:
        if not self._enabled:
            log.warning(
                "COSMOS_ENDPOINT unset; active run registry using in-memory storage"
            )
            return
        self._credential = DefaultAzureCredential()
        self._client = CosmosClient(COSMOS_ENDPOINT, credential=self._credential)
        database = self._client.get_database_client(COSMOS_DATABASE)
        self._container = database.get_container_client(COSMOS_ACTIVE_RUNS_CONTAINER)

    async def shutdown(self) -> None:
        if self._client is not None:
            await self._client.close()
        if self._credential is not None:
            await self._credential.close()

    async def start(
        self,
        *,
        email: str,
        session_id: str,
        run_id: str,
        pod_name: str,
        provider: str,
        stream_path: str,
        pid_path: str,
    ) -> ActiveRunRecord:
        normalized = email.lower()
        now = _now_iso()
        record = ActiveRunRecord(
            session_id=session_id,
            email=normalized,
            run_id=run_id,
            pod_name=pod_name,
            provider=provider,
            status="running",
            stream_path=stream_path,
            pid_path=pid_path,
            started_at=now,
            updated_at=now,
        )
        if not self._enabled or self._container is None:
            self._memory[session_id] = record
            return record
        await self._container.upsert_item(body=_active_run_doc(record))
        return record

    async def get_active(self, session_id: str) -> ActiveRunRecord | None:
        if not self._enabled or self._container is None:
            record = self._memory.get(session_id)
            if record is None or record.status not in self.ACTIVE_STATUSES:
                return None
            return record
        try:
            doc = await self._container.read_item(
                item=session_id, partition_key=session_id
            )
        except CosmosResourceNotFoundError:
            return None
        record = _active_run_from_doc(doc)
        if record.status not in self.ACTIVE_STATUSES:
            return None
        return record

    async def mark_completed(self, session_id: str, run_id: str) -> None:
        await self._mark_terminal(session_id, run_id, "completed")

    async def mark_stale(self, session_id: str, run_id: str) -> None:
        await self._mark_terminal(session_id, run_id, "stale")

    async def _mark_terminal(
        self, session_id: str, run_id: str, status: str
    ) -> None:
        record = await self.get_active(session_id)
        if record is None or record.run_id != run_id:
            return
        now = _now_iso()
        record.status = status
        record.updated_at = now
        record.completed_at = now
        if not self._enabled or self._container is None:
            self._memory[session_id] = record
            return
        await self._container.upsert_item(body=_active_run_doc(record))


def _active_run_doc(record: ActiveRunRecord) -> dict[str, Any]:
    return {
        "id": record.session_id,
        "type": "active_run",
        "session_id": record.session_id,
        "email": record.email,
        "run_id": record.run_id,
        "pod_name": record.pod_name,
        "provider": record.provider,
        "status": record.status,
        "stream_path": record.stream_path,
        "pid_path": record.pid_path,
        "started_at": record.started_at,
        "updated_at": record.updated_at,
        "completed_at": record.completed_at,
    }
