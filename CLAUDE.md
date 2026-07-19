# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

Self-hosted remote control for a desktop **Codex**. A phone app drives the Codex
running on your Mac/PC by attaching a *second* `codex app-server` that **shares
`~/.codex` on disk** with the desktop app — so it inherits the same ChatGPT
auth + model with zero extra API cost. The relay link is self-hosted (frp +
nginx, or the multi-machine `codexhub`), so it sidesteps OpenAI's account/cloud
restrictions. Background and rationale live in `FINDINGS.md` and `README.md`.

## Repository layout (multi-language monorepo)

| Dir | Language | Role |
|---|---|---|
| `bridge/` | Go (1.26) | Desktop bridge, hub, menubar tray — the core. |
| `app/` | Flutter/Dart | Phone app "Caret" (iOS-first; Android buildable). |
| `miniprogram/` | WXML/WXSS/JS | WeChat mini-program; a *second* front-end speaking the same relay protocol. No build step. |
| `protocol/` | JSON Schema | Codex app-server protocol schemas (vendored, generated). |
| `scripts/` | Python | Phase-0 read-only probes against a real app-server. |
| `website/`, `docs/` | — | Landing page + Chinese design/audit docs. |

## Build / run / test

### Bridge (Go) — `cd bridge`
```bash
go test ./...                          # all tests
go test ./internal/bridge/ -run TestClampDecision   # one test
go build ./cmd/...                     # build every binary
go vet ./...

# run the bridge locally (token REQUIRED — empty token refuses to start):
CODEX_BRIDGE_TOKEN=devtoken go run ./cmd/codexbridge serve -addr 127.0.0.1:8767
go run ./cmd/codexbridge serve -addr 127.0.0.1:8767 -dev-random-token  # local-only convenience
go run ./cmd/codexbridge list                 # read-only: list sessions
go run ./cmd/codexbridge read <THREAD_ID>     # read-only: dump one session
```
`-codex` flag points at the codex binary (default
`/Applications/Codex.app/Contents/Resources/codex`).

The `bridge/cmd/` binaries: `codexbridge` (the bridge), `codexhub` (multi-machine
switchboard, runs on the relay), `codexmenubar` (cross-platform tray companion),
`wsprobe`/`wsdiag` (WebSocket test harnesses).

### App (Flutter) — `cd app`
```bash
flutter pub get
flutter run -d <device> --dart-define=WS_URL='ws://127.0.0.1:8767/ws?token=devtoken'
flutter test
```
Distributable APK (see the warning below):
```bash
./build-apk-release.sh [output-dir]     # → ./dist/caret-<ts>.apk
```

### Mini-program
Open `miniprogram/` directly in WeChat DevTools — no build. See `miniprogram/README.md`.

## Architecture — how a phone reaches Codex

```
Phone ──wss/TLS──> nginx(:443) ──> codexhub  ──/agent ws──> codexbridge ──JSON-RPC/stdio──> codex app-server (shares ~/.codex)
                                  (relay)                   (your machine)
```
Two link modes, both speaking the **same phone protocol** (text WS frames, JSON
objects with a `"type"` field):
- **Direct/LAN**: phone → `codexbridge` `/ws` directly.
- **Hub/relay**: each machine's bridge dials the hub's `/agent` and registers a
  `machineId`+token; phones connect `/ws?machine=<id>&token=<t>`. The hub
  multiplexes many phone sessions over one agent WS, tagged by SID, and never
  interprets the phone protocol (except proxying `/file`). Adding a machine =
  running an agent; no per-machine DNS/port/nginx.

Key Go files (read the package doc-comment headers first — they are the spec):
- `internal/bridge/server.go` — holds ONE `appserver.Client`, fans its
  notifications out to all phone clients, defines the **phone protocol** and the
  **security model** (top-of-file comment is authoritative).
- `internal/bridge/agent.go` — agent-mode: dials the hub, the `hubFrame` envelope, `/file` proxy.
- `internal/hub/hub.go` — the switchboard (mirror `frame` envelope).
- `internal/appserver/{client,methods,protocol}.go` — JSON-RPC client that
  spawns and drives the `codex app-server` child.
- `internal/provision/` — per-OS machine setup/reset (build-tagged `_darwin`/`_windows`/`_other`).

The Flutter `bridge_client.dart` (`app/lib/src/services/`) is the client mirror of
this protocol; `relay.js` (`miniprogram/utils/`) is the mini-program mirror. **When
you change the wire protocol, update all three** plus the `server.go` header comment.

## Security model — DO NOT WEAKEN

This bridge can be exposed to the public internet, so the server-side clamps are
load-bearing. The full rationale is the `server.go` package comment; the
invariants:
- **Auth**: every `/ws` and `/file` is gated by a pairing token, compared in
  constant time (SHA-256/`subtle`). Empty token rejects everything. Multiple
  named device tokens are supported so a lost device can be revoked individually.
- **Turn clamp**: phone-initiated turns are pinned to a workspace-write sandbox
  with `networkAccess:false` regardless of what the phone asks;
  `danger-full-access`/`never` are refused. `auto_review` requires operator opt-in
  `CODEX_ALLOW_AUTO_REVIEW=1`, else downgraded to a human gate.
- **cwd clamp** (`validateCwd`): a phone-supplied cwd must be absolute, existing,
  inside `CODEX_WORKSPACE_ROOTS` (default `$HOME`), and NOT the home root or a
  sensitive subtree (`.ssh`/`.aws`/`.codex`/…).
- **Approval clamp**: accept/decline is mapped to the exact protocol decision;
  persistent variants (`acceptForSession`/`approved_for_session`/`*_amendment`)
  are NEVER forwarded — one approval can't disable the per-command gate.
- **Approval-ownership binding**: an approval is answerable only by the device
  whose turn triggered it (meaningful in direct LAN mode; in hub mode all
  sessions share `device="phone"`).
- **`/file`**: token-gated, restricted to a media-root allowlist, AND gated by
  both an extension allowlist and a magic-byte content sniff. The media-root
  allowlist MIRRORS the cwd workspace roots (so it follows `CODEX_WORKSPACE_ROOTS`,
  default `$HOME`) plus the `~/.codex` media output dirs — a single knob governs
  both what the phone can operate on and what images it can see. The ext+magic
  gates are the load-bearing exfil control, independent of how wide the roots are.

Relevant env vars: `CODEX_BRIDGE_TOKEN`(S), `CODEX_MACHINE_TOKEN`(`_FILE`),
`CODEX_AGENT_KEY`(`_FILE`), `CODEX_MACHINE_CRED`(`_FILE`), `CODEX_WORKSPACE_ROOTS`,
`CODEX_MEDIA_ROOTS`, `CODEX_ALLOW_AUTO_REVIEW`, `CODEX_LAN_ADDR` (agent-mode LAN
direct listener, default `:8767`, `off` disables); hub: `HUB_ADDR`,
`HUB_AGENT_KEY`, `HUB_LICENSE_MODE`, `HUB_DB`.

### License mode (subscription beta)
`HUB_LICENSE_MODE=1` switches hub agent registration from the shared
`HUB_AGENT_KEY` to per-machine credentials minted by `codexbridge activate`
against a license key (`codexhub admin issue-key`). Machine-limit roster,
self-service unbind (rate-limited), 72h expiry grace. See the `internal/license`
package comment and
`docs/superpowers/specs/2026-06-10-subscription-machine-binding-design.md`.

### ⚠️ Never bake a token into a distributable build
The app reads its connection (machine + token) from the on-device keychain after
the user scans the desktop tray's QR. `build-apk-release.sh` deliberately does
**not** pass `--dart-define=TOKEN`, and has a safety net that refuses to emit an
APK containing the local machine token. Only add `--dart-define=TOKEN=...` for a
private personal build, and never share that file.

## Deployment

`bridge/deploy/` is the public-relay kit: frps/frpc TOML, nginx subdomain
reverse-proxy, launchd/systemd units, Windows NSIS installer, and one-shot
installers (`install-mac.sh`, `install-agent-*`, `install-menubar-*`). Bilingual
walkthrough in `bridge/deploy/DEPLOY.md`; multi-machine hub in
`bridge/deploy/HUB.md`. `deploy/secrets*.env` are gitignored.

## Conventions

- Each Go package's behavior + protocol is documented in its **top-of-file
  package comment** — treat those as the source of truth and keep them in sync
  with code changes.
- The relay host migrated to `relay.example.com`; per-machine relay host is now
  client-selectable (recent commits).
