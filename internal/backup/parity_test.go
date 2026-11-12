// Package backup holds the end-to-end backup/restore parity check. The actual
// scripts live in scripts/ so an operator can run them without Go.
package backup_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const restoreTarget = "iris_restore_check"

// TestBackupRestoreParity is opt-in: it drops and recreates iris_restore_check
// on the Postgres server named by IRIS_BACKUP_TEST_DSN. Set that variable to a
// throwaway, migrated Iris database. The live iris/lab databases are never
// touched because the target name is fixed and the source name is guarded.
func TestBackupRestoreParity(t *testing.T) {
	sourceDSN := strings.TrimSpace(os.Getenv("IRIS_BACKUP_TEST_DSN"))
	if sourceDSN == "" {
		t.Skip("set IRIS_BACKUP_TEST_DSN to a throwaway Iris database to run the restore parity test")
	}

	sourceCfg, err := pgx.ParseConfig(sourceDSN)
	if err != nil {
		t.Fatalf("parse IRIS_BACKUP_TEST_DSN: %v", err)
	}
	if sourceCfg.Database == restoreTarget {
		t.Fatalf("source database must not be %s", restoreTarget)
	}
	targetDSN, err := withDatabase(sourceDSN, restoreTarget)
	if err != nil {
		t.Fatalf("build target DSN: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	source, err := pgx.Connect(ctx, sourceDSN)
	if err != nil {
		t.Fatalf("connect source: %v", err)
	}
	defer source.Close(ctx)

	want, err := rowCounts(ctx, source)
	if err != nil {
		t.Fatalf("count source rows: %v", err)
	}
	if want.rooms == 0 {
		t.Fatal("source has no rooms; the parity test needs a real room to be meaningful")
	}

	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "rooms", "room-1", "inbox"), 0o755); err != nil {
		t.Fatalf("create room folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "rooms", "room-1", "inbox", "catatan.txt"), []byte("bukti restore\n"), 0o644); err != nil {
		t.Fatalf("write room file: %v", err)
	}
	backupDir := filepath.Join(tmp, "backups")

	env := append(os.Environ(),
		"IRIS_DATABASE_URL="+sourceDSN,
		"IRIS_DATA_DIR="+dataDir,
		"IRIS_BACKUP_DIR="+backupDir,
		"IRIS_ENV_FILE="+filepath.Join(tmp, "no-env"),
	)

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	runScript(t, root, env, "scripts/backup.sh")

	archives, err := filepath.Glob(filepath.Join(backupDir, "iris-*.tar.gz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives = %v, err = %v, want exactly one", archives, err)
	}

	restoredData := filepath.Join(tmp, "restored")
	runScript(t, root, env, "scripts/restore.sh", archives[0], restoreTarget, "--force", "--data-dir", restoredData)

	target, err := pgx.Connect(ctx, targetDSN)
	if err != nil {
		t.Fatalf("connect restored database: %v", err)
	}
	defer target.Close(ctx)
	defer dropTarget(t, sourceCfg, ctx)

	got, err := rowCounts(ctx, target)
	if err != nil {
		t.Fatalf("count restored rows: %v", err)
	}
	if got != want {
		t.Fatalf("row counts differ: source %+v, restored %+v", want, got)
	}
	t.Logf("row-count parity: rooms=%d messages=%d files=%d continuations=%d", got.rooms, got.messages, got.files, got.continuations)

	restoredFile := filepath.Join(restoredData, "rooms", "room-1", "inbox", "catatan.txt")
	body, err := os.ReadFile(restoredFile)
	if err != nil {
		t.Fatalf("restored room file: %v", err)
	}
	if string(body) != "bukti restore\n" {
		t.Fatalf("restored room file = %q", body)
	}
}

type counts struct {
	rooms         int64
	messages      int64
	files         int64
	continuations int64
}

func rowCounts(ctx context.Context, conn *pgx.Conn) (counts, error) {
	var c counts
	err := conn.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM rooms),
		       (SELECT count(*) FROM messages),
		       (SELECT count(*) FROM files),
		       (SELECT count(*) FROM continuations)`).
		Scan(&c.rooms, &c.messages, &c.files, &c.continuations)
	return c, err
}

func runScript(t *testing.T, root string, env []string, script string, args ...string) {
	t.Helper()
	path := filepath.Join(root, script)
	cmd := exec.Command(path, args...)
	cmd.Dir = root
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	t.Logf("%s %s\n%s", script, strings.Join(args, " "), strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("%s failed: %v", script, err)
	}
}

func withDatabase(dsn, database string) (string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", err
	}
	cfg.Database = database
	// ParseConfig keeps the original connection string fields we can rebuild
	// safely: host, port, user, password, sslmode.
	sslmode := "disable"
	if cfg.TLSConfig != nil {
		sslmode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database, sslmode), nil
}

func dropTarget(t *testing.T, sourceCfg *pgx.ConnConfig, ctx context.Context) {
	t.Helper()
	adminCfg := sourceCfg.Copy()
	adminCfg.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Logf("could not connect to postgres to drop %s: %v", restoreTarget, err)
		return
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS "`+restoreTarget+`" WITH (FORCE)`); err != nil {
		t.Logf("could not drop %s: %v", restoreTarget, err)
		return
	}
	t.Logf("dropped %s", restoreTarget)
}
