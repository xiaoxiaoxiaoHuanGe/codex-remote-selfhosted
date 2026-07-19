package license

import (
	"database/sql"
	"errors"
	"time"
)

// MachineInfo is one active roster entry (deactivated rows stay in the db for
// audit but never surface here).
type MachineInfo struct {
	MachineID   string
	Name        string
	ActivatedAt time.Time
	LastSeen    time.Time // zero when the machine never registered
}

// AccountIDForMachine maps an ACTIVE machine id to its account — the roster
// API's auth pivot (phone proves it can reach machine X; X's account is what
// it may manage).
func (s *Service) AccountIDForMachine(machineID string) (int64, error) {
	var aid int64
	err := s.db.QueryRow(`SELECT account_id FROM machines
		WHERE machine_id=? AND deactivated_at IS NULL`, machineID).Scan(&aid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUnknownMachine
	}
	return aid, err
}

// Roster lists the account's active machines, oldest first.
func (s *Service) Roster(accountID int64) ([]MachineInfo, error) {
	rows, err := s.db.Query(`SELECT machine_id, name, activated_at, COALESCE(last_seen, 0)
		FROM machines WHERE account_id=? AND deactivated_at IS NULL
		ORDER BY activated_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineInfo
	for rows.Next() {
		var m MachineInfo
		var act, seen int64
		if err := rows.Scan(&m.MachineID, &m.Name, &act, &seen); err != nil {
			return nil, err
		}
		m.ActivatedAt = time.Unix(act, 0).UTC()
		if seen != 0 {
			m.LastSeen = time.Unix(seen, 0).UTC()
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Deactivate frees a roster slot (self-service). Rate-limited to SwapLimit per
// SwapWindow per account — the anti-key-sharing control; fingerprint-reuse
// reinstalls never pass through here so they don't burn swaps.
func (s *Service) Deactivate(accountID int64, machineID string) error {
	now := s.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var swaps int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM swap_events WHERE account_id=? AND ts>?`,
		accountID, now.Add(-SwapWindow).Unix()).Scan(&swaps); err != nil {
		return err
	}
	if swaps >= SwapLimit {
		return ErrSwapLimit
	}
	res, err := tx.Exec(`UPDATE machines SET deactivated_at=?
		WHERE account_id=? AND machine_id=? AND deactivated_at IS NULL`,
		now.Unix(), accountID, machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnknownMachine
	}
	if _, err := tx.Exec(`INSERT INTO swap_events(account_id, ts) VALUES(?,?)`,
		accountID, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminDeactivate is the operator override (lost machine, support case): frees
// the slot with no rate limit and no swap burned.
func (s *Service) AdminDeactivate(machineID string) error {
	res, err := s.db.Exec(`UPDATE machines SET deactivated_at=?
		WHERE machine_id=? AND deactivated_at IS NULL`,
		s.Now().UTC().Unix(), machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnknownMachine
	}
	return nil
}

// AdminMachineInfo is one row of the operator's GLOBAL machine list (web
// admin): the active roster entry plus which key (by display prefix) owns it.
type AdminMachineInfo struct {
	MachineID   string
	Name        string
	KeyPrefix   string
	ActivatedAt time.Time
	LastSeen    time.Time // zero when the machine never registered
	Blocked     bool      // blacklisted: CheckAgent refuses it despite a valid key
}

// ListMachines lists every active machine across all accounts, oldest first.
// Phase-1 invariant: IssueKey makes accounts and keys 1:1, so the join is flat.
func (s *Service) ListMachines() ([]AdminMachineInfo, error) {
	rows, err := s.db.Query(`
		SELECT m.machine_id, m.name, k.prefix, m.activated_at, COALESCE(m.last_seen, 0), m.blocked
		FROM machines m JOIN license_keys k ON k.account_id = m.account_id
		WHERE m.deactivated_at IS NULL
		ORDER BY m.activated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminMachineInfo
	for rows.Next() {
		var m AdminMachineInfo
		var act, seen int64
		var blocked int
		if err := rows.Scan(&m.MachineID, &m.Name, &m.KeyPrefix, &act, &seen, &blocked); err != nil {
			return nil, err
		}
		m.ActivatedAt = time.Unix(act, 0).UTC()
		if seen != 0 {
			m.LastSeen = time.Unix(seen, 0).UTC()
		}
		m.Blocked = blocked != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// BlockMachine blacklists an active machine: CheckAgent refuses it at the next
// register/recheck even with a valid credential and live key (operator override
// for a single abusive machine, without touching the key it activated under).
func (s *Service) BlockMachine(machineID string) error { return s.setBlocked(machineID, 1) }

// UnblockMachine lifts a blacklist; the machine may register again.
func (s *Service) UnblockMachine(machineID string) error { return s.setBlocked(machineID, 0) }

func (s *Service) setBlocked(machineID string, v int) error {
	res, err := s.db.Exec(`UPDATE machines SET blocked=?
		WHERE machine_id=? AND deactivated_at IS NULL`, v, machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnknownMachine
	}
	return nil
}
