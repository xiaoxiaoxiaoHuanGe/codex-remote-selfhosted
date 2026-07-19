// api.go — license-mode HTTP control endpoints. Every handler 404s when the
// hub runs without a license store (self-host mode), so the public surface is
// unchanged unless HUB_LICENSE_MODE is on.
//
//	POST /api/activate            {key, machineId, name, fpHint}
//	                              → {credential, used, limit, reused}
//	GET  /api/roster              auth like /file (legacy ?token= / device-auth
//	                              ?ticket=) → the caller's account's machines
//	POST /api/roster/deactivate   same auth + {machineId} → frees the slot,
//	                              kicks the agent (swap rate limit applies)
package hub

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

type activateReq struct {
	Key       string `json:"key"`
	MachineID string `json:"machineId"`
	Name      string `json:"name"`
	FpHint    string `json:"fpHint"`
}

func (h *Hub) handleActivate(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req activateReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	res, err := h.Lic.Activate(req.Key, req.MachineID, req.Name, req.FpHint)
	if err != nil {
		log.Printf("hub: activate DENIED machine=%q from %s: %v", req.MachineID, clientIP(r), err)
		writeLicErr(w, err)
		return
	}
	if res.Reused {
		// reinstall reissued the credential — drop any stale link so the fresh
		// install can register immediately instead of fighting a zombie.
		h.kickAgent(res.MachineID, "credential reissued")
	}
	log.Printf("hub: activated machine=%q (%d/%d) from %s", res.MachineID, res.Used, res.Limit, clientIP(r))
	writeJSON(w, map[string]any{
		"credential": res.Credential,
		"used":       res.Used,
		"limit":      res.Limit,
		"reused":     res.Reused,
	})
}

// rosterAccount resolves the caller to an account id using material a phone
// already holds: the legacy per-machine bearer token (machine must be online)
// or the device-auth /file session ticket. A caller can only ever reach its
// own account. Fallback when every machine is offline/lost: admin CLI.
func (h *Hub) rosterAccount(r *http.Request) (int64, bool) {
	var machineID string
	if h.RequireDeviceAuth {
		machineID = h.resolveFileTicket(r.URL.Query().Get("ticket"))
	} else if a := h.findAgent(r.URL.Query().Get("machine"), r.URL.Query().Get("token")); a != nil {
		machineID = a.id
	}
	if machineID == "" {
		return 0, false
	}
	aid, err := h.Lic.AccountIDForMachine(machineID)
	if err != nil {
		return 0, false
	}
	return aid, true
}

func (h *Hub) handleRoster(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	aid, ok := h.rosterAccount(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ms, err := h.Lic.Roster(aid)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	type entry struct {
		MachineID string `json:"machineId"`
		Name      string `json:"name"`
		Online    bool   `json:"online"`
		LastSeen  int64  `json:"lastSeen,omitempty"` // unix seconds, 0 = never
	}
	out := make([]entry, 0, len(ms))
	for _, m := range ms {
		var seen int64
		if !m.LastSeen.IsZero() {
			seen = m.LastSeen.Unix()
		}
		out = append(out, entry{m.MachineID, m.Name, h.agentByID(m.MachineID) != nil, seen})
	}
	writeJSON(w, out)
}

func (h *Hub) handleRosterDeactivate(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	aid, ok := h.rosterAccount(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		MachineID string `json:"machineId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.Lic.Deactivate(aid, req.MachineID); err != nil {
		writeLicErr(w, err)
		return
	}
	h.kickAgent(req.MachineID, "deactivated")
	log.Printf("hub: machine %q deactivated (account %d) from %s", req.MachineID, aid, clientIP(r))
	writeJSON(w, map[string]string{"status": "ok"})
}

// kickAgent drops a machine's live agent link, if any (deactivation, reissue).
// CloseNow, not Close: an eviction needs no close handshake, and Close would
// block the calling HTTP handler up to its handshake timeout when the peer
// isn't mid-Read. The agent's read loop still unblocks and cleans up normally.
func (h *Hub) kickAgent(machineID, reason string) {
	if a := h.agentByID(machineID); a != nil {
		log.Printf("hub: kicking agent id=%q (%s)", machineID, reason)
		_ = a.ws.CloseNow()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeLicErr maps license errors onto HTTP statuses with a JSON body the
// activation CLI / app can show verbatim.
func writeLicErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, license.ErrBadKey), errors.Is(err, license.ErrRevoked),
		errors.Is(err, license.ErrExpired), errors.Is(err, license.ErrBadCredential):
		code = http.StatusForbidden
	case errors.Is(err, license.ErrRosterFull), errors.Is(err, license.ErrMachineTaken):
		code = http.StatusConflict
	case errors.Is(err, license.ErrSwapLimit):
		code = http.StatusTooManyRequests
	case errors.Is(err, license.ErrUnknownMachine):
		code = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
