import 'dart:convert';

/// A saved desktop machine the phone can connect to. The [token] is the per-machine
/// access token; [label] is a human name the user gives it (shown as a chip).
class Machine {
  final String id;
  final String label;
  final String token;

  /// Relay host this machine is reached through (e.g. `relay.example.com`).
  /// Empty = use the app default ([kRelayHost]). Lets different machines live on
  /// different relays — e.g. during a relay migration, or a self-hosted relay.
  final String host;

  /// This machine's hub requires device public-key auth (HUB_REQUIRE_DEVICE_AUTH):
  /// connect with ?keyId= and answer the hub's challenge instead of sending the
  /// bearer token. Captured from the paired connect string's `auth=device` flag.
  final bool requireDeviceAuth;

  /// Cached LAN direct-connect candidates ("ip:port"): seeded from the pairing
  /// QR's `lan=` param, refreshed by every `lanInfo` frame (empty list from a
  /// lanInfo CLEARS the cache — the bridge is authoritative).
  final List<String> lanCandidates;

  /// Whether this machine's relay (public) tier is active — QR `pub=` param /
  /// lanInfo. UX only (greys 仅公网); the hub enforces the tier server-side.
  final bool pubEnabled;

  /// User link-mode override: 'auto' (race LAN first, relay 300ms behind),
  /// 'lanOnly', or 'relayOnly'.
  final String linkMode;

  const Machine({
    required this.id,
    required this.label,
    required this.token,
    this.host = '',
    this.requireDeviceAuth = false,
    this.lanCandidates = const [],
    this.pubEnabled = true,
    this.linkMode = 'auto',
  });

  Machine copyWith(
          {String? label,
          String? host,
          bool? requireDeviceAuth,
          List<String>? lanCandidates,
          bool? pubEnabled,
          String? linkMode}) =>
      Machine(
        id: id,
        label: label ?? this.label,
        token: token,
        host: host ?? this.host,
        requireDeviceAuth: requireDeviceAuth ?? this.requireDeviceAuth,
        lanCandidates: lanCandidates ?? this.lanCandidates,
        pubEnabled: pubEnabled ?? this.pubEnabled,
        linkMode: linkMode ?? this.linkMode,
      );

  Map<String, dynamic> toJson() => {
        'id': id,
        'label': label,
        'token': token,
        if (host.isNotEmpty) 'host': host,
        if (requireDeviceAuth) 'auth': 'device',
        if (lanCandidates.isNotEmpty) 'lan': lanCandidates,
        if (!pubEnabled) 'pub': 0,
        if (linkMode != 'auto') 'link': linkMode,
      };

  factory Machine.fromJson(Map<String, dynamic> j) => Machine(
        id: (j['id'] as String?)?.trim().isNotEmpty == true
            ? j['id'] as String
            : (j['token'] as String? ?? ''),
        label: (j['label'] as String?)?.trim().isNotEmpty == true
            ? j['label'] as String
            : '我的电脑',
        token: (j['token'] as String?) ?? '',
        host: (j['host'] as String?)?.trim() ?? '',
        requireDeviceAuth: j['auth'] == 'device',
        lanCandidates:
            (j['lan'] as List?)?.whereType<String>().toList() ?? const [],
        pubEnabled: j['pub'] != 0,
        linkMode: (j['link'] as String?) ?? 'auto',
      );

  static String encodeList(List<Machine> m) =>
      jsonEncode(m.map((e) => e.toJson()).toList());

  static List<Machine> decodeList(String? s) {
    if (s == null || s.isEmpty) return const [];
    try {
      final l = jsonDecode(s);
      if (l is List) {
        return l
            .whereType<Map<String, dynamic>>()
            .map(Machine.fromJson)
            .where((e) => e.token.isNotEmpty)
            .toList();
      }
    } catch (_) {}
    return const [];
  }
}
