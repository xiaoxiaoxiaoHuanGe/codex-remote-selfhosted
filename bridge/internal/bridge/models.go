package bridge

import (
	"sort"
	"strings"

	"github.com/yunyuchen/codex-remote/bridge/internal/appserver"
)

type modelCapability struct {
	ID            string
	Label         string
	Efforts       map[string]bool
	DefaultEffort string
	IsDefault     bool
}

// These are only a compatibility fallback for an older app-server/bridge. They
// are never used when model/list succeeds.
func fallbackModelCapabilities() map[string]modelCapability {
	return map[string]modelCapability{
		"gpt-5.5":    {ID: "gpt-5.5", Label: "GPT-5.5", Efforts: oldEfforts(), DefaultEffort: "medium"},
		"gpt-5":      {ID: "gpt-5", Label: "GPT-5", Efforts: oldEfforts(), DefaultEffort: "medium"},
		"gpt-5-mini": {ID: "gpt-5-mini", Label: "GPT-5 mini", Efforts: oldEfforts(), DefaultEffort: "medium"},
	}
}

func oldEfforts() map[string]bool {
	return map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true}
}

func modelCapabilities(models []appserver.ModelInfo) (map[string]modelCapability, []map[string]any) {
	cat := make(map[string]modelCapability)
	wire := make([]map[string]any, 0, len(models))
	for _, m := range models {
		if m.Hidden {
			continue
		}
		id := strings.TrimSpace(m.ID)
		if id == "" {
			id = strings.TrimSpace(m.Model)
		}
		if id == "" || cat[id].ID != "" {
			continue
		}
		label := strings.TrimSpace(m.DisplayName)
		if label == "" {
			label = id
		}
		efforts := make(map[string]bool)
		for _, e := range m.SupportedReasoningEfforts {
			if v := strings.TrimSpace(e.ReasoningEffort); v != "" {
				efforts[v] = true
			}
		}
		if len(efforts) == 0 {
			efforts = oldEfforts()
		}
		defaultEffort := strings.TrimSpace(m.DefaultReasoningEffort)
		if !efforts[defaultEffort] {
			defaultEffort = firstEffort(efforts)
		}
		cat[id] = modelCapability{ID: id, Label: label, Efforts: efforts, DefaultEffort: defaultEffort, IsDefault: m.IsDefault}
		wire = append(wire, map[string]any{
			"id": id, "label": label, "efforts": sortedKeys(efforts),
			"defaultEffort": defaultEffort, "default": m.IsDefault,
		})
	}
	return cat, wire
}

func firstEffort(e map[string]bool) string {
	for _, v := range []string{"low", "medium", "high", "xhigh", "max", "ultra", "minimal", "none"} {
		if e[v] {
			return v
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
