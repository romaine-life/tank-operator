package pgstore

// DataBrowser backs the admin-only, read-only "database browser" surface
// (GET /api/admin/data/...). It is the structured, parameterized counterpart
// to the free-form service-principal SQL endpoint (handlers_db_read_query.go):
// the browser never accepts caller SQL. It enumerates the live public-schema
// tables, then reads one table's rows with keyset pagination, applying a
// code-owned redaction policy so bearer tokens and blob bytes never reach the
// browser.
//
// Safety model (mirrors the diagnostic SQL path):
//   - every query runs inside a read-only transaction with a statement
//     timeout, so a handler cannot write/DDL and a runaway read is bounded;
//   - table names are validated against a strict identifier regex AND resolved
//     against the live catalog (unknown -> ErrDataTableUnknown), then quoted —
//     never string-interpolated from caller input into a value position;
//   - row counts use pg_class.reltuples estimates, never count(*), so browsing
//     the billions-row session_events ledger stays O(1);
//   - pagination is keyset by primary key (index-ordered), never large OFFSET.
//
// The redaction policy is the one settled contract here; see isSecretColumn.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// dataBrowserStatementTimeout bounds any single browser query the same
	// way handlers_db_read_query.go bounds the diagnostic SQL path.
	dataBrowserStatementTimeout = "15s"
	// dataBrowserMaxCellChars caps a single rendered cell so an oversized
	// jsonb/text value (e.g. a session_events.payload document) cannot bloat
	// the response. Deep payload inspection stays on the dedicated
	// /api/debug/session-event-ledger surface.
	dataBrowserMaxCellChars = 4000
	// dataRedactedPlaceholder is the value substituted for a secret column.
	// The column still appears (so the operator knows it exists) but its
	// value never crosses into the browser.
	dataRedactedPlaceholder = "‹redacted›"
)

// ErrDataTableUnknown is returned when a (syntactically valid) table name does
// not resolve to a public base table. The handler maps it to 404.
var ErrDataTableUnknown = errors.New("unknown table")

// ErrDataCursorInvalid is returned when a pagination cursor cannot be decoded
// or does not match the table's primary key arity. The handler maps it to 400.
var ErrDataCursorInvalid = errors.New("invalid cursor")

// dataTableIdentRe is the defense-in-depth identifier gate. The name is also
// resolved against the live catalog; this regex rejects anything that could
// not be a bare lower-snake identifier before it is ever quoted into SQL.
var dataTableIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ValidDataTableIdent reports whether name is a syntactically acceptable
// public-schema table identifier. It is a pure pre-filter; membership in the
// live catalog is checked separately by ReadRows.
func ValidDataTableIdent(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	return dataTableIdentRe.MatchString(name)
}

// DataTable is one row of the table directory.
type DataTable struct {
	Name    string `json:"name"`
	EstRows int64  `json:"est_rows"`
}

// DataColumn describes one column of a browsed table. Kind drives rendering:
// "value" (normal), "redacted" (secret, value is the placeholder), or "bytes"
// (a bytea column whose value is its octet length, not its contents).
type DataColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Kind string `json:"kind"`
}

// DataRowsRequest is one page request for a table.
type DataRowsRequest struct {
	Table  string
	Cursor string
	Limit  int
}

// DataRowsPage is one page of rows plus the cursor to fetch the next page.
type DataRowsPage struct {
	Table      string       `json:"table"`
	Columns    []DataColumn `json:"columns"`
	PrimaryKey []string     `json:"primary_key"`
	Rows       [][]any      `json:"rows"`
	HasMore    bool         `json:"has_more"`
	NextCursor string       `json:"next_cursor"`
	EstTotal   int64        `json:"est_total"`
	Paginated  bool         `json:"paginated"`
}

// DataBrowser reads the orchestrator's own Postgres database read-only.
type DataBrowser struct {
	pool *pgxpool.Pool
}

// NewDataBrowser wraps a pool for read-only browsing. The pool is the
// orchestrator's normal pool (the DB's AAD admin); the read-only transaction
// is what bounds blast radius, exactly as the diagnostic SQL path does.
func NewDataBrowser(pool *pgxpool.Pool) *DataBrowser {
	return &DataBrowser{pool: pool}
}

// ListTables enumerates public base tables with row-count estimates.
func (b *DataBrowser) ListTables(ctx context.Context) ([]DataTable, error) {
	tx, err := b.readOnlyTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	rows, err := tx.Query(ctx, `
		SELECT c.relname, GREATEST(c.reltuples, 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r'
		ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DataTable, 0, 64)
	for rows.Next() {
		var t DataTable
		if err := rows.Scan(&t.Name, &t.EstRows); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReadRows returns one keyset page of a single table, with redaction applied.
func (b *DataBrowser) ReadRows(ctx context.Context, req DataRowsRequest) (*DataRowsPage, error) {
	if !ValidDataTableIdent(req.Table) {
		return nil, ErrDataTableUnknown
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	cursorVals, err := decodeRowCursor(req.Cursor)
	if err != nil {
		return nil, ErrDataCursorInvalid
	}

	tx, err := b.readOnlyTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	exists, err := tableExists(ctx, tx, req.Table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrDataTableUnknown
	}

	cols, err := fetchColumns(ctx, tx, req.Table)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, ErrDataTableUnknown
	}
	est, err := fetchEstRows(ctx, tx, req.Table)
	if err != nil {
		return nil, err
	}
	pk, err := fetchPrimaryKey(ctx, tx, req.Table)
	if err != nil {
		return nil, err
	}
	plan := planRows(req.Table, cols, pk)

	// A cursor only makes sense for a keyset-capable table and must match the
	// key arity; anything else is a malformed/forged cursor.
	if len(cursorVals) > 0 && (len(plan.pk) == 0 || len(cursorVals) != len(plan.pk)) {
		return nil, ErrDataCursorInvalid
	}

	query := buildRowsQuery(req.Table, cols, plan, len(cursorVals) > 0, limit)
	args := make([]any, 0, len(cursorVals))
	for _, v := range cursorVals {
		args = append(args, v)
	}

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &DataRowsPage{
		Table:      req.Table,
		PrimaryKey: plan.pk,
		EstTotal:   est,
		Paginated:  len(plan.pk) > 0,
		Columns:    make([]DataColumn, len(cols)),
		Rows:       make([][]any, 0, limit),
	}
	for i, c := range cols {
		page.Columns[i] = DataColumn{Name: c.Name, Type: c.Type, Kind: columnKind(req.Table, c)}
	}
	pkIdx := pkColumnIndexes(cols, plan.pk)

	var lastVals []any
	for rows.Next() {
		if len(page.Rows) >= limit {
			page.HasMore = true
			break
		}
		vals, verr := rows.Values()
		if verr != nil {
			return nil, verr
		}
		out := make([]any, len(cols))
		for i, c := range cols {
			out[i] = applyCellPolicy(req.Table, c, vals[i])
		}
		page.Rows = append(page.Rows, out)
		lastVals = vals
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}

	if page.HasMore && len(plan.pk) > 0 && lastVals != nil {
		cursor := make([]string, len(pkIdx))
		for i, idx := range pkIdx {
			cursor[i] = cursorString(lastVals[idx])
		}
		page.NextCursor = encodeRowCursor(cursor)
	}
	return page, nil
}

func (b *DataBrowser) readOnlyTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '"+dataBrowserStatementTimeout+"'"); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

// columnMeta is the internal per-column shape used to build the row query.
type columnMeta struct {
	Name    string
	Type    string // format_type token, e.g. "bigint", "timestamp with time zone", "bytea"
	IsBytea bool
}

func tableExists(ctx context.Context, tx pgx.Tx, table string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname = $1
		)`, table).Scan(&exists)
	return exists, err
}

func fetchColumns(ctx context.Context, tx pgx.Tx, table string) ([]columnMeta, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = ('"public".' || quote_ident($1))::regclass
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]columnMeta, 0, 32)
	for rows.Next() {
		var c columnMeta
		if err := rows.Scan(&c.Name, &c.Type); err != nil {
			return nil, err
		}
		c.IsBytea = c.Type == "bytea"
		out = append(out, c)
	}
	return out, rows.Err()
}

func fetchEstRows(ctx context.Context, tx pgx.Tx, table string) (int64, error) {
	var est int64
	err := tx.QueryRow(ctx, `
		SELECT GREATEST(c.reltuples, 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1`, table).Scan(&est)
	return est, err
}

func fetchPrimaryKey(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = ('"public".' || quote_ident($1))::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pk []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		pk = append(pk, name)
	}
	return pk, rows.Err()
}

// rowsPlan captures whether a table is keyset-paginatable and the cast types
// for its primary-key columns. An empty pk means "first page only" (no PK, or
// a PK whose types we do not keyset on); the table still browses, it just
// renders a single ordered page rather than paginating.
type rowsPlan struct {
	pk      []string
	pkTypes []string
}

func planRows(table string, cols []columnMeta, pk []string) rowsPlan {
	if len(pk) == 0 {
		return rowsPlan{}
	}
	types := make([]string, len(pk))
	for i, name := range pk {
		// A secret primary-key column (e.g. stream_auth_tickets.ticket) must not
		// ride a keyset cursor: next_cursor would carry the very value the row
		// cell redacts. Degrade such tables to a single ordered page so no
		// secret ever leaves in a cursor.
		if isSecretColumn(table, name) {
			return rowsPlan{}
		}
		ft := columnType(cols, name)
		if !keysetSafeType(ft) {
			return rowsPlan{}
		}
		types[i] = ft
	}
	return rowsPlan{pk: pk, pkTypes: types}
}

// keysetSafeType gates which PK column types we build a typed keyset over. The
// current schema's primary keys are entirely text / integer / timestamp, so
// these cover every real table; an exotic PK type degrades to a single ordered
// page rather than risking an incorrect comparison.
func keysetSafeType(formatType string) bool {
	switch baseType(formatType) {
	case "text", "character varying", "character", "bpchar",
		"smallint", "integer", "bigint",
		"timestamp with time zone", "timestamp without time zone",
		"date", "boolean":
		return true
	}
	return false
}

func baseType(formatType string) string {
	if i := strings.IndexByte(formatType, '('); i >= 0 {
		return strings.TrimSpace(formatType[:i])
	}
	return strings.TrimSpace(formatType)
}

func columnType(cols []columnMeta, name string) string {
	for _, c := range cols {
		if c.Name == name {
			return c.Type
		}
	}
	return ""
}

func pkColumnIndexes(cols []columnMeta, pk []string) []int {
	idx := make([]int, 0, len(pk))
	for _, name := range pk {
		for i, c := range cols {
			if c.Name == name {
				idx = append(idx, i)
				break
			}
		}
	}
	return idx
}

// buildRowsQuery assembles the SELECT for one page. It is pure (no DB access)
// so the injection-safety and keyset shape are unit-testable. Every identifier
// is quoted; cursor values bind as parameters cast to the PK column type.
func buildRowsQuery(table string, cols []columnMeta, plan rowsPlan, hasCursor bool, limit int) string {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	for i, c := range cols {
		if i > 0 {
			sb.WriteString(", ")
		}
		if c.IsBytea {
			// Never select blob bytes; expose only the size.
			sb.WriteString("octet_length(")
			sb.WriteString(quoteIdent(c.Name))
			sb.WriteString(") AS ")
			sb.WriteString(quoteIdent(c.Name))
		} else {
			sb.WriteString(quoteIdent(c.Name))
		}
	}
	sb.WriteString(" FROM ")
	sb.WriteString(quoteQualified(table))

	if len(plan.pk) > 0 {
		if hasCursor {
			sb.WriteString(" WHERE (")
			for i, col := range plan.pk {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(quoteIdent(col))
			}
			sb.WriteString(") > (")
			for i := range plan.pk {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString("CAST($")
				sb.WriteString(strconv.Itoa(i + 1))
				sb.WriteString(" AS ")
				sb.WriteString(plan.pkTypes[i])
				sb.WriteString(")")
			}
			sb.WriteString(")")
		}
		sb.WriteString(" ORDER BY ")
		for i, col := range plan.pk {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(quoteIdent(col))
		}
	} else {
		// No keyset key: render a single stable-ordered page.
		sb.WriteString(" ORDER BY 1")
	}
	sb.WriteString(" LIMIT ")
	sb.WriteString(strconv.Itoa(limit + 1))
	return sb.String()
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteQualified(table string) string {
	return `"public".` + quoteIdent(table)
}

// columnKind classifies a column for the renderer and for value policy.
func columnKind(table string, c columnMeta) string {
	if isSecretColumn(table, c.Name) {
		return "redacted"
	}
	if c.IsBytea {
		return "bytes"
	}
	return "value"
}

// applyCellPolicy turns a scanned pgx value into a JSON-safe, redacted cell.
func applyCellPolicy(table string, c columnMeta, v any) any {
	if isSecretColumn(table, c.Name) {
		return dataRedactedPlaceholder
	}
	// bytea columns already arrive as their octet_length (an integer).
	return jsonSafe(v)
}

// secretColumnExplicit lists (table, column) pairs whose column name is too
// generic to pattern-match. `state` is a common non-secret column elsewhere
// (sessions.test_state, rollout_state, clone_state), so the github install
// nonce must be named explicitly; likewise the stream-ticket bearer.
var secretColumnExplicit = map[string]map[string]bool{
	"github_install_states": {"state": true},
	"stream_auth_tickets":   {"ticket": true},
	"message_link_shares":   {"token_hash": true},
}

// secretColumnSubstrings redacts any column whose name contains one of these,
// in any table. Conservative on purpose: bearer/credential material only.
var secretColumnSubstrings = []string{
	"token", "secret", "password", "api_key", "apikey", "private_key", "credential",
}

func isSecretColumn(table, column string) bool {
	col := strings.ToLower(column)
	if cols, ok := secretColumnExplicit[table]; ok && cols[col] {
		return true
	}
	for _, sub := range secretColumnSubstrings {
		if strings.Contains(col, sub) {
			return true
		}
	}
	return false
}

// jsonSafe mirrors handlers_db_read_query.go's stringifyDBValue (different
// package boundary), adding a cell-size cap. pg types that do not marshal
// natively (numeric, uuid, jsonb bytes) fall through to a truncated string.
func jsonSafe(v any) any {
	switch x := v.(type) {
	case nil, bool, float64, float32, int64, int32, int, int16, int8, uint64, uint32:
		return x
	case string:
		return truncateCell(x)
	case []byte:
		return truncateCell(string(x))
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return truncateCell(fmt.Sprintf("%v", x))
	}
}

func truncateCell(s string) string {
	if len(s) <= dataBrowserMaxCellChars {
		return s
	}
	cut := dataBrowserMaxCellChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("… (%d bytes total, truncated)", len(s))
}

// cursorString renders a primary-key value into the canonical string that
// CAST($n AS <pkType>) reproduces on the next page. Only the keyset-safe types
// reach here (see keysetSafeType).
func cursorString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func encodeRowCursor(vals []string) string {
	b, _ := json.Marshal(vals)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeRowCursor(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var vals []string
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil, err
	}
	return vals, nil
}
