package bridge

import (
	"encoding/json"
	"testing"
)

// turnsOf pulls the turns array out of a (possibly nested) thread/read shape so
// tests can assert on the count and the boundary element after tailing.
func turnsOf(t *testing.T, raw json.RawMessage) []any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	holder := top
	if inner, ok := top["thread"].(map[string]any); ok {
		if _, has := inner["turns"]; has {
			holder = inner
		}
	}
	turns, _ := holder["turns"].([]any)
	return turns
}

func makeTurns(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += `{"id":` + itoa(i) + `}`
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func TestTailThread(t *testing.T) {
	t.Run("limit<=0 returns full untouched", func(t *testing.T) {
		raw := json.RawMessage(`{"turns":[` + makeTurns(5) + `]}`)
		out, trunc, total := tailThread(raw, 0)
		// total IS counted on a full read — the byte-bound loop shrinks from it.
		if trunc || total != 5 {
			t.Fatalf("want passthrough with total=5, got trunc=%v total=%d", trunc, total)
		}
		if string(out) != string(raw) {
			t.Fatalf("raw mutated: %s", out)
		}
	})

	t.Run("fewer turns than limit is not truncated", func(t *testing.T) {
		raw := json.RawMessage(`{"turns":[` + makeTurns(3) + `]}`)
		out, trunc, total := tailThread(raw, 40)
		if trunc {
			t.Fatalf("should not truncate when total<=limit")
		}
		if total != 3 {
			t.Fatalf("total=%d want 3", total)
		}
		if got := len(turnsOf(t, out)); got != 3 {
			t.Fatalf("turns=%d want 3", got)
		}
	})

	t.Run("keeps the LAST N turns and marks truncated", func(t *testing.T) {
		raw := json.RawMessage(`{"turns":[` + makeTurns(100) + `]}`)
		out, trunc, total := tailThread(raw, 40)
		if !trunc {
			t.Fatalf("want truncated")
		}
		if total != 100 {
			t.Fatalf("total=%d want 100", total)
		}
		turns := turnsOf(t, out)
		if len(turns) != 40 {
			t.Fatalf("kept %d turns want 40", len(turns))
		}
		// The first kept turn must be #60 (100-40), i.e. we keep the tail.
		first, _ := turns[0].(map[string]any)
		if first["id"].(float64) != 60 {
			t.Fatalf("first kept id=%v want 60", first["id"])
		}
	})

	t.Run("turns nested under thread", func(t *testing.T) {
		raw := json.RawMessage(`{"thread":{"id":"t1","turns":[` + makeTurns(50) + `]}}`)
		out, trunc, total := tailThread(raw, 10)
		if !trunc || total != 50 {
			t.Fatalf("trunc=%v total=%d want true/50", trunc, total)
		}
		turns := turnsOf(t, out)
		if len(turns) != 10 {
			t.Fatalf("kept %d want 10", len(turns))
		}
		// The thread wrapper (id) must survive the reshape.
		var top map[string]any
		_ = json.Unmarshal(out, &top)
		if top["thread"].(map[string]any)["id"] != "t1" {
			t.Fatalf("thread wrapper lost")
		}
	})

	t.Run("missing turns array falls back to full", func(t *testing.T) {
		raw := json.RawMessage(`{"thread":{"id":"t1"}}`)
		out, trunc, total := tailThread(raw, 10)
		if trunc || total != 0 {
			t.Fatalf("want passthrough on missing turns")
		}
		if string(out) != string(raw) {
			t.Fatalf("raw mutated")
		}
	})

	t.Run("invalid json falls back to full", func(t *testing.T) {
		raw := json.RawMessage(`not json`)
		out, trunc, _ := tailThread(raw, 10)
		if trunc || string(out) != string(raw) {
			t.Fatalf("want passthrough on bad json")
		}
	})
}

// Cursor paging: before=K + limit=N must return turns [K-N, K) with the offset
// reported, chaining all the way back to turn 0.
func TestWindowThreadPaging(t *testing.T) {
	raw := json.RawMessage(`{"thread":{"turns":[` + makeTurns(10) + `]}}`)

	// Open: newest tail of 4 → turns 6..9, offset 6.
	out, total, offset := windowThread(raw, 0, 4)
	if total != 10 || offset != 6 {
		t.Fatalf("open: total=%d offset=%d, want 10/6", total, offset)
	}
	turns := turnsOf(t, out)
	if len(turns) != 4 || turns[0].(map[string]any)["id"].(float64) != 6 {
		t.Fatalf("open window wrong: %v", turns)
	}

	// Page 1: before=6 → turns 2..5, offset 2.
	out, total, offset = windowThread(raw, 6, 4)
	if total != 10 || offset != 2 {
		t.Fatalf("page1: total=%d offset=%d, want 10/2", total, offset)
	}
	turns = turnsOf(t, out)
	if len(turns) != 4 || turns[0].(map[string]any)["id"].(float64) != 2 ||
		turns[3].(map[string]any)["id"].(float64) != 5 {
		t.Fatalf("page1 window wrong: %v", turns)
	}

	// Page 2: before=2 → turns 0..1, offset 0 (history exhausted).
	out, total, offset = windowThread(raw, 2, 4)
	if total != 10 || offset != 0 {
		t.Fatalf("page2: total=%d offset=%d, want 10/0", total, offset)
	}
	turns = turnsOf(t, out)
	if len(turns) != 2 || turns[0].(map[string]any)["id"].(float64) != 0 {
		t.Fatalf("page2 window wrong: %v", turns)
	}

	// Out-of-range before falls back to the newest window.
	_, _, offset = windowThread(raw, 99, 4)
	if offset != 6 {
		t.Fatalf("oob before: offset=%d, want 6", offset)
	}
}
