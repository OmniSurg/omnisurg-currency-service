// Package harness provides shared integration test helpers: an ephemeral
// Postgres via testcontainers, migration application, and a non superuser
// application role so RLS is fully enforced in tests.
package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// StartPostgres launches a throwaway Postgres 16 container, applies every
// migration as the bootstrap owner, then creates a NON superuser application
// role and returns a DSN that connects as that role.
//
// PostgreSQL superusers and table owners BYPASS row level security even with
// FORCE ROW LEVEL SECURITY. The bootstrap user testcontainers creates is a
// superuser, so the application pool must connect as a plain role for the
// tenant isolation tests to be real.
func StartPostgres(t *testing.T) (appDSN string, stop func()) {
	t.Helper()
	ctx := context.Background()
	container, err := tcpg.Run(ctx,
		"postgres:16-alpine",
		tcpg.WithDatabase("omnisurg_currency"),
		tcpg.WithUsername("omnisurg_root"),
		tcpg.WithPassword("root"),
		tcpg.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	cleanup := func() { _ = container.Terminate(ctx) }

	adminDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	applyMigrations(t, adminDSN)
	provisionAppRole(t, adminDSN)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	appDSN = fmt.Sprintf("postgres://omnisurg_app:app@%s:%s/omnisurg_currency?sslmode=disable", host, port.Port())
	return appDSN, cleanup
}

func applyMigrations(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)

	_, thisFile, _, _ := runtime.Caller(0)
	migDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	entries, err := os.ReadDir(migDir)
	require.NoError(t, err)

	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)
	for _, name := range ups {
		sqlBytes, err := os.ReadFile(filepath.Join(migDir, name))
		require.NoError(t, err)
		_, err = conn.Exec(ctx, string(sqlBytes))
		require.NoError(t, err, "applying migration %s", name)
	}
}

func provisionAppRole(t *testing.T, adminDSN string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	require.NoError(t, err)
	defer conn.Close(ctx)
	stmts := []string{
		"CREATE ROLE omnisurg_app LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS",
		"GRANT USAGE ON SCHEMA public TO omnisurg_app",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO omnisurg_app",
	}
	for _, s := range stmts {
		_, err := conn.Exec(ctx, s)
		require.NoError(t, err, "provision app role: %s", s)
	}
}
