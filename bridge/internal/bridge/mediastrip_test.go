package bridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fakePNG returns >16KB(b64) of bytes with a valid PNG magic so the sniff and
// the strip threshold both pass.
func fakePNG() []byte {
	b := make([]byte, 16<<10)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'})
	for i := 8; i < len(b); i++ {
		b[i] = byte(i % 251)
	}
	return b
}

func threadWith(t *testing.T, item map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"thread": map[string]any{"id": "t1", "turns": []any{
			map[string]any{"items": []any{item}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func firstItem(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	turns := top["thread"].(map[string]any)["turns"].([]any)
	return turns[0].(map[string]any)["items"].([]any)[0].(map[string]any)
}

func TestStripInlineMedia(t *testing.T) {
	dir := t.TempDir()
	png := fakePNG()
	b64 := base64.StdEncoding.EncodeToString(png)

	t.Run("imageGeneration result is staged and swapped for resultPath", func(t *testing.T) {
		raw := threadWith(t, map[string]any{"type": "imageGeneration", "result": b64})
		out := stripInlineMedia(raw, dir)
		it := firstItem(t, out)
		if it["result"] != "" {
			t.Fatalf("result should be emptied, got %d bytes", len(it["result"].(string)))
		}
		p, _ := it["resultPath"].(string)
		if p == "" || !strings.HasPrefix(p, dir) || !strings.HasSuffix(p, ".png") {
			t.Fatalf("bad resultPath %q", p)
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, png) {
			t.Fatalf("staged bytes mismatch (err=%v)", err)
		}
	})

	t.Run("data:image url is swapped for a path", func(t *testing.T) {
		raw := threadWith(t, map[string]any{
			"type": "image", "image_url": "data:image/png;base64," + b64,
		})
		out := stripInlineMedia(raw, dir)
		it := firstItem(t, out)
		u, _ := it["image_url"].(string)
		if !strings.HasPrefix(u, dir) {
			t.Fatalf("image_url not swapped: %.60q", u)
		}
	})

	t.Run("small inline images stay inline", func(t *testing.T) {
		small := base64.StdEncoding.EncodeToString(fakePNG()[:256])
		raw := threadWith(t, map[string]any{"type": "imageGeneration", "result": small})
		out := stripInlineMedia(raw, dir)
		if it := firstItem(t, out); it["result"] != small {
			t.Fatal("small result must stay inline")
		}
	})

	t.Run("non-image bytes are never staged", func(t *testing.T) {
		text := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("secret"), 4<<10))
		raw := threadWith(t, map[string]any{"type": "imageGeneration", "result": text})
		out := stripInlineMedia(raw, dir)
		it := firstItem(t, out)
		if it["result"] != text {
			t.Fatal("unrecognizable bytes must stay inline")
		}
		if _, has := it["resultPath"]; has {
			t.Fatal("no resultPath for unrecognizable bytes")
		}
	})

	t.Run("idempotent: same image stages to the same path", func(t *testing.T) {
		raw := threadWith(t, map[string]any{"type": "imageGeneration", "result": b64})
		p1, _ := firstItem(t, stripInlineMedia(raw, dir))["resultPath"].(string)
		p2, _ := firstItem(t, stripInlineMedia(raw, dir))["resultPath"].(string)
		if p1 == "" || p1 != p2 {
			t.Fatalf("want stable path, got %q / %q", p1, p2)
		}
	})

	t.Run("invalid json and empty cacheDir pass through", func(t *testing.T) {
		bad := json.RawMessage("not json")
		if out := stripInlineMedia(bad, dir); string(out) != "not json" {
			t.Fatal("bad json must pass through")
		}
		raw := threadWith(t, map[string]any{"type": "imageGeneration", "result": b64})
		if out := stripInlineMedia(raw, ""); string(out) != string(raw) {
			t.Fatal("empty cacheDir must disable stripping")
		}
	})
}
