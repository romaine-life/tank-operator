// Package pgstore is the shared Postgres connection layer for tank-operator's
// durable-history stores. It builds a pgxpool.Pool whose connections present a
// fresh Azure AD access token as the password on every dial, so the orchestrator
// pod authenticates to Azure Database for PostgreSQL through its workload
// identity instead of a static admin password.
//
// The Azure AD resource ID for the OSS RDBMS service is fixed
// (`https://ossrdbms-aad.database.windows.net/.default`). Tokens expire roughly
// every hour, so connections are recycled before that lifetime so pgx never
// presents an expired credential. Background health-check workers and the
// schema-migration runner also live in this package.
package pgstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AADTokenScope is the resource identifier for Azure Database for PostgreSQL's
// AAD-issued access tokens. It is a fixed Microsoft-owned value, not a per-server
// or per-tenant string. The trailing `/.default` selects the v2 token endpoint.
const AADTokenScope = "https://ossrdbms-aad.database.windows.net/.default"

// MaxConnLifetime is bounded below the AAD access-token TTL (~60 minutes) so a
// connection never holds a token past its expiry. Refreshes happen transparently
// inside BeforeConnect when pgx recycles the connection.
const MaxConnLifetime = 50 * time.Minute

// Config describes how to reach the Postgres Flexible Server. Username is the
// AAD principal name as it appears in the server's `pg_authid` (for a UAMI,
// this is the UAMI's display name, e.g. `claude-credentials-refresher-identity`).
//
// QueryMetrics is optional; when non-nil every query the pool runs is
// observed by the tracer (operation + outcome + duration). Production
// always supplies a Prometheus-backed implementation through
// cmd/tank-operator/observability.go. Tests can pass nil to opt out.
type Config struct {
	Host         string
	Database     string
	Username     string
	Credential   azcore.TokenCredential
	QueryMetrics SQLMetrics
}

// NewPool builds a pgxpool.Pool wired with AAD authentication. The pool
// validates one connection synchronously before returning so misconfiguration
// fails fast at startup rather than on first request.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	host := strings.TrimSpace(cfg.Host)
	database := strings.TrimSpace(cfg.Database)
	username := strings.TrimSpace(cfg.Username)
	if host == "" || database == "" || username == "" {
		return nil, fmt.Errorf("pgstore: host, database, and username are required")
	}
	if cfg.Credential == nil {
		return nil, fmt.Errorf("pgstore: azcore.TokenCredential is required")
	}

	// Construct a libpq-style DSN. The password is set per-connection by
	// BeforeConnect, so the static URL omits it. sslmode=require is mandatory
	// for Flexible Server's public endpoint.
	dsn := fmt.Sprintf(
		"postgres://%s@%s/%s?sslmode=require",
		url.QueryEscape(username),
		host,
		url.QueryEscape(database),
	)
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: parse DSN: %w", err)
	}
	poolConfig.MaxConnLifetime = MaxConnLifetime
	// MaxConns is bounded against the shared Azure Postgres Flex Server's
	// `max_connections` ceiling. B1ms's default cap is 50. The orchestrator
	// runs as prod×2 replicas + up to 5 test slots×2 replicas = 12 pods
	// sharing one DB, each with its own pool; 12×4 = 48 fits the cap with
	// 2-conn headroom. Raising MaxConns or growing the slot fleet without
	// matching B-tier upgrade reintroduces the SQLSTATE 53300 crash-loop
	// observed on 2026-05-25 ("remaining connection slots are reserved
	// for roles with the SUPERUSER attribute").
	poolConfig.MaxConns = 4
	poolConfig.MinConns = 1
	// Operational headroom (issue #1079 item 6): a server-side statement
	// timeout bounds any one query's hold on the 4-conn pool — a runaway
	// scan cannot wedge the pod's entire DB capacity behind it (the
	// migration runner overrides per-migration with its own 120s budget
	// via SET LOCAL). 30s is far above every steady-state query's p99 and
	// far below "the pod is effectively down".
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, ok := poolConfig.ConnConfig.RuntimeParams["statement_timeout"]; !ok {
		poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	}
	if cfg.QueryMetrics != nil {
		poolConfig.ConnConfig.Tracer = NewQueryTracer(cfg.QueryMetrics)
	}

	credential := cfg.Credential
	poolConfig.BeforeConnect = func(ctx context.Context, c *pgx.ConnConfig) error {
		tok, err := credential.GetToken(ctx, policy.TokenRequestOptions{
			Scopes: []string{AADTokenScope},
		})
		if err != nil {
			return fmt.Errorf("pgstore: acquire AAD token: %w", err)
		}
		c.Password = tok.Token
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("pgstore: build pool: %w", err)
	}

	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}

	return pool, nil
}
