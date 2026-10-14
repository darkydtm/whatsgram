package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"modernc.org/sqlite"
)

var (
	ErrSchema    = errors.New("unsupported or inconsistent storage schema")
	ErrStorage   = errors.New("storage failure")
	ErrBusy      = errors.New("storage busy")
	ErrNoWork    = errors.New("no eligible work")
	ErrLeaseLost = errors.New("storage lease lost")
	ErrSaturated = errors.New("durable queue saturated")
	ErrConflict  = domain.ErrConflict
	ErrInvalid   = domain.ErrInvalid
)

type Options struct {
	MaxInboxCount  int
	MaxInboxBytes  int64
	MaxOutboxCount int
	MaxOutboxBytes int64
}

func DefaultOptions() Options {
	return Options{MaxInboxCount: 10000, MaxInboxBytes: 64 << 20, MaxOutboxCount: 10000, MaxOutboxBytes: 64 << 20}
}

type Store struct {
	db        *sql.DB
	sessions  *sqlstore.Container
	options   Options
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, path string, busyTimeout time.Duration) (*Store, error) {
	return OpenWithOptions(ctx, path, busyTimeout, DefaultOptions())
}
func OpenWithOptions(ctx context.Context, path string, busyTimeout time.Duration, options Options) (*Store, error) {
	if path == "" || busyTimeout < time.Millisecond || busyTimeout > time.Minute || options.MaxInboxCount <= 0 || options.MaxInboxBytes <= 0 || options.MaxOutboxCount <= 0 || options.MaxOutboxBytes <= 0 {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	if err := privateParents(filepath.Dir(absolute)); err != nil {
		return nil, ErrStorage
	}
	file, err := os.OpenFile(absolute, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		info, e := os.Lstat(absolute)
		if e != nil || !info.Mode().IsRegular() {
			return nil, ErrStorage
		}
		if e := os.Chmod(absolute, 0600); e != nil {
			return nil, ErrStorage
		}
	} else if err != nil {
		return nil, ErrStorage
	} else {
		if err := file.Close(); err != nil {
			return nil, ErrStorage
		}
	}
	query := url.Values{"_pragma": {"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(" + strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")", "synchronous(FULL)"}, "_txlock": {"immediate"}}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, ErrStorage
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s := &Store{db: db, sessions: sqlstore.NewWithDB(db, "sqlite", nil), options: options}
	if err := db.PingContext(ctx); err != nil {
		s.Close()
		return nil, safe(err)
	}
	if err := migrate(ctx, db, bridgeMigrations()); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.sessions.Upgrade(ctx); err != nil {
		s.Close()
		return nil, safe(err)
	}
	for _, p := range []string{absolute, absolute + "-wal", absolute + "-shm"} {
		if err := os.Chmod(p, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.Close()
			return nil, ErrStorage
		}
	}
	return s, nil
}
func privateParents(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return ErrStorage
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	if err := privateParents(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
func (s *Store) SessionStore() *sqlstore.Container { return s.sessions }
func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closeErr = safe(s.sessions.Close()) })
	return s.closeErr
}
func safe(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{ErrSchema, ErrNoWork, ErrLeaseLost, ErrSaturated, ErrInvalid, ErrConflict, ErrBusy, ErrStorage, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, e) {
			return e
		}
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 255 {
		case 5, 6:
			return ErrBusy
		case 19:
			return ErrConflict
		}
	}
	return ErrStorage
}
