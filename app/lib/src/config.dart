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

/// Whether a value is a bare IPv4 host. LAN candidates still use plain `ws://`;
/// this helper is kept for callers that need to classify those candidates. A
/// public IP may still terminate TLS, so it must not force the relay URL to `ws`.
bool hostIsBareIp(String host) {
  final h = host.split(':').first; // drop an optional :port
  final parts = h.split('.');
  if (parts.length != 4) return false;
  return parts.every((s) {
    final n = int.tryParse(s);
    return n != null && n >= 0 && n <= 255;
  });
}

/// Build the phone's public WebSocket URL from a token and an address. The
/// address may be a bare host, `https://…`, or `wss://…`; public endpoints always
/// use `wss://`, including an IP whose TLS certificate is terminated upstream.
/// LAN direct candidates use [lanWsUrl] separately and remain plain `ws://`.
String wsUrlForToken(String token, {String? host}) {
  var raw = relayHostOr(host);
  if (!raw.contains('://')) raw = 'https://$raw';
  final u = Uri.parse(raw);
  if (u.host.isEmpty) throw const FormatException('连接地址缺少主机名');
  return Uri(
    scheme: 'wss',
    host: u.host,
    port: u.hasPort ? u.port : null,
    path: '/ws',
    queryParameters: {'token': token.trim()},
  ).toString();
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
String lanWsUrl(String token, String hostPort) {
  final u = Uri.parse('ws://$hostPort');
  return Uri(
    scheme: 'ws',
    host: u.host,
    port: u.hasPort ? u.port : null,
    path: '/ws',
    queryParameters: {'token': token.trim()},
  ).toString();
}

/// LAN direct-connect candidates from a scanned connect string (the comma-
/// separated `lan=` param the tray adds). Empty for old QRs / parse failures.
List<String> lanFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['lan'];
    if (v == null || v.isEmpty) return const [];
    return v
        .split(',')
        .map((e) => e.trim())
        .where((e) => e.isNotEmpty)
        .toList();
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
