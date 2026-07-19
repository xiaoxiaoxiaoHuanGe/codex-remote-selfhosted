package license

import (
	"database/sql"
	"regexp"
)

// machineIDRe mirrors what the hub/phone treat as a machine id (it lands in
// URLs and QR payloads): short, no spaces, no exotic characters.
var machineIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ActivateResult carries the one-time plaintext credential plus roster usage
// for the "bound N/limit" UX line.
type ActivateResult struct {
	Credential string
	MachineID  string
	Used       int
	Limit      int
	Reused     bool
}

// Activate binds a machine to the key's account: reuse the slot when the
// fingerprint matches an active machine of the SAME account (reinstall — no
// slot burned, no swap counted), otherwise take a free slot. The per-machine
// credential is (re)issued either way; activation requires an ACTIVE
// subscription (grace is for staying online, not for binding new machines).
func (s *Service) Activate(key, machineID, name, fpHint string) (ActivateResult, error) {
	if !ValidKeyFormat(key) {
		return ActivateResult{}, ErrBadKey
	}
	if !machineIDRe.MatchString(machineID) {
		return ActivateResult{}, ErrBadMachineID
	}
	sub, err := s.accountByKey(key)
	if err != nil {
		return ActivateResult{}, err
	}
	now := s.Now().UTC()
	if sub.Status != "active" || now.After(sub.ExpiresAt) {
		return ActivateResult{}, ErrExpired
	}
	cred, err := NewCredential()
	if err != nil {
		return ActivateResult{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ActivateResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Global id uniqueness: the hub routes by machine id across ALL accounts.
	// Exclude the row we may be about to reuse (same account + same fp).
	var clash int
	if fpHint != "" {
		err = tx.QueryRow(`SELECT COUNT(*) FROM machines
			WHERE machine_id=? AND deactivated_at IS NULL
			  AND NOT (account_id=? AND fp_hint=?)`,
			machineID, sub.AccountID, fpHint).Scan(&clash)
	} else {
		err = tx.QueryRow(`SELECT COUNT(*) FROM machines
			WHERE machine_id=? AND deactivated_at IS NULL`, machineID).Scan(&clash)
	}
	if err != nil {
		return ActivateResult{}, err
	}
	if clash > 0 {
		return ActivateResult{}, ErrMachineTaken
	}

	// Reinstall path: an active machine of this account with the same
	// fingerprint gets refreshed in place (id/name may change too).
	if fpHint != "" {
		res, err := tx.Exec(`UPDATE machines
			SET machine_id=?, name=?, cred_hash=?, activated_at=?
			WHERE account_id=? AND fp_hint=? AND deactivated_at IS NULL`,
			machineID, name, hashSecret(cred), now.Unix(), sub.AccountID, fpHint)
		if err != nil {
			return ActivateResult{}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			used, err := countActive(tx, sub.AccountID)
			if err != nil {
				return ActivateResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return ActivateResult{}, err
			}
			return ActivateResult{Credential: cred, MachineID: machineID,
				Used: used, Limit: sub.MachineLimit, Reused: true}, nil
		}
	}

	used, err := countActive(tx, sub.AccountID)
	if err != nil {
		return ActivateResult{}, err
	}
	if used >= sub.MachineLimit {
		return ActivateResult{}, ErrRosterFull
	}
	if _, err := tx.Exec(`INSERT INTO machines
		(account_id, machine_id, name, fp_hint, cred_hash, activated_at)
		VALUES(?,?,?,?,?,?)`,
		sub.AccountID, machineID, name, fpHint, hashSecret(cred), now.Unix()); err != nil {
		return ActivateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActivateResult{}, err
	}
	return ActivateResult{Credential: cred, MachineID: machineID,
		Used: used + 1, Limit: sub.MachineLimit, Reused: false}, nil
}

// rowQuerier lets countActive run inside or outside a transaction.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func countActive(q rowQuerier, accountID int64) (int, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM machines
		WHERE account_id=? AND deactivated_at IS NULL`, accountID).Scan(&n)
	return n, err
}
