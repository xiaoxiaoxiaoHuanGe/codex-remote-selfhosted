package bridge

import (
	"testing"

	"github.com/yunyuchen/codex-remote/bridge/internal/appserver"
)

func TestModelCapabilitiesUsesAppServerIDsAndEfforts(t *testing.T) {
	cat, wire := modelCapabilities([]appserver.ModelInfo{
		{
			ID: "gpt-5.6-sol", Model: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol",
			IsDefault: true, DefaultReasoningEffort: "low",
			SupportedReasoningEfforts: []appserver.ReasoningEffortOption{
				{ReasoningEffort: "low"}, {ReasoningEffort: "max"}, {ReasoningEffort: "ultra"},
			},
		},
		{ID: "hidden", DisplayName: "Hidden", Hidden: true},
	})

	m, ok := cat["gpt-5.6-sol"]
	if !ok || m.Label != "GPT-5.6-Sol" || !m.Efforts["max"] || !m.Efforts["ultra"] {
		t.Fatalf("unexpected catalog: %+v", cat)
	}
	if _, ok := cat["hidden"]; ok || len(wire) != 1 {
		t.Fatalf("hidden model should not be exposed: catalog=%v wire=%v", cat, wire)
	}
}

func TestFallbackModelCapabilitiesKeepsLegacyIDs(t *testing.T) {
	cat := fallbackModelCapabilities()
	for _, id := range []string{"gpt-5.5", "gpt-5", "gpt-5-mini"} {
		if _, ok := cat[id]; !ok {
			t.Fatalf("missing fallback model %q", id)
		}
	}
}
