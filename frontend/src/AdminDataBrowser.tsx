import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangleIcon,
  DatabaseIcon,
  Loader2Icon,
  LockIcon,
  RefreshCwIcon,
} from "lucide-react";
import { authedFetch } from "./auth";

// Admin-only, read-only database browser. Mirrors the backend surface at
// GET /api/admin/data/tables and /api/admin/data/tables/{table}/rows. The
// server enforces the gate, redaction, and bounds; this component only renders
// what it returns. Pages are capped (PAGE_LIMIT) and appended on demand, so the
// DOM never holds more than the rows the operator has explicitly loaded.

export type DataTable = { name: string; est_rows: number };
export type DataColumnKind = "value" | "redacted" | "bytes";
export type DataColumn = { name: string; type: string; kind: DataColumnKind };
export type RowsResponse = {
  table: string;
  columns: DataColumn[];
  primary_key: string[];
  rows: unknown[][];
  has_more: boolean;
  next_cursor: string;
  est_total: number;
  paginated: boolean;
};

const PAGE_LIMIT = 100;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

async function fetchJSON<T>(path: string): Promise<T> {
  const res = await authedFetch(path);
  const text = await res.text();
  let parsed: unknown = {};
  if (text.trim()) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { detail: text };
    }
  }
  if (!res.ok) {
    const detail = (parsed as { detail?: unknown }).detail;
    throw new Error(String(detail ?? `request returned ${res.status}`));
  }
  return parsed as T;
}

export function formatBytes(value: unknown): string {
  const num = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(num)) return String(value);
  if (num < 1024) return `${num} B`;
  if (num < 1024 * 1024) return `${(num / 1024).toFixed(1)} KB`;
  return `${(num / (1024 * 1024)).toFixed(1)} MB`;
}

function cellText(value: unknown): string {
  if (typeof value === "string") return value;
  return JSON.stringify(value);
}

function DataCell({ column, value }: { column: DataColumn; value: unknown }) {
  if (value === null || value === undefined) {
    return <span className="admin-data-cell-null">null</span>;
  }
  if (column.kind === "redacted") {
    return (
      <span
        className="admin-data-cell-redacted"
        title="Secret value — redacted by the server"
      >
        <LockIcon className="admin-data-cell-lock" aria-hidden="true" />
        redacted
      </span>
    );
  }
  if (column.kind === "bytes") {
    return <span className="admin-data-cell-bytes">{formatBytes(value)}</span>;
  }
  const text = cellText(value);
  return (
    <span className="admin-data-cell-value" title={text}>
      {text}
    </span>
  );
}

export function AdminDataBrowser() {
  const [tables, setTables] = useState<DataTable[] | null>(null);
  const [tablesError, setTablesError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");

  const [selected, setSelected] = useState<string | null>(null);
  const [page, setPage] = useState<RowsResponse | null>(null);
  const [rows, setRows] = useState<unknown[][]>([]);
  const [cursor, setCursor] = useState("");
  const [loadingRows, setLoadingRows] = useState(false);
  const [rowsError, setRowsError] = useState<string | null>(null);

  const loadTables = useCallback(async () => {
    setTablesError(null);
    try {
      const body = await fetchJSON<{ tables: DataTable[] }>(
        "/api/admin/data/tables",
      );
      setTables(body.tables ?? []);
    } catch (err) {
      setTablesError(errorMessage(err));
      setTables([]);
    }
  }, []);

  useEffect(() => {
    void loadTables();
  }, [loadTables]);

  const loadRows = useCallback(
    async (table: string, nextCursor: string, append: boolean) => {
      setLoadingRows(true);
      setRowsError(null);
      try {
        const params = new URLSearchParams({ limit: String(PAGE_LIMIT) });
        if (nextCursor) params.set("cursor", nextCursor);
        const body = await fetchJSON<RowsResponse>(
          `/api/admin/data/tables/${encodeURIComponent(table)}/rows?${params.toString()}`,
        );
        setPage(body);
        setRows((prev) => (append ? [...prev, ...body.rows] : body.rows));
        setCursor(body.next_cursor ?? "");
      } catch (err) {
        setRowsError(errorMessage(err));
      } finally {
        setLoadingRows(false);
      }
    },
    [],
  );

  const selectTable = useCallback(
    (table: string) => {
      setSelected(table);
      setRows([]);
      setPage(null);
      setCursor("");
      void loadRows(table, "", false);
    },
    [loadRows],
  );

  const visibleTables = useMemo(() => {
    const all = tables ?? [];
    const q = filter.trim().toLowerCase();
    return q ? all.filter((t) => t.name.toLowerCase().includes(q)) : all;
  }, [tables, filter]);

  return (
    <div className="admin-data-browser">
      <aside className="admin-data-tables">
        <div className="admin-data-tables-head">
          <span className="run-settings-link-label">
            <DatabaseIcon className="run-settings-link-icon" aria-hidden="true" />
            <span>Tables</span>
          </span>
          <button
            type="button"
            className="run-settings-test-btn"
            onClick={() => void loadTables()}
            title="Reload tables"
          >
            <RefreshCwIcon aria-hidden="true" />
          </button>
        </div>
        <input
          className="break-glass-input admin-data-filter"
          placeholder="Filter tables…"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          aria-label="Filter tables"
        />
        {tablesError && (
          <div className="run-settings-observability-note is-critical">
            {tablesError}
          </div>
        )}
        {tables === null ? (
          <div className="admin-data-loading">
            <Loader2Icon aria-hidden="true" /> Loading…
          </div>
        ) : (
          <ul className="admin-data-table-list">
            {visibleTables.map((table) => (
              <li key={table.name}>
                <button
                  type="button"
                  className={`admin-data-table-item${selected === table.name ? " is-active" : ""}`}
                  onClick={() => selectTable(table.name)}
                >
                  <span className="admin-data-table-name">{table.name}</span>
                  <span className="admin-data-table-count">
                    ~{table.est_rows.toLocaleString()}
                  </span>
                </button>
              </li>
            ))}
            {visibleTables.length === 0 && (
              <li className="admin-data-empty">No tables match.</li>
            )}
          </ul>
        )}
      </aside>

      <section className="admin-data-rows">
        {!selected ? (
          <div className="admin-data-placeholder">
            <DatabaseIcon aria-hidden="true" />
            <p>Select a table to view its rows.</p>
            <p className="admin-data-placeholder-note">
              Read-only. Secret columns (tokens, install nonces) are redacted
              and blob columns show their size only.
            </p>
          </div>
        ) : (
          <>
            <div className="admin-data-rows-head">
              <h3 className="admin-data-rows-title">{selected}</h3>
              {page && (
                <span className="run-settings-scope-value">
                  {rows.length}
                  {page.has_more ? "+" : ""} of ~
                  {page.est_total.toLocaleString()} rows
                  {page.primary_key.length > 0
                    ? ` · pk: ${page.primary_key.join(", ")}`
                    : " · no primary key"}
                </span>
              )}
              <button
                type="button"
                className="run-settings-test-btn"
                onClick={() => selectTable(selected)}
                disabled={loadingRows}
                title="Reload rows"
              >
                <RefreshCwIcon aria-hidden="true" />
              </button>
            </div>

            {rowsError && (
              <div className="run-settings-observability-note is-critical">
                <AlertTriangleIcon aria-hidden="true" /> {rowsError}
              </div>
            )}

            {page && page.columns.length > 0 && (
              <div className="admin-data-table-scroll">
                <table className="admin-data-grid">
                  <thead>
                    <tr>
                      {page.columns.map((column) => (
                        <th key={column.name} title={column.type}>
                          <span className="admin-data-col-name">
                            {column.name}
                          </span>
                          <span className="admin-data-col-type">
                            {column.type}
                          </span>
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row, rowIndex) => (
                      <tr key={rowIndex}>
                        {page.columns.map((column, colIndex) => (
                          <td key={column.name}>
                            <DataCell column={column} value={row[colIndex]} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {page && rows.length === 0 && !loadingRows && !rowsError && (
              <div className="admin-data-empty">No rows.</div>
            )}

            <div className="admin-data-rows-foot">
              {loadingRows && (
                <span className="admin-data-loading">
                  <Loader2Icon aria-hidden="true" /> Loading…
                </span>
              )}
              {cursor && !loadingRows && (
                <button
                  type="button"
                  className="btn-primary run-settings-admin-save"
                  onClick={() => void loadRows(selected, cursor, true)}
                >
                  Load more
                </button>
              )}
              {page && !page.paginated && page.has_more && (
                <span className="admin-data-placeholder-note">
                  This table has no primary key; only the first page is shown.
                </span>
              )}
            </div>
          </>
        )}
      </section>
    </div>
  );
}
