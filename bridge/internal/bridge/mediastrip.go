package bridge

// Inline-media stripping for thread/read responses.
//
// Generated images ride inside the thread JSON as multi-hundred-KB base64
// strings, so an image-heavy session ships ~3MB through the relay before the
// phone can paint anything. stripInlineMedia swaps those blobs for absolute
// paths under a bridge-managed staging dir; the phone then lazy-loads each
// image via the existing /file channel only when it scrolls into view.
//
// SECURITY: this does not widen what can leave the box. The stripped bytes
// were already being sent to the phone inline; staging them re-routes the SAME
// bytes through /file, which still enforces the media-extension allowlist and
// the magic-byte sniff on every fetch. Unrecognizable bytes are never staged
// (they stay inline), so the cache contains genuine images only.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// stripThreshold: inline base64 below this stays inline — a tiny thumbnail
// isn't worth an extra /file round trip over the relay.
const stripThreshold = 16 << 10 // 16KB of base64 text

// mediaCacheDir returns (creating if needed) the staging dir for stripped
// media. It lives under the OS temp dir: contents are reproducible — the next
// read re-stages anything the OS cleaned up — so no GC is needed. mediaRoots()
// includes it in the /file allowlist.
func mediaCacheDir() string {
	d := filepath.Join(os.TempDir(), "codex-remote-mediacache")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(d); err == nil {
		return real
	}
	return d
}

// sniffImageExt maps leading bytes to an extension the /file allowlist accepts;
// "" means "not a recognizable image" and the caller must leave the data inline.
func sniffImageExt(b []byte) string {
	switch {
	case len(b) >= 4 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return ".png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return ".gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp"
	}
	return ""
}

// stripInlineMedia rewrites a thread/read result, swapping large inline base64
// images for staged absolute paths (imageGeneration.result → resultPath;
// data:image image_url/imageUrl → path). Fail-soft: any parse/marshal trouble
// returns the original payload untouched.
func stripInlineMedia(raw json.RawMessage, cacheDir string) json.RawMessage {
	if cacheDir == "" {
		return raw
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return raw
	}
	if stripNode(top, cacheDir) == 0 {
		return raw
	}
	out, err := json.Marshal(top)
	if err != nil {
		return raw
	}
	return out
}

// stripNode walks the decoded thread and performs the swaps in place,
// returning how many blobs were staged.
func stripNode(node any, cacheDir string) int {
	n := 0
	switch v := node.(type) {
	case map[string]any:
		if v["type"] == "imageGeneration" {
			if s, ok := v["result"].(string); ok && len(s) > stripThreshold {
				if p := stageB64(s, cacheDir); p != "" {
					v["result"] = ""
					v["resultPath"] = p
					n++
				}
			}
		}
		for _, key := range []string{"image_url", "imageUrl"} {
			if s, ok := v[key].(string); ok && len(s) > stripThreshold &&
				strings.HasPrefix(s, "data:image/") {
				if i := strings.IndexByte(s, ','); i > 0 {
					if p := stageB64(s[i+1:], cacheDir); p != "" {
						v[key] = p
						n++
					}
				}
			}
		}
		for _, child := range v {
			n += stripNode(child, cacheDir)
		}
	case []any:
		for _, child := range v {
			n += stripNode(child, cacheDir)
		}
	}
	return n
}

var b64Cleaner = strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "")

// stageB64 decodes base64 image data into cacheDir and returns its absolute
// path, or "" when the bytes aren't a recognizable image (leave inline — /file
// would refuse to serve them anyway) or staging fails. Content-hash filenames
// make repeated reads idempotent and dedupe identical images across sessions.
func stageB64(b64, cacheDir string) string {
	b, err := base64.StdEncoding.DecodeString(b64Cleaner.Replace(b64))
	if err != nil {
		return ""
	}
	ext := sniffImageExt(b)
	if ext == "" {
		return ""
	}
	sum := sha256.Sum256(b)
	p := filepath.Join(cacheDir, hex.EncodeToString(sum[:8])+ext)
	if fi, err := os.Stat(p); err == nil && fi.Size() == int64(len(b)) {
		return p // already staged
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return ""
	}
	return p
}
