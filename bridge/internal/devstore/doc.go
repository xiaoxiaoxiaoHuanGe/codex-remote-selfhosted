// Package devstore persists the bridge's paired-device registry (devices.json)
// and the one-time pending-pairings list (pending-pairings.json), both mode 0600.
//
// devices.json is a JSON array of Device{keyId,name,devicePub,account,psk,added}.
// The bridge loads it into an in-memory keyId->Device map at startup and serves
// lookups from memory; Watch hot-reloads it (a zero-dependency mtime poll) so that
// revoking a device — deleting its entry — takes effect on running connections.
// On a read/parse error the old in-memory map is KEPT and the error returned: the
// bridge audit-warns but never crashes and never silently drops paired devices.
//
// pending-pairings.json is a JSON array of Pending{pairingId,expiresAt}. The tray
// writes a fresh pairingId with a TTL; the bridge consumes it single-use during
// pairing. Consume is atomic — a pairingId is redeemable at most once.
package devstore
