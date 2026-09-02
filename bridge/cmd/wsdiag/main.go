// wsdiag reproduces "send a prompt on an EXISTING session" and dumps every
// message the bridge returns, to diagnose the stuck-thinking bug.
//
// Usage: wsdiag <ws-url>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func main() {
	url := os.Args[1]
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		fmt.Println("dial failed:", err)
		os.Exit(1)
	}
	defer c.Close(websocket.StatusNormalClosure, "done")
	c.SetReadLimit(32 << 20)

	firstID := make(chan string, 1)
	go func() {
		var sentOnce bool
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				fmt.Println("[read end]", err)
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			t, _ := m["type"].(string)
			switch t {
			case "sessions":
				arr, _ := m["data"].([]any)
				fmt.Printf("[sessions] %d\n", len(arr))
				if !sentOnce && len(arr) > 0 {
					if sm, ok := arr[0].(map[string]any); ok {
						id, _ := sm["id"].(string)
						fmt.Printf("   picked existing thread: %s (%v)\n", id, sm["name"])
						firstID <- id
						sentOnce = true
					}
				}
			case "event":
				meth, _ := m["method"].(string)
				p, _ := m["params"].(map[string]any)
				d, _ := p["delta"].(string)
				if d != "" {
					fmt.Printf("[event] %s delta=%q\n", meth, d)
				} else {
					fmt.Printf("[event] %s\n", meth)
				}
			case "error":
				fmt.Printf("[ERROR] %v\n", m["message"])
			default:
				fmt.Printf("[%s] %v\n", t, compact(m))
			}
		}
	}()

	must(wsjson.Write(ctx, c, map[string]any{"type": "list"}))

	select {
	case id := <-firstID:
		fmt.Println(">>> sending prompt on EXISTING thread", id)
		must(wsjson.Write(ctx, c, map[string]any{
			"type": "prompt", "threadId": id,
			"text": "Reply with exactly the word: pong. Do not use any tools.",
		}))
	case <-time.After(5 * time.Second):
		fmt.Println("no sessions arrived")
	}

	time.Sleep(30 * time.Second)
	fmt.Println("=== done ===")
}

func compact(m map[string]any) string {
	b, _ := json.Marshal(m)
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}

func must(err error) {
	if err != nil {
		fmt.Println("write err:", err)
		os.Exit(1)
	}
}
