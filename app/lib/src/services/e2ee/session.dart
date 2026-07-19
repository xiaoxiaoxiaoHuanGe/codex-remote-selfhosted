import 'dart:convert';

import 'codec.dart';
import 'kdf.dart';

/// E2eeSession is the phone's per-connection codec state — the mirror of the
/// bridge conn's keyId + directional subkeys + seqIn/seqOut. The phone seals
/// phone->bridge frames (kP2B, dir p2b) and opens bridge->phone frames (kB2P, dir
/// b2p), each with its own strictly-monotonic sequence. A fresh session is built
/// per connection (seq restarts at 0 on both sides — the bridge does the same).
class E2eeSession {
  final String keyId;
  final List<int> kP2B;
  final List<int> kB2P;
  int _seqIn = 0;
  int _seqOut = 0;

  E2eeSession({required this.keyId, required this.kP2B, required this.kB2P});

  /// Build a session from the stored PSK by deriving both directional subkeys.
  static Future<E2eeSession> fromPsk(String keyId, List<int> psk) async {
    final ks = await deriveKeys(psk, keyId);
    return E2eeSession(keyId: keyId, kP2B: ks.kP2B, kB2P: ks.kB2P);
  }

  /// Seal an outbound message into the JSON envelope text to put on the wire.
  /// Must be called sequentially (seqOut is not lock-guarded) — BridgeClient
  /// serializes sends through a single chain.
  Future<String> sealText(Map<String, dynamic> m) async {
    final env = await seal(kP2B, keyId, E2eeDir.p2b, _seqOut, utf8.encode(jsonEncode(m)));
    _seqOut++;
    return jsonEncode(env.toJson());
  }

  /// Open an inbound wire frame into the decoded message map. Throws [E2eeError]
  /// on any decode/decrypt/seq failure — the caller drops the connection. Must be
  /// called sequentially (seqIn) — BridgeClient serializes inbound through a chain.
  Future<Map<String, dynamic>> openText(String raw) async {
    final decoded = jsonDecode(raw);
    if (decoded is! Map<String, dynamic>) throw const E2eeError('not an object');
    final env = Envelope.tryParse(decoded);
    if (env == null) throw const E2eeError('not an envelope');
    final pt = await open(kB2P, keyId, E2eeDir.b2p, _seqIn, env);
    _seqIn++;
    final msg = jsonDecode(utf8.decode(pt));
    if (msg is! Map<String, dynamic>) throw const E2eeError('bad plaintext');
    return msg;
  }
}
