// codexhub is the multi-machine switchboard (runs on the relay server). Machines
// dial in via /agent and register; phones connect via /ws?machine=<id>&token=<t>.
// See internal/hub for the protocol. Config via env:
//
//	HUB_ADDR               listen address (default 127.0.0.1:8090; nginx fronts it)
//	HUB_AGENT_KEY          shared secret machines must present to register (required)
//	HUB_REQUIRE_DEVICE_AUTH "1" replaces the phone's bearer ?token= with a device
//	                       public-key challenge-response on /ws. Off by default; only
//	                       flip it once all machines run CODEX_E2EE=1 and all phone
//	                       clients speak the handshake.
//	HUB_LICENSE_MODE       "1" switches agent registration from the shared
//	                       HUB_AGENT_KEY to per-machine credentials backed by
//	                       the license db (see internal/license); also enables
//	                       /api/activate and /api/roster*.
//	HUB_DB                 license db path (default hub.db; license mode only)
//	HUB_ADMIN_ADDR         operator admin API listen address (e.g. 127.0.0.1:8768).
//	                       Empty (default) = admin API off. NEVER expose this
//	                       listener publicly — nginx must not proxy it; the RuoYi
//	                       admin backend on the same host is the only caller.
//	HUB_ADMIN_KEY          Bearer secret for the admin API (required when
//	                       HUB_ADMIN_ADDR is set)
//
// Admin subcommands (license-mode ops; run on the hub host, safe alongside a
// running hub thanks to SQLite WAL):
//
//	codexhub admin issue-key  [-plan beta] [-machines 3] [-months 6]
//	codexhub admin list-keys
//	codexhub admin revoke-key  crk_...
//	codexhub admin extend-key  -months 6 crk_...
//	codexhub admin unbind      <machine-id>
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/hub"
	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		adminMain(os.Args[2:])
		return
	}
	addr := os.Getenv("HUB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	agentKey := os.Getenv("HUB_AGENT_KEY")
	if agentKey == "" {
		fmt.Fprintln(os.Stderr, "FATAL: set HUB_AGENT_KEY (shared secret machines use to register)")
		os.Exit(1)
	}
	h := hub.New(agentKey)
	h.RequireDeviceAuth = os.Getenv("HUB_REQUIRE_DEVICE_AUTH") == "1"
	if h.RequireDeviceAuth {
		fmt.Println("codex-hub: device public-key auth REQUIRED (bearer ?token= disabled for /ws)")
	}
	if os.Getenv("HUB_LICENSE_MODE") == "1" {
		dbPath := os.Getenv("HUB_DB")
		if dbPath == "" {
			dbPath = "hub.db"
		}
		lic, err := license.Open(dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FATAL: license db:", err)
			os.Exit(1)
		}
		h.Lic = lic
		go h.RunRecheck(context.Background(), time.Hour)
		fmt.Printf("codex-hub: license mode ON (db=%s) — agents register with machine credentials\n", dbPath)
	}
	if adminAddr := os.Getenv("HUB_ADMIN_ADDR"); adminAddr != "" {
		adminKey := os.Getenv("HUB_ADMIN_KEY")
		if adminKey == "" {
			fmt.Fprintln(os.Stderr, "FATAL: HUB_ADMIN_ADDR is set but HUB_ADMIN_KEY is empty")
			os.Exit(1)
		}
		asrv := &http.Server{
			Addr:              adminAddr,
			Handler:           h.AdminHandler(adminKey),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			fmt.Printf("codex-hub admin api on %s (keep this loopback-only)\n", adminAddr)
			if err := asrv.ListenAndServe(); err != nil {
				fmt.Fprintln(os.Stderr, "hub admin:", err)
				os.Exit(1)
			}
		}()
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           h.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No IdleTimeout: agent/phone WebSockets are long-lived.
	}
	fmt.Printf("codex-hub listening on %s\n", addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "hub:", err)
		os.Exit(1)
	}
}
