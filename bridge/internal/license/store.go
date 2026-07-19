package license

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, cross-compiles for the hub host
)

// Open opens (creating if needed) the license db and applies the schema.
// WAL + busy_timeout make cross-process access safe (admin CLI alongside hub).
func Open(path string) (*Service, error) {
	db, err := sql.Open("sqlite",
		"file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Service{db: db, Now: time.Now}, nil
}

func (s *Service) Close() error { return s.db.Close() }

// migrate applies the idempotent schema. Phase-1 simplifications: one
// subscription per account (UNIQUE), times are unix seconds UTC, machines are
// soft-deleted (deactivated_at) for audit.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS accounts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	email      TEXT UNIQUE,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS license_keys (
	key_hash   TEXT PRIMARY KEY,
	prefix     TEXT NOT NULL,
	account_id INTEGER NOT NULL REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS subscriptions (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id    INTEGER NOT NULL UNIQUE REFERENCES accounts(id),
	plan          TEXT NOT NULL,
	machine_limit INTEGER NOT NULL,
	status        TEXT NOT NULL DEFAULT 'active',
	expires_at    INTEGER NOT NULL,
	source        TEXT NOT NULL,
	external_ref  TEXT
);
CREATE TABLE IF NOT EXISTS machines (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id     INTEGER NOT NULL REFERENCES accounts(id),
	machine_id     TEXT NOT NULL,
	name           TEXT NOT NULL DEFAULT '',
	fp_hint        TEXT NOT NULL DEFAULT '',
	cred_hash      TEXT NOT NULL,
	activated_at   INTEGER NOT NULL,
	last_seen      INTEGER,
	deactivated_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_machines_active_id
	ON machines(machine_id) WHERE deactivated_at IS NULL;
CREATE TABLE IF NOT EXISTS swap_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id),
	ts         INTEGER NOT NULL
);`)
	if err != nil {
		return err
	}
	// Idempotent column add: upgrades an existing db to the machine-blacklist
	// schema. CREATE TABLE IF NOT EXISTS never alters an existing table, so a
	// new column needs an explicit, duplicate-safe ALTER.
	return ensureColumn(db, "machines", "blocked", "blocked INTEGER NOT NULL DEFAULT 0")
}

// ensureColumn adds `col` to `table` if absent (SQLite has no ADD COLUMN IF NOT
// EXISTS). table/col/ddl are package-internal constants, never user input.
func ensureColumn(db *sql.DB, table, col, ddl string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + ddl)
	return err
}
