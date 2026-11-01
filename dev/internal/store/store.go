package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	dialect string
}

type Record struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Detail    string    `json:"detail"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

var Kinds = map[string]bool{
	"adapters": true, "servers": true, "players": true, "social-accounts": true,
	"groups": true, "whitelist": true, "blacklist": true, "commands": true,
	"ai-providers": true, "image-templates": true, "plugins": true, "settings": true,
	"sync-rules": true, "chat-images": true, "placeholders": true, "player-statistics": true, "skins": true,
}

func Open(dsn string) (*Store, error) {
	dialect, source := "sqlite", dsn
	if source == "" {
		source = os.Getenv("EASYBOT_SQLITE_PATH")
		if source == "" {
			source = "appdata/easybot.db"
		}
		if err := os.MkdirAll(filepath.Dir(source), 0750); err != nil {
			return nil, err
		}
		dialect = "sqlite"
	} else if strings.HasPrefix(source, "mysql://") {
		dialect = "mysql"
		parsed, err := url.Parse(source)
		if err != nil {
			return nil, err
		}
		password, _ := parsed.User.Password()
		source = fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true&charset=utf8mb4", parsed.User.Username(), password, parsed.Host, strings.TrimPrefix(parsed.Path, "/"))
	} else if strings.HasPrefix(source, "postgres://") || strings.HasPrefix(source, "postgresql://") {
		dialect = "postgres"
	} else if strings.Contains(source, "://") && !strings.HasPrefix(source, "file://") {
		return nil, fmt.Errorf("unsupported database URL scheme")
	}
	driver := map[string]string{"sqlite": "sqlite", "mysql": "mysql", "postgres": "pgx"}[dialect]
	db, err := sql.Open(driver, source)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err == nil {
		if dialect == "sqlite" {
			_, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL`)
			if err == nil {
				_, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`)
			}
		}
	}
	instance := &Store{db: db, dialect: dialect}
	if err == nil {
		err = instance.migrate(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return instance, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL)`); err != nil {
		return err
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return err
	}
	if version < 1 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS records (
			id VARCHAR(36) PRIMARY KEY, kind VARCHAR(48) NOT NULL, name VARCHAR(255) NOT NULL,
			detail TEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`); err == nil {
			_, err = tx.ExecContext(ctx, s.query(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`), 1, time.Now().UTC())
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		version = 1
	}
	if version < 2 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		statements := []string{
			`CREATE TABLE IF NOT EXISTS users (
				id VARCHAR(36) PRIMARY KEY, username VARCHAR(64) NOT NULL UNIQUE,
				password_hash VARCHAR(255) NOT NULL, created_at TIMESTAMP NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS sessions (
				token_hash VARCHAR(64) PRIMARY KEY, user_id VARCHAR(36) NOT NULL,
				created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP NOT NULL,
				revoked_at TIMESTAMP NULL, FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE)`,
			`CREATE TABLE IF NOT EXISTS setup_state (
				id INTEGER PRIMARY KEY, setup_complete BOOLEAN NOT NULL DEFAULT FALSE)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				tx.Rollback()
				return err
			}
		}
		seed := `INSERT INTO setup_state(id, setup_complete) VALUES(1, FALSE)`
		switch s.dialect {
		case "sqlite":
			seed = `INSERT OR IGNORE INTO setup_state(id, setup_complete) VALUES(1, FALSE)`
		case "mysql":
			seed = `INSERT IGNORE INTO setup_state(id, setup_complete) VALUES(1, FALSE)`
		case "postgres":
			seed = `INSERT INTO setup_state(id, setup_complete) VALUES(1, FALSE) ON CONFLICT(id) DO NOTHING`
		}
		if _, err = tx.ExecContext(ctx, seed); err == nil {
			_, err = tx.ExecContext(ctx, s.query(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`), 2, time.Now().UTC())
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return nil
}

type Admin struct {
	ID       string
	Username string
}

func (s *Store) SetupComplete(ctx context.Context) (bool, error) {
	var complete bool
	err := s.db.QueryRowContext(ctx, `SELECT setup_complete FROM setup_state WHERE id=1`).Scan(&complete)
	return complete, err
}

func (s *Store) CreateInitialAdmin(ctx context.Context, username, password, tokenHash string, expiresAt time.Time) (Admin, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Admin{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Admin{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.query(`UPDATE setup_state SET setup_complete=? WHERE id=? AND setup_complete=?`), true, 1, false)
	if err != nil {
		return Admin{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Admin{}, err
	}
	if changed != 1 {
		return Admin{}, errors.New("initial setup is already complete")
	}
	user := Admin{ID: uuid.NewString(), Username: username}
	if _, err = tx.ExecContext(ctx, s.query(`INSERT INTO users(id,username,password_hash,created_at) VALUES(?,?,?,?)`), user.ID, user.Username, string(hash), time.Now().UTC()); err != nil {
		return Admin{}, err
	}
	if _, err = tx.ExecContext(ctx, s.query(`INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)`), tokenHash, user.ID, time.Now().UTC(), expiresAt.UTC()); err != nil {
		return Admin{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admin{}, err
	}
	return user, nil
}

func (s *Store) AuthenticateAdmin(ctx context.Context, username, password string) (Admin, bool, error) {
	var user Admin
	var hash string
	err := s.db.QueryRowContext(ctx, s.query(`SELECT id,username,password_hash FROM users WHERE username=?`), username).Scan(&user.ID, &user.Username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, false, nil
	}
	if err != nil {
		return Admin{}, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return Admin{}, false, nil
	}
	return user, true, nil
}

func (s *Store) CreateSession(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, s.query(`INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES(?,?,?,?)`), tokenHash, userID, time.Now().UTC(), expiresAt.UTC())
	return err
}

func (s *Store) SessionAdmin(ctx context.Context, tokenHash string) (Admin, bool, error) {
	var user Admin
	err := s.db.QueryRowContext(ctx, s.query(`SELECT users.id,users.username FROM sessions JOIN users ON users.id=sessions.user_id WHERE sessions.token_hash=? AND sessions.revoked_at IS NULL AND sessions.expires_at>?`), tokenHash, time.Now().UTC()).Scan(&user.ID, &user.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, false, nil
	}
	if err != nil {
		return Admin{}, false, err
	}
	return user, true, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, s.query(`UPDATE sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`), time.Now().UTC(), tokenHash)
	return err
}

func (s *Store) Dialect() string { return s.dialect }
func (s *Store) Close() error    { return s.db.Close() }

func (s *Store) List(ctx context.Context, kind string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, s.query(`SELECT id,kind,name,detail,enabled,created_at,updated_at FROM records WHERE kind=? ORDER BY updated_at DESC`), kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Record, 0)
	for rows.Next() {
		var item Record
		if err := rows.Scan(&item.ID, &item.Kind, &item.Name, &item.Detail, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Save(ctx context.Context, item Record) (Record, error) {
	if !Kinds[item.Kind] {
		return item, errors.New("unsupported record kind")
	}
	if strings.TrimSpace(item.Name) == "" {
		return item, errors.New("name is required")
	}
	now := time.Now().UTC()
	if item.ID == "" {
		item.ID = uuid.NewString()
		item.CreatedAt, item.UpdatedAt = now, now
		_, err := s.db.ExecContext(ctx, s.query(`INSERT INTO records(id,kind,name,detail,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`), item.ID, item.Kind, item.Name, item.Detail, item.Enabled, item.CreatedAt, item.UpdatedAt)
		return item, err
	}
	item.UpdatedAt = now
	result, err := s.db.ExecContext(ctx, s.query(`UPDATE records SET name=?,detail=?,enabled=?,updated_at=? WHERE id=? AND kind=?`), item.Name, item.Detail, item.Enabled, item.UpdatedAt, item.ID, item.Kind)
	if err != nil {
		return item, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return item, err
	}
	if count == 0 {
		return item, sql.ErrNoRows
	}
	return item, nil
}

func (s *Store) Delete(ctx context.Context, kind, id string) error {
	result, err := s.db.ExecContext(ctx, s.query(`DELETE FROM records WHERE kind=? AND id=?`), kind, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) Count(ctx context.Context, kind string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.query(`SELECT COUNT(*) FROM records WHERE kind=?`), kind).Scan(&count)
	return count, err
}

func (s *Store) query(statement string) string {
	if s.dialect != "postgres" {
		return statement
	}
	var out strings.Builder
	arg := 1
	for _, ch := range statement {
		if ch == '?' {
			fmt.Fprintf(&out, "$%d", arg)
			arg++
		} else {
			out.WriteRune(ch)
		}
	}
	return out.String()
}
