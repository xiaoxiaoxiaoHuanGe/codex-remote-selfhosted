package license

import (
	"database/sql"
	"errors"
	"time"
)

// KeyInfo is one row of ListKeys — operator-facing, never contains the key
// itself (only its display prefix; the full key exists only at issue time).
type KeyInfo struct {
	Prefix    string
	Plan      string
	Limit     int
	Used      int
	ExpiresAt time.Time
	Revoked   bool
}

// IssueKey creates account + subscription + key in one shot (the phase-1
// "manual" path — a future payment webhook calls this same method) and returns
// the plaintext key ONCE. Only its hash is stored.
func (s *Service) IssueKey(plan string, machineLimit, months int) (string, error) {
	key, err := NewKey()
	if err != nil {
		return "", err
	}
	now := s.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`INSERT INTO accounts(created_at) VALUES(?)`, now.Unix())
	if err != nil {
		return "", err
	}
	aid, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO license_keys(key_hash, prefix, account_id, created_at) VALUES(?,?,?,?)`,
		hashSecret(key), key[:12], aid, now.Unix()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO subscriptions(account_id, plan, machine_limit, status, expires_at, source)
		 VALUES(?,?,?,'active',?,'manual')`,
		aid, plan, machineLimit, now.AddDate(0, months, 0).Unix()); err != nil {
		return "", err
	}
	return key, tx.Commit()
}

// ListKeys returns every key with its subscription and live machine count.
func (s *Service) ListKeys() ([]KeyInfo, error) {
	rows, err := s.db.Query(`
		SELECT k.prefix, s.plan, s.machine_limit, s.expires_at, k.revoked,
		       (SELECT COUNT(*) FROM machines m
		         WHERE m.account_id = k.account_id AND m.deactivated_at IS NULL)
		FROM license_keys k
		JOIN subscriptions s ON s.account_id = k.account_id
		ORDER BY k.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		var ki KeyInfo
		var exp int64
		var revoked int
		if err := rows.Scan(&ki.Prefix, &ki.Plan, &ki.Limit, &exp, &revoked, &ki.Used); err != nil {
			return nil, err
		}
		ki.ExpiresAt = time.Unix(exp, 0).UTC()
		ki.Revoked = revoked != 0
		out = append(out, ki)
	}
	return out, rows.Err()
}

// RevokeKey bars the key (and, via CheckAgent's live-key check, its machines
// at the next recheck). The roster is kept — un-revoke is a manual db edit in
// phase 1.
func (s *Service) RevokeKey(key string) error {
	res, err := s.db.Exec(`UPDATE license_keys SET revoked=1 WHERE key_hash=?`, hashSecret(key))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBadKey
	}
	return nil
}

// ExtendKey pushes expiry out by months from max(now, current expiry) and
// re-activates the subscription status.
func (s *Service) ExtendKey(key string, months int) error {
	sub, err := s.accountByKeyAnyState(key)
	if err != nil {
		return err
	}
	return s.extendAccount(sub.AccountID, sub.ExpiresAt, months)
}

// subRow is the joined key→account→subscription view used by lookups.
type subRow struct {
	AccountID    int64
	Plan         string
	MachineLimit int
	Status       string
	ExpiresAt    time.Time
	Revoked      bool
}

// accountByKeyAnyState resolves a key regardless of revocation (admin paths).
func (s *Service) accountByKeyAnyState(key string) (subRow, error) {
	var r subRow
	var exp int64
	var revoked int
	err := s.db.QueryRow(`
		SELECT k.account_id, k.revoked, s.plan, s.machine_limit, s.status, s.expires_at
		FROM license_keys k JOIN subscriptions s ON s.account_id = k.account_id
		WHERE k.key_hash = ?`, hashSecret(key)).
		Scan(&r.AccountID, &revoked, &r.Plan, &r.MachineLimit, &r.Status, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return subRow{}, ErrBadKey
	}
	if err != nil {
		return subRow{}, err
	}
	r.ExpiresAt = time.Unix(exp, 0).UTC()
	r.Revoked = revoked != 0
	return r, nil
}

// accountByKey is the user-facing variant: revoked keys are refused.
func (s *Service) accountByKey(key string) (subRow, error) {
	r, err := s.accountByKeyAnyState(key)
	if err != nil {
		return subRow{}, err
	}
	if r.Revoked {
		return subRow{}, ErrRevoked
	}
	return r, nil
}

// subByPrefix resolves a key row by display prefix — the web-admin path, where
// the plaintext key no longer exists anywhere. Ambiguity (two keys sharing a
// prefix) is refused; disambiguate with the full key via the CLI.
func (s *Service) subByPrefix(prefix string) (subRow, error) {
	rows, err := s.db.Query(`
		SELECT k.account_id, k.revoked, s.plan, s.machine_limit, s.status, s.expires_at
		FROM license_keys k JOIN subscriptions s ON s.account_id = k.account_id
		WHERE k.prefix = ?`, prefix)
	if err != nil {
		return subRow{}, err
	}
	defer rows.Close()
	var out []subRow
	for rows.Next() {
		var r subRow
		var exp int64
		var revoked int
		if err := rows.Scan(&r.AccountID, &revoked, &r.Plan, &r.MachineLimit, &r.Status, &exp); err != nil {
			return subRow{}, err
		}
		r.ExpiresAt = time.Unix(exp, 0).UTC()
		r.Revoked = revoked != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return subRow{}, err
	}
	switch len(out) {
	case 0:
		return subRow{}, ErrBadKey
	case 1:
		return out[0], nil
	default:
		return subRow{}, ErrPrefixAmbiguous
	}
}

// RevokeKeyByPrefix is RevokeKey for the web admin (see subByPrefix).
func (s *Service) RevokeKeyByPrefix(prefix string) error {
	if _, err := s.subByPrefix(prefix); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE license_keys SET revoked=1 WHERE prefix=?`, prefix)
	return err
}

// ExtendKeyByPrefix is ExtendKey for the web admin (see subByPrefix).
func (s *Service) ExtendKeyByPrefix(prefix string, months int) error {
	sub, err := s.subByPrefix(prefix)
	if err != nil {
		return err
	}
	return s.extendAccount(sub.AccountID, sub.ExpiresAt, months)
}

// extendAccount pushes expiry out by months from max(now, current expiry) and
// re-activates the subscription status. Shared by both Extend paths.
func (s *Service) extendAccount(accountID int64, cur time.Time, months int) error {
	base := cur
	if now := s.Now().UTC(); now.After(base) {
		base = now
	}
	_, err := s.db.Exec(
		`UPDATE subscriptions SET expires_at=?, status='active' WHERE account_id=?`,
		base.AddDate(0, months, 0).Unix(), accountID)
	return err
}

// DeleteKeyByPrefix HARD-deletes a key and everything under its account —
// subscription, full machine roster (active and deactivated), swap history,
// the account row — in one transaction. Unlike RevokeKey (which keeps rows for
// audit), this is irreversible. Returns the ids of the machines that were
// active, so the caller can kick their live links. Web-admin only.
func (s *Service) DeleteKeyByPrefix(prefix string) ([]string, error) {
	sub, err := s.subByPrefix(prefix)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Snapshot active machine ids BEFORE deletion (caller kicks their links).
	rows, err := tx.Query(`SELECT machine_id FROM machines
		WHERE account_id=? AND deactivated_at IS NULL`, sub.AccountID)
	if err != nil {
		return nil, err
	}
	var machines []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		machines = append(machines, id)
	}
	rows.Close() // must close before Exec on the same SQLite connection
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Children first, then the account (foreign-key order).
	for _, q := range []string{
		`DELETE FROM swap_events   WHERE account_id=?`,
		`DELETE FROM machines      WHERE account_id=?`,
		`DELETE FROM license_keys  WHERE account_id=?`,
		`DELETE FROM subscriptions WHERE account_id=?`,
		`DELETE FROM accounts      WHERE id=?`,
	} {
		if _, err := tx.Exec(q, sub.AccountID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return machines, nil
}
