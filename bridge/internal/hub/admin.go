// admin.go — the OPERATOR control API, consumed by the RuoYi admin backend
// (web 后台). Unlike api.go (client-facing license endpoints on the public
// listener), this handler is served from its OWN listener (HUB_ADMIN_ADDR —
// loopback in production, never proxied by nginx) and every request must carry
// a Bearer shared secret (HUB_ADMIN_KEY) compared in constant time. Plaintext
// license keys appear exactly once — in the issue response — and are never
// logged or stored.
//
//	POST /admin/keys            {plan,machineLimit,months} → {key} (once!)
//	GET  /admin/keys            → [{prefix,plan,limit,used,expiresAt,revoked}]
//	POST /admin/keys/revoke     {prefix}
//	POST /admin/keys/extend     {prefix,months}
//	GET  /admin/machines        → roster ⨝ live link state (license mode only)
//	POST /admin/machines/unbind {machineId} — frees the slot + kicks the link
//	GET  /admin/status          → live agents + hub runtime stats
package hub

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

// AdminHandler builds the operator API mux. adminKey must be non-empty — the
// caller (cmd/codexhub) refuses to start the listener otherwise.
func (h *Hub) AdminHandler(adminKey string) http.Handler {
	auth := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !ctEq(tok, adminKey) {
				writeAdminErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			fn(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/keys", auth(h.adminKeys))
	mux.HandleFunc("/admin/keys/revoke", auth(h.adminKeyRevoke))
	mux.HandleFunc("/admin/keys/extend", auth(h.adminKeyExtend))
	mux.HandleFunc("/admin/keys/delete", auth(h.adminKeyDelete))
	mux.HandleFunc("/admin/machines", auth(h.adminMachines))
	mux.HandleFunc("/admin/machines/unbind", auth(h.adminMachineUnbind))
	mux.HandleFunc("/admin/machines/block", auth(h.adminMachineBlock))
	mux.HandleFunc("/admin/machines/unblock", auth(h.adminMachineUnblock))
	mux.HandleFunc("/admin/status", auth(h.adminStatus))
	return mux
}

func writeAdminErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// licErrStatus maps license errors onto the admin API's HTTP codes.
func licErrStatus(err error) int {
	switch {
	case errors.Is(err, license.ErrBadKey), errors.Is(err, license.ErrUnknownMachine):
		return http.StatusNotFound
	case errors.Is(err, license.ErrPrefixAmbiguous):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// fmtT renders a timestamp for the JSON API; zero → "" (machine never seen).
func fmtT(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type adminAgentRow struct {
	MachineID   string `json:"machineId"`
	Name        string `json:"name"`
	KeyPrefix   string `json:"keyPrefix"`
	ConnectedAt string `json:"connectedAt"`
	Phones      int    `json:"phones"`
}

// liveAgentRows snapshots the connected agents. Agent pointers are collected
// under h.mu and their phone counts read under each a.mu AFTERWARDS — never
// nest the two locks.
func (h *Hub) liveAgentRows() []adminAgentRow {
	h.mu.Lock()
	agents := make([]*agent, 0, len(h.agents))
	for _, a := range h.agents {
		agents = append(agents, a)
	}
	h.mu.Unlock()

	rows := make([]adminAgentRow, 0, len(agents))
	for _, a := range agents {
		a.mu.Lock()
		phones := len(a.phones)
		a.mu.Unlock()
		rows = append(rows, adminAgentRow{
			MachineID:   a.id,
			Name:        a.name,
			ConnectedAt: fmtT(a.connectedAt),
			Phones:      phones,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MachineID < rows[j].MachineID })
	return rows
}

func (h *Hub) adminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rows := h.liveAgentRows()
	if h.Lic != nil {
		if ms, err := h.Lic.ListMachines(); err == nil {
			prefix := make(map[string]string, len(ms))
			for _, m := range ms {
				prefix[m.MachineID] = m.KeyPrefix
			}
			for i := range rows {
				rows[i].KeyPrefix = prefix[rows[i].MachineID]
			}
		}
	}
	phones := 0
	for _, a := range rows {
		phones += a.Phones
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	version := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
	writeJSON(w, map[string]any{
		"uptime":        int(time.Since(h.started).Seconds()),
		"version":       version,
		"goVersion":     runtime.Version(),
		"memMB":         mem.Alloc / (1 << 20),
		"agentsOnline":  len(rows),
		"phoneSessions": phones,
		"agents":        rows,
	})
}

type adminKeyRow struct {
	Prefix    string `json:"prefix"`
	Plan      string `json:"plan"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// adminKeys: GET = list, POST = issue. The issue response is the plaintext
// key's ONLY appearance — log the prefix, never the key.
func (h *Hub) adminKeys(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := h.Lic.ListKeys()
		if err != nil {
			writeAdminErr(w, licErrStatus(err), err.Error())
			return
		}
		rows := make([]adminKeyRow, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, adminKeyRow{
				Prefix: k.Prefix, Plan: k.Plan, Limit: k.Limit, Used: k.Used,
				ExpiresAt: fmtT(k.ExpiresAt), Revoked: k.Revoked,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var req struct {
			Plan         string `json:"plan"`
			MachineLimit int    `json:"machineLimit"`
			Months       int    `json:"months"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "bad request")
			return
		}
		if req.Plan == "" || req.MachineLimit < 1 || req.Months < 1 {
			writeAdminErr(w, http.StatusBadRequest, "plan/machineLimit/months required")
			return
		}
		key, err := h.Lic.IssueKey(req.Plan, req.MachineLimit, req.Months)
		if err != nil {
			writeAdminErr(w, licErrStatus(err), err.Error())
			return
		}
		log.Printf("hub admin: issued key prefix=%q plan=%q machines=%d months=%d",
			key[:12], req.Plan, req.MachineLimit, req.Months)
		writeJSON(w, map[string]string{"key": key})
	default:
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Hub) adminKeyRevoke(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Prefix == "" {
		writeAdminErr(w, http.StatusBadRequest, "prefix required")
		return
	}
	if err := h.Lic.RevokeKeyByPrefix(req.Prefix); err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	log.Printf("hub admin: revoked key prefix=%q", req.Prefix)
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Hub) adminKeyExtend(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Prefix string `json:"prefix"`
		Months int    `json:"months"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil ||
		req.Prefix == "" || req.Months < 1 {
		writeAdminErr(w, http.StatusBadRequest, "prefix/months required")
		return
	}
	if err := h.Lic.ExtendKeyByPrefix(req.Prefix, req.Months); err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	log.Printf("hub admin: extended key prefix=%q months=%d", req.Prefix, req.Months)
	writeJSON(w, map[string]bool{"ok": true})
}

type adminMachineRow struct {
	MachineID   string `json:"machineId"`
	Name        string `json:"name"`
	KeyPrefix   string `json:"keyPrefix"`
	ActivatedAt string `json:"activatedAt"`
	LastSeen    string `json:"lastSeen"`
	Online      bool   `json:"online"`
	Phones      int    `json:"phones"`
	Blocked     bool   `json:"blocked"`
}

// adminMachines merges the persistent roster with the live link table: a row
// is online iff its machineId currently holds an agent registration.
func (h *Hub) adminMachines(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodGet {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ms, err := h.Lic.ListMachines()
	if err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	live := make(map[string]int) // machineId → phone count
	for _, a := range h.liveAgentRows() {
		live[a.MachineID] = a.Phones
	}
	rows := make([]adminMachineRow, 0, len(ms))
	for _, m := range ms {
		phones, online := live[m.MachineID]
		rows = append(rows, adminMachineRow{
			MachineID: m.MachineID, Name: m.Name, KeyPrefix: m.KeyPrefix,
			ActivatedAt: fmtT(m.ActivatedAt), LastSeen: fmtT(m.LastSeen),
			Online: online, Phones: phones, Blocked: m.Blocked,
		})
	}
	writeJSON(w, rows)
}

func (h *Hub) adminMachineUnbind(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		MachineID string `json:"machineId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.MachineID == "" {
		writeAdminErr(w, http.StatusBadRequest, "machineId required")
		return
	}
	if err := h.Lic.AdminDeactivate(req.MachineID); err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	h.kickAgent(req.MachineID, "unbound by operator")
	log.Printf("hub admin: unbound machine=%q", req.MachineID)
	writeJSON(w, map[string]bool{"ok": true})
}

// adminKeyDelete hard-deletes a key + its whole account (irreversible), then
// kicks every machine that was live under it.
func (h *Hub) adminKeyDelete(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Prefix == "" {
		writeAdminErr(w, http.StatusBadRequest, "prefix required")
		return
	}
	machines, err := h.Lic.DeleteKeyByPrefix(req.Prefix)
	if err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	for _, mid := range machines {
		h.kickAgent(mid, "license deleted by operator")
	}
	log.Printf("hub admin: deleted key prefix=%q (kicked %d machines)", req.Prefix, len(machines))
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Hub) adminMachineBlock(w http.ResponseWriter, r *http.Request) {
	h.machineBlockToggle(w, r, true)
}

func (h *Hub) adminMachineUnblock(w http.ResponseWriter, r *http.Request) {
	h.machineBlockToggle(w, r, false)
}

// machineBlockToggle blacklists (block=true) or lifts the blacklist on a
// machine. Blocking also kicks its live link immediately.
func (h *Hub) machineBlockToggle(w http.ResponseWriter, r *http.Request, block bool) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		MachineID string `json:"machineId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.MachineID == "" {
		writeAdminErr(w, http.StatusBadRequest, "machineId required")
		return
	}
	var err error
	if block {
		err = h.Lic.BlockMachine(req.MachineID)
	} else {
		err = h.Lic.UnblockMachine(req.MachineID)
	}
	if err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	if block {
		h.kickAgent(req.MachineID, "blacklisted by operator")
		log.Printf("hub admin: blocked machine=%q", req.MachineID)
	} else {
		log.Printf("hub admin: unblocked machine=%q", req.MachineID)
	}
	writeJSON(w, map[string]bool{"ok": true})
}
