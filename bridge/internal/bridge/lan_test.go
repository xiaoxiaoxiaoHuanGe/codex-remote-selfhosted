package bridge

import (
	"io"
	"net"
	"net/http"
	"testing"
)

func ipnet(cidr string) net.Addr {
	ip, n, _ := net.ParseCIDR(cidr)
	n.IP = ip
	return n
}

func TestCandidatesFromAddrs(t *testing.T) {
	addrs := []net.Addr{
		ipnet("192.168.1.5/24"), // 私网 IPv4 → 保留
		ipnet("10.0.0.3/8"),     // 私网 IPv4 → 保留
		ipnet("8.8.8.8/32"),     // 公网 IPv4 → 丢弃
		ipnet("fe80::1/64"),     // IPv6 → 丢弃（v1 仅 IPv4）
		ipnet("127.0.0.1/8"),    // 环回 → 丢弃（IsPrivate=false 兜底）
	}
	got := candidatesFromAddrs(addrs, "8767")
	want := []string{"192.168.1.5:8767", "10.0.0.3:8767"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestStartLANListener(t *testing.T) {
	// "off" 显式关闭
	if got := StartLANListener(&Server{}, "off"); got != "" {
		t.Fatalf("off: want empty port, got %q", got)
	}
	// 正常监听：/healthz 可达（Handler 复用，不触发 ws 鉴权路径）
	s := &Server{tokens: map[string]string{"tok": "default"}, clients: map[*conn]struct{}{}, maxConn: 8}
	port := StartLANListener(s, "127.0.0.1:0")
	if port == "" {
		t.Fatal("want a bound port")
	}
	resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("healthz body %q", b)
	}
	// 端口冲突：同端口再监听 → 返回 ""（不 panic 不 fatal）
	if got := StartLANListener(s, "127.0.0.1:"+port); got != "" {
		t.Fatalf("conflict: want empty, got %q", got)
	}
}

func TestLanInfoFrame(t *testing.T) {
	s := &Server{lanCands: func(port string) []string {
		return []string{"192.168.1.5:" + port}
	}}
	// 未设 lanPort：候选必须为空（不发布假地址），pub 默认 false
	f := s.lanInfoFrame()
	if f["type"] != "lanInfo" {
		t.Fatalf("type = %v", f["type"])
	}
	if c := f["candidates"].([]string); len(c) != 0 {
		t.Fatalf("no lanPort: want empty candidates, got %v", c)
	}
	if f["pub"] != false {
		t.Fatalf("pub default: want false, got %v", f["pub"])
	}
	// 设置后：候选带端口，pub 翻转
	s.SetLANPort("8767")
	s.SetPubEnabled(true)
	f = s.lanInfoFrame()
	c := f["candidates"].([]string)
	if len(c) != 1 || c[0] != "192.168.1.5:8767" {
		t.Fatalf("candidates = %v", c)
	}
	if f["pub"] != true {
		t.Fatalf("pub = %v", f["pub"])
	}
}
