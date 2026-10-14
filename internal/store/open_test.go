package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

func TestOpenPrivateEscapedDatabaseAndEveryConnection(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "new", "space ?&_pragma=foreign_keys(0)#.db")
	s, err := Open(ctx, path, 1250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for file, mode := range map[string]os.FileMode{parent: 0750, filepath.Dir(path): 0700, path: 0600, path + "-wal": 0600, path + "-shm": 0600} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: %v %v", filepath.Base(file), info, err)
		}
	}
	conns := make([]*sql.Conn, 4)
	for i := range conns {
		conns[i], err = s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conns[i].Close()
	}
	for i, conn := range conns {
		var fk, busy, sync int
		var wal string
		for q, dest := range map[string]any{"PRAGMA foreign_keys": &fk, "PRAGMA busy_timeout": &busy, "PRAGMA synchronous": &sync, "PRAGMA journal_mode": &wal} {
			if err := conn.QueryRowContext(ctx, q).Scan(dest); err != nil {
				t.Fatal(err)
			}
		}
		if fk != 1 || busy != 1250 || sync != 2 || wal != "wal" {
			t.Fatalf("connection %d: %d %d %d %s", i, fk, busy, sync, wal)
		}
	}
}

func TestRealSessionPersistsAndCascades(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.db")
	s, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d := s.SessionStore().NewDevice()
	jid := types.NewADJID("15550000000", 0, 7)
	d.ID = &jid
	d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	d.Platform = "synthetic-test"
	if err := d.Save(ctx); err != nil {
		t.Fatal(err)
	}
	identity := append([]byte(nil), d.IdentityKey.Priv[:]...)
	if err := d.Sessions.PutSession(ctx, "synthetic-peer", []byte("synthetic-session")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO whatsmeow_identity_keys (our_jid,their_id,identity) VALUES ('missing','peer',zeroblob(32))"); err == nil {
		t.Fatal("orphan accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.SessionStore().GetDevice(ctx, jid)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	data, err := d.Sessions.GetSession(ctx, "synthetic-peer")
	if err != nil || !bytes.Equal(data, []byte("synthetic-session")) || !bytes.Equal(identity, d.IdentityKey.Priv[:]) {
		t.Fatal("session reopen mismatch", err)
	}
	if err := d.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM whatsmeow_sessions").Scan(&n); err != nil || n != 0 {
		t.Fatal("cascade", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err = s.SessionStore().GetDevice(ctx, jid)
	if err != nil || d != nil {
		t.Fatal("deleted device resurrected", err)
	}
}

func TestMigrationsIdempotentRejectFutureAndChecksum(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	s, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_migration").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, table := range []string{"bridge_chat", "bridge_message", "bridge_part", "bridge_source_state", "bridge_inbox", "bridge_outbox", "bridge_echo", "bridge_timer", "bridge_cursor"} {
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(table, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO bridge_migration(version,checksum,applied_at_ms) VALUES (999,'future',0)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, path, time.Second); !errors.Is(err, ErrSchema) {
		t.Fatal("future accepted", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DELETE FROM bridge_migration WHERE version=999; UPDATE bridge_migration SET checksum='wrong'"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path, time.Second); !errors.Is(err, ErrSchema) {
		t.Fatal("checksum accepted", err)
	}
}

func TestMigrationRollback(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bad := []migration{{version: 1, sql: "CREATE TABLE temporary_test(id INTEGER); INSERT INTO nonexistent VALUES(1);"}}
	if err := migrate(ctx, db, bad); err == nil {
		t.Fatal("bad migration succeeded")
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name='temporary_test'").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial migration", n, err)
	}
}

func TestOpenErrorDoesNotExposePath(t *testing.T) {
	secret := "DO-NOT-EXPOSE"
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), secret), -time.Second); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("invalid open leaked or succeeded", err)
	}
}
