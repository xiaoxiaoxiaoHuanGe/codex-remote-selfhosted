package license

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AgentState classifies a successful check; hard failures are errors instead.
type AgentState int

const (
	StateActive AgentState = iota
	StateGrace             // expired but inside GracePeriod — allowed + notice
)

// CheckResult is a successful CheckAgent verdict.
type CheckResult struct {
	State     AgentState
	AccountID int64
	Message   string // non-empty in grace: human renewal notice for tray/phone
}

// CheckAgent answers "may this machine register (or stay registered)?". Order
// matters for what we may reveal: credential is verified FIRST, so expiry /
// revocation details only ever reach the machine's legitimate owner.
func (s *Service) CheckAgent(machineID, credential string) (CheckResult, error) {
	var credHash string
	var aid int64
	var blocked int
	err := s.db.QueryRow(`SELECT cred_hash, account_id, blocked FROM machines
		WHERE machine_id=? AND deactivated_at IS NULL`, machineID).Scan(&credHash, &aid, &blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckResult{}, ErrUnknownMachine
	}
	if err != nil {
		return CheckResult{}, err
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(credential)), []byte(credHash)) != 1 {
		return CheckResult{}, ErrBadCredential
	}
	// Blacklist gate — checked after credential so a blocked verdict only ever
	// reaches the machine's legitimate owner, never an attacker probing ids.
	if blocked != 0 {
		return CheckResult{}, ErrMachineBlocked
	}
	var live int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM license_keys
		WHERE account_id=? AND revoked=0`, aid).Scan(&live); err != nil {
		return CheckResult{}, err
	}
	if live == 0 {
		return CheckResult{}, ErrRevoked
	}
	var status string
	var expUnix int64
	if err := s.db.QueryRow(`SELECT status, expires_at FROM subscriptions
		WHERE account_id=?`, aid).Scan(&status, &expUnix); err != nil {
		return CheckResult{}, err
	}
	if status != "active" {
		return CheckResult{}, ErrExpired
	}
	now := s.Now().UTC()
	exp := time.Unix(expUnix, 0).UTC()
	switch {
	case now.Before(exp):
		return CheckResult{State: StateActive, AccountID: aid}, nil
	case now.Before(exp.Add(GracePeriod)):
		return CheckResult{State: StateGrace, AccountID: aid,
			Message: fmt.Sprintf("subscription expired %s — relay access ends %s, renew to keep remote access",
				exp.Format("2006-01-02"), exp.Add(GracePeriod).Format("2006-01-02 15:04 UTC"))}, nil
	default:
		return CheckResult{}, ErrExpired
	}
}

// TouchLastSeen records liveness (best-effort; errors are deliberately dropped
// — a failed stamp must never break a register).
func (s *Service) TouchLastSeen(machineID string) {
	_, _ = s.db.Exec(`UPDATE machines SET last_seen=?
		WHERE machine_id=? AND deactivated_at IS NULL`,
		s.Now().UTC().Unix(), machineID)
}
