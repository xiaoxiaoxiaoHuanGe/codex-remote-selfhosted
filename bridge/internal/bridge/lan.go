// LAN direct-connect support: enumerating this machine's private IPv4
// addresses (the candidates pushed to phones via lanInfo and embedded in the
// tray QR's lan= param) and the optional LAN listener agent mode serves them
// on. The listener reuses Server.Handler() verbatim — token gate, turn/cwd/
// approval clamps and the /file allowlists all apply unchanged.
package bridge

import (
	"log"
	"net"
	"net/http"
	"time"
)

// candidatesFromAddrs filters interface addresses down to private (RFC1918)
// IPv4s and joins each with port. Pure — unit-tested directly.
func candidatesFromAddrs(addrs []net.Addr, port string) []string {
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := n.IP.To4()
		if ip4 == nil || !ip4.IsPrivate() {
			continue
		}
		out = append(out, net.JoinHostPort(ip4.String(), port))
	}
	return out
}

// LANCandidates returns "ip:port" for every private IPv4 on an up,
// non-loopback interface — the addresses a phone on the same LAN can reach
// this machine at. Enumerated per call: DHCP can change them mid-run.
func LANCandidates(port string) []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		out = append(out, candidatesFromAddrs(addrs, port)...)
	}
	return out
}

// StartLANListener serves s.Handler() on a LAN address (agent mode's direct
// path). addr "off" disables. Listen failure is NON-fatal — the agent keeps
// running relay-only and no candidates get published. Returns the bound port
// ("" when disabled/failed).
func StartLANListener(s *Server, addr string) string {
	if addr == "off" {
		return ""
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("lan: listen %s failed: %v — direct LAN disabled, relay only", addr, err)
		return ""
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return ""
	}
	hs := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := hs.Serve(ln); err != nil {
			log.Printf("lan: listener ended: %v", err)
		}
	}()
	log.Printf("lan: direct listener on %s", ln.Addr())
	return port
}

// SetLANPort publishes the LAN listener port lanInfo advertises ("" = none).
func (s *Server) SetLANPort(p string) { s.lanMu.Lock(); s.lanPort = p; s.lanMu.Unlock() }

// SetPubEnabled records whether the relay (public) tier is active.
func (s *Server) SetPubEnabled(b bool) { s.lanMu.Lock(); s.pubEnabled = b; s.lanMu.Unlock() }

// lanInfoFrame builds the lanInfo frame pushed to every fresh phone session:
// the LAN addresses the phone may dial directly + the relay-tier flag. An
// empty candidate list is authoritative — the phone clears its cache.
func (s *Server) lanInfoFrame() map[string]any {
	s.lanMu.Lock()
	port, pub, cands := s.lanPort, s.pubEnabled, s.lanCands
	s.lanMu.Unlock()
	list := []string{}
	if port != "" && cands != nil {
		list = append(list, cands(port)...)
	}
	return map[string]any{"type": "lanInfo", "candidates": list, "pub": pub}
}

// sendLanInfo pushes lanInfo to one fresh session. Called at the SAME moments
// as resyncTo (post-arm under E2EE, on connect otherwise) so it rides the
// "session can carry app frames" instant on both transports; via the hub it is
// an ordinary msg frame the relay forwards blindly.
func (s *Server) sendLanInfo(c *conn) { c.push(s.lanInfoFrame()) }
