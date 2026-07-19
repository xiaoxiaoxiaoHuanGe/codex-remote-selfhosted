// activate.go — `codexbridge activate` binds this machine to a license key via
// the hub's POST /api/activate and stores the per-machine credential the agent
// registers with (license mode replaces the shared CODEX_AGENT_KEY). The
// credential is shown/written ONCE; the hub keeps only its hash.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/provision"
)

func runActivate(hubURL, key, id, name, out string) {
	if hubURL == "" || key == "" || id == "" {
		fatal("activate", fmt.Errorf("need -hub, -key and -id"))
	}
	if name == "" {
		name = id
	}
	base := strings.TrimSuffix(hubURL, "/")
	base = strings.Replace(base, "ws://", "http://", 1)
	base = strings.Replace(base, "wss://", "https://", 1)

	body, err := json.Marshal(map[string]string{
		"key": key, "machineId": id, "name": name,
		"fpHint": provision.FingerprintHint(),
	})
	if err != nil {
		fatal("activate", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(base+"/api/activate", "application/json", bytes.NewReader(body))
	if err != nil {
		fatal("activate", err)
	}
	defer resp.Body.Close()

	var res struct {
		Credential string `json:"credential"`
		Used       int    `json:"used"`
		Limit      int    `json:"limit"`
		Reused     bool   `json:"reused"`
		Error      string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		fatal("activate", fmt.Errorf("bad hub response (HTTP %d): %v", resp.StatusCode, err))
	}
	if resp.StatusCode != http.StatusOK {
		fatal("activate", fmt.Errorf("hub refused (HTTP %d): %s", resp.StatusCode, res.Error))
	}

	if out != "" {
		if err := os.WriteFile(out, []byte(res.Credential+"\n"), 0o600); err != nil {
			fatal("activate", err)
		}
		fmt.Printf("✓ 已激活 %q(%d/%d 台)— 凭证已写入 %s(只此一次,hub 仅存哈希)\n", id, res.Used, res.Limit, out)
	} else {
		fmt.Printf("✓ 已激活 %q(%d/%d 台)。机器凭证(只显示一次):\n%s\n", id, res.Used, res.Limit, res.Credential)
	}
	if res.Reused {
		fmt.Println("  (检测到同一台机器重装 — 复用了原名额,旧凭证已作废)")
	}
	wsBase := strings.Replace(strings.Replace(base, "http://", "ws://", 1), "https://", "wss://", 1)
	fmt.Printf("  启动: codexbridge agent -hub %s/agent -id %s -cred-file <凭证文件> -token <手机配对token>\n", wsBase, id)
}
