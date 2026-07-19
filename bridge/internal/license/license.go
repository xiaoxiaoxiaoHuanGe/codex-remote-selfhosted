// Package license is the hub's subscription control plane (phase 1: invite-only
// beta — keys are issued by the operator's admin CLI, no payments). It owns the
// SQLite-backed account/key/subscription/machine roster and answers three
// questions for the hub:
//
//   - Activate: may this license key bind a (new) machine, and with what
//     per-machine credential?
//   - CheckAgent: may this machine, presenting its credential, register right
//     now? (also re-asked hourly for long-lived links)
//   - Deactivate: free a roster slot — self-service, rate-limited to
//     SwapLimit/SwapWindow so a key can't be shared by rotating machines.
//
// Secrets discipline: license keys and machine credentials are stored as
// SHA-256 hex ONLY; the plaintext is shown once at issue/activate time and
// never again. Hash comparison goes through crypto/subtle.
//
// Fingerprint (fp_hint) is an opportunistic hardware hint, never a hard gate:
// a reinstall with a matching hint silently reuses its old slot (no swap
// burned); a mismatch just takes the normal slot path.
//
// Design doc: docs/superpowers/specs/2026-06-10-subscription-machine-binding-design.md
package license

import (
	"database/sql"
	"errors"
	"time"
)

var (
	ErrBadKey          = errors.New("license: invalid key")
	ErrRevoked         = errors.New("license: key revoked")
	ErrExpired         = errors.New("license: subscription expired")
	ErrRosterFull      = errors.New("license: machine limit reached")
	ErrMachineTaken    = errors.New("license: machine id already in use")
	ErrBadMachineID    = errors.New("license: bad machine id")
	ErrSwapLimit       = errors.New("license: swap limit reached")
	ErrUnknownMachine  = errors.New("license: unknown machine")
	ErrBadCredential   = errors.New("license: bad machine credential")
	ErrPrefixAmbiguous = errors.New("license: prefix matches multiple keys")
	ErrMachineBlocked  = errors.New("license: machine blacklisted")
)

const (
	// GracePeriod keeps an expired subscription's agents connectable while the
	// user renews; after it the hub refuses register (roster rows are kept).
	GracePeriod = 72 * time.Hour
	// SwapWindow/SwapLimit bound self-service deactivations — the anti-sharing
	// control (fingerprint reuse does NOT count against it).
	SwapWindow = 30 * 24 * time.Hour
	SwapLimit  = 3
)

// Service is the SQLite-backed control plane. Safe for concurrent use.
type Service struct {
	db *sql.DB
	// Now is injectable for expiry/grace/swap-window tests; defaults to time.Now.
	Now func() time.Time
}
