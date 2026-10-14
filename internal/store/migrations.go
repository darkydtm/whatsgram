package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	sql     string
}

func bridgeMigrations() []migration {
	data, err := migrationFiles.ReadFile("migrations/001-initial.sql")
	if err != nil {
		panic("missing embedded migration")
	}
	return []migration{{version: 1, sql: string(data)}}
}
func migrate(ctx context.Context, db *sql.DB, migrations []migration) error {
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS bridge_migration(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,applied_at_ms INTEGER NOT NULL)"); err != nil {
		return safe(err)
	}
	for i, m := range migrations {
		if m.version != i+1 {
			return ErrSchema
		}
	}
	for {
		applied, err := migrationStep(ctx, db, migrations)
		if err != nil {
			return err
		}
		if applied == len(migrations) {
			return nil
		}
	}
}
func migrationStep(ctx context.Context, db *sql.DB, migrations []migration) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, safe(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT version,checksum FROM bridge_migration ORDER BY version")
	if err != nil {
		return 0, ErrSchema
	}
	applied := 0
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return 0, ErrSchema
		}
		if version != applied+1 || version > len(migrations) || checksum != fmt.Sprintf("%x", sha256.Sum256([]byte(migrations[version-1].sql))) {
			rows.Close()
			return 0, ErrSchema
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, safe(err)
	}
	rows.Close()
	if applied < len(migrations) {
		m := migrations[applied]
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return 0, safe(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_migration(version,checksum,applied_at_ms) VALUES (?,?,?)", m.version, fmt.Sprintf("%x", sha256.Sum256([]byte(m.sql))), time.Now().UnixMilli()); err != nil {
			return 0, safe(err)
		}
		applied++
	}
	return applied, safe(tx.Commit())
}
