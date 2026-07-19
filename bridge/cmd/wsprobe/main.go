// wsprobe is a tiny stand-in for the phone: it connects to the bridge's
// WebSocket, lists sessions, sends a (downgraded, safe) prompt, and prints the
// streamed reply. Used to validate the serve layer end-to-end.
//
// Usage: wsprobe <bridge-log-path>
//
//	reads the log to discover ws://...token=..., then connects.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var urlRe = regexp.MustCompile(`ws://[^\s]*token=[0-9a-f]+`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wsprobe <bridge-log-path>")
		os.Exit(2)
	}
	arg := os.Args[1]

	// Direct-URL mode: if arg is a ws/wss URL, dial it and only list (no prompt).
	// Otherwise treat arg as a bridge log path and discover the local ws URL.
	var wsURL string
	listOnly := false
	if strings.HasPrefix(arg, "ws://") || strings.HasPrefix(arg, "wss://") {
		wsURL = arg
		listOnly = true
	} else {
		// Wait for the bridge to print its ws URL (Go sleep, not shell sleep).
		for i := 0; i < 30; i++ {
			if b, err := os.ReadFile(arg); err == nil {
				if m := urlRe.Find(b); m != nil {
					wsURL = string(m)
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if wsURL == "" {
		fmt.Fprintln(os.Stderr, "could not find ws url")
		os.Exit(1)
	}
	fmt.Println("dialing", wsURL)

	ctx := context.Background()
	var c *websocket.Conn
	var err error
	for i := 0; i < 20; i++ {
		c, _, err = websocket.Dial(ctx, wsURL, nil)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial failed:", err)
		os.Exit(1)
	}
	defer c.Close(websocket.StatusNormalClosure, "done")
	c.SetReadLimit(32 << 20) // match the phone: large frames (image echoes) are normal

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				fmt.Printf("\n[read error] %v (close=%v)\n", err, websocket.CloseStatus(err))
				return
			}
			if len(data) > 4096 {
				fmt.Printf("[large frame: %d bytes]\n", len(data))
			}
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m["type"] {
			case "sessions":
				arr, _ := m["data"].([]any)
				fmt.Printf("[sessions] %d\n", len(arr))
				for i, s := range arr {
					if i >= 3 {
						break
					}
					sm, _ := s.(map[string]any)
					fmt.Printf("   - %v  %v\n", sm["id"], sm["name"])
				}
			case "promptAccepted":
				fmt.Printf("[promptAccepted] thread=%v\nreply: ", m["threadId"])
			case "event":
				meth, _ := m["method"].(string)
				if strings.HasSuffix(meth, "agentMessage/delta") {
					if p, ok := m["params"].(map[string]any); ok {
						fmt.Print(p["delta"])
					}
				} else if meth == "turn/completed" {
					fmt.Println("\n[turn/completed]")
					return
				}
			case "error":
				fmt.Printf("[error] %v\n", m["message"])
			}
		}
	}()

	// 1) list sessions
	must(wsjson.Write(ctx, c, map[string]any{"type": "list"}))
	time.Sleep(1500 * time.Millisecond)

	if listOnly {
		// If IMG_DATA_URL is set, run a real image turn end-to-end (phone -> hub ->
		// agent -> codex with an image input item) and stream the reply.
		if imgURL := os.Getenv("IMG_DATA_URL"); imgURL != "" {
			must(wsjson.Write(ctx, c, map[string]any{
				"type":   "prompt",
				"text":   "In one word, what is the dominant color of this image?",
				"images": []string{imgURL},
			}))
			fmt.Println("[sent image prompt] reply: ")
			select {
			case <-done:
			case <-time.After(120 * time.Second):
				fmt.Println("\n[timeout]")
			}
			return
		}
		// Otherwise end-to-end reachability is proven; don't run a model turn.
		return
	}

	// 2) send a safe prompt (new ephemeral thread, read-only sandbox enforced by bridge)
	must(wsjson.Write(ctx, c, map[string]any{
		"type": "prompt",
		"text": "Reply with exactly the word: pong. Do not use any tools.",
		"cwd":  os.TempDir(),
	}))

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		fmt.Println("\n[timeout]")
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
