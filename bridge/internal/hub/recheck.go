// recheck.go — periodic revalidation of connected agents against the license
// store. Register-time checks alone would let an expired/revoked/deactivated
// machine stay online for as long as its WebSocket survives; this loop bounds
// that exposure to one interval (default hourly — stricter than the spec's
// daily floor, so admin revocations land within the hour). Grace-window agents
// get a renewal notice each pass instead of a kick.
package hub

import (
	"context"
	"log"
	"time"
)

// RunRecheck blocks, revalidating every connected agent each interval. No-op
// (returns immediately) outside license mode.
func (h *Hub) RunRecheck(ctx context.Context, interval time.Duration) {
	if h.Lic == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.recheckOnce()
		}
	}
}

func (h *Hub) recheckOnce() {
	h.mu.Lock()
	agents := make([]*agent, 0, len(h.agents))
	for _, a := range h.agents {
		agents = append(agents, a)
	}
	h.mu.Unlock()
	for _, a := range agents {
		res, err := h.Lic.CheckAgent(a.id, a.cred)
		if err != nil {
			log.Printf("hub: recheck kicking id=%q: %v", a.id, err)
			// CloseNow unblocks the agent's read loop; its defer cleans up
			// phones + registration exactly like a normal disconnect.
			_ = a.ws.CloseNow()
			continue
		}
		h.Lic.TouchLastSeen(a.id)
		if res.Message != "" {
			_ = a.write(context.Background(), frame{T: "notice", Message: res.Message})
		}
	}
}
