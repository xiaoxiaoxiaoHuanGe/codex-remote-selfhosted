package main

import (
	"net/url"
	"testing"
)

func TestPhoneURLPreservesConfiguredWSSForPublicIP(t *testing.T) {
	c := config{
		host:            "203.0.113.10:7446",
		token:           "tok ?&=",
		transportScheme: "wss",
	}
	u, err := url.Parse(c.phoneURL())
	if err != nil {
		t.Fatalf("phoneURL parse failed: %v", err)
	}
	if u.Scheme != "wss" || u.Host != "203.0.113.10:7446" || u.Path != "/ws" {
		t.Fatalf("phoneURL = %q, want wss://203.0.113.10:7446/ws", c.phoneURL())
	}
	if got := u.Query().Get("token"); got != c.token {
		t.Fatalf("decoded token = %q, want %q", got, c.token)
	}
}

func TestPhoneURLKeepsExplicitPlainWSForLegacyEndpoint(t *testing.T) {
	c := config{
		host:            "127.0.0.1:8767",
		token:           "local-token",
		transportScheme: "ws",
	}
	if got := c.phoneURL(); got != "ws://127.0.0.1:8767/ws?token=local-token" {
		t.Fatalf("phoneURL = %q, want explicit ws URL", got)
	}
}
