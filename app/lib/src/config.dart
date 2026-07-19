/// App-wide configuration.
///
/// [kRelayHost] is the DEFAULT relay; a machine may override it with its own
/// [Machine.host] (captured from the paired connect URL or entered manually),
/// so different machines can live on different relays.
/// Override the default at build time with --dart-define=RELAY_HOST=...
// Default relay: the Hong Kong box behind nginx + Let's Encrypt — a named host,
// so wsUrlForToken picks wss:// (full TLS on 443, no 备案 issue). The old
// Wenzhou high-port IP relay is decommissioned. Override at build time with
// --dart-define=RELAY_HOST=…
const String kRelayHost =
    String.fromEnvironment('RELAY_HOST', defaultValue: 'relay.example.com');

/// The effective relay host for a value that may be empty (→ the app default).
String relayHostOr(String? host) =>
    (host == null || host.trim().isEmpty) ? kRelayHost : host.trim();

/// A bare IPv4 host (optionally `ip:port`) has no TLS certificate, so it must be
/// reached over plain `ws://`; named hosts (domains) use `wss://`. This lets the app
/// talk to an IP-only relay before its domain + cert is set up, then auto-upgrades
/// to wss once a real host is used.
bool hostIsBareIp(String host) {
  final h = host.split(':').first; // drop an optional :port
  final parts = h.split('.');
  if (parts.length != 4) return false;
  return parts.every((s) {
    final n = int.tryParse(s);
    return n != null && n >= 0 && n <= 255;
  });
}

/// Build the phone's WebSocket URL from a token, optionally on a specific relay
/// host (empty/null → [kRelayHost]). Scheme is `wss://` for domains, `ws://` for a
/// bare IP (no cert possible there yet).
String wsUrlForToken(String token, {String? host}) {
  final h = relayHostOr(host);
  final scheme = hostIsBareIp(h) ? 'ws' : 'wss';
  return '$scheme://$h/ws?token=${token.trim()}';
}

/// Extract the token from a full ws URL (used to migrate older stored URLs).
String? tokenFromWsUrl(String url) {
  try {
    return Uri.parse(url).queryParameters['token'];
  } catch (_) {
    return null;
  }
}

/// Extract the one-time pairing id from a scanned connect string (the `pid` query
/// param the desktop tray adds in E2EE mode). Null if absent / unparseable.
String? pairingIdFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['pid'];
    return (v != null && v.isNotEmpty) ? v : null;
  } catch (_) {
    return null;
  }
}

/// Extract the bridge enrollment public key (base64) from a scanned connect string
/// (the `epub` query param). Null if absent / unparseable.
String? enrollPubFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['epub'];
    return (v != null && v.isNotEmpty) ? v : null;
  } catch (_) {
    return null;
  }
}

/// Whether a scanned connect string opts into hub device public-key auth (the
/// `auth=device` query param the tray adds when its hub runs HUB_REQUIRE_DEVICE_AUTH).
/// The phone then connects with ?keyId= and answers the hub challenge.
bool deviceAuthFromConnectString(String s) {
  try {
    return Uri.parse(s.trim()).queryParameters['auth'] == 'device';
  } catch (_) {
    return false;
  }
}

/// Extract the host from a full ws/wss URL (so a scanned/pasted connect string
/// pins the machine to the relay it actually came from). Null if not parseable
/// or hostless (a bare token).
String? hostFromWsUrl(String url) {
  try {
    // authority is "host" or "host:port" — keep an explicit (high) port, which a
    // mainland relay needs since 80/443 get blocked for un-备案 domains/IPs.
    final a = Uri.parse(url.trim()).authority;
    return a.isEmpty ? null : a;
  } catch (_) {
    return null;
  }
}

/// Build the direct-LAN ws URL for one candidate ("ip:port"). Always plain
/// ws:// — a LAN IP has no cert; the E2EE layer protects the payload.
String lanWsUrl(String token, String hostPort) =>
    'ws://$hostPort/ws?token=${token.trim()}';

/// LAN direct-connect candidates from a scanned connect string (the comma-
/// separated `lan=` param the tray adds). Empty for old QRs / parse failures.
List<String> lanFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['lan'];
    if (v == null || v.isEmpty) return const [];
    return v.split(',').map((e) => e.trim()).where((e) => e.isNotEmpty).toList();
  } catch (_) {
    return const [];
  }
}

/// Relay-tier flag from a connect string (`pub=0|1`). Null when absent (old
/// QR) — callers keep the Machine default (true).
bool? pubFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['pub'];
    if (v == null) return null;
    return v != '0';
  } catch (_) {
    return null;
  }
}
