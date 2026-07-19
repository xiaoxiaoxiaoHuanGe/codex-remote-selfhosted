import 'dart:convert';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';

/// E2EE control-plane codec — the Dart mirror of `bridge/internal/e2ee/codec.go`.
///
/// Wire envelope (one JSON object per encrypted frame, short keys so the relay can
/// route but not read): `{ "k": keyId, "s": seq, "n": base64 24B nonce, "c": base64
/// ct||tag }`. AAD = ASCII "keyId|dir|seq". Primitive: XChaCha20-Poly1305. The byte
/// layout MUST match Go — proven by the cross-language vector in the codec test.

/// Any decode/decrypt failure. Callers map ALL of these to one action: drop the
/// connection (never fall through to an unverified frame).
class E2eeError implements Exception {
  final String message;
  const E2eeError(this.message);
  @override
  String toString() => 'E2eeError: $message';
}

/// Frame direction; part of the AAD and selects the subkey.
enum E2eeDir { p2b, b2p }

String _dirTag(E2eeDir d) => d == E2eeDir.b2p ? 'b2p' : 'p2b';

/// The wire form of one encrypted frame. JSON keys k,s,n,c byte-match the Go side.
class Envelope {
  final String k; // keyId (plaintext, selects the PSK)
  final int s; // seq, monotonic per direction
  final String n; // nonce, base64 std, 24 bytes
  final String c; // ciphertext||tag, base64 std
  const Envelope(this.k, this.s, this.n, this.c);

  Map<String, dynamic> toJson() => {'k': k, 's': s, 'n': n, 'c': c};

  /// Parse a decoded JSON map as an Envelope, or null if it isn't one (so the
  /// caller can distinguish a control frame / cleartext from an envelope).
  static Envelope? tryParse(Map<String, dynamic> m) {
    final k = m['k'], s = m['s'], n = m['n'], c = m['c'];
    if (k is String && k.isNotEmpty && s is num && n is String && c is String) {
      return Envelope(k, s.toInt(), n, c);
    }
    return null;
  }
}

Uint8List _aad(String keyId, E2eeDir dir, int seq) =>
    Uint8List.fromList(ascii.encode('$keyId|${_dirTag(dir)}|$seq'));

final _xchacha = Xchacha20.poly1305Aead();

/// seal encrypts [plaintext] under a 32-byte directional subkey with a fresh random
/// 24-byte nonce. AAD = "keyId|dir|seq". Mirrors Go e2ee.Seal.
Future<Envelope> seal(
  List<int> key,
  String keyId,
  E2eeDir dir,
  int seq,
  List<int> plaintext,
) async {
  final box = await _xchacha.encrypt(
    plaintext,
    secretKey: SecretKey(key),
    aad: _aad(keyId, dir, seq),
  );
  return _envelope(keyId, seq, box);
}

/// sealWithNonce is the deterministic-nonce variant used only to pin the
/// cross-language vector. Production code uses [seal] (random nonce).
Future<Envelope> sealWithNonce(
  List<int> key,
  String keyId,
  E2eeDir dir,
  int seq,
  List<int> nonce,
  List<int> plaintext,
) async {
  if (nonce.length != 24) throw const E2eeError('bad nonce length');
  final box = await _xchacha.encrypt(
    plaintext,
    secretKey: SecretKey(key),
    nonce: nonce,
    aad: _aad(keyId, dir, seq),
  );
  return _envelope(keyId, seq, box);
}

Envelope _envelope(String keyId, int seq, SecretBox box) {
  // Go appends the 16-byte Poly1305 tag to the ciphertext: c = ct||tag.
  final ct = Uint8List(box.cipherText.length + box.mac.bytes.length)
    ..setAll(0, box.cipherText)
    ..setAll(box.cipherText.length, box.mac.bytes);
  return Envelope(keyId, seq, base64.encode(box.nonce), base64.encode(ct));
}

/// open decrypts [env]. It verifies env.k == keyId and env.s == expectSeq (the
/// caller enforces monotonicity by passing the next expected seq), then opens the
/// AEAD. Every failure throws [E2eeError] — the caller disconnects, leaking no
/// distinction to the peer. Mirrors Go e2ee.Open.
Future<Uint8List> open(
  List<int> key,
  String keyId,
  E2eeDir dir,
  int expectSeq,
  Envelope env,
) async {
  if (env.k != keyId) throw const E2eeError('keyId mismatch');
  if (env.s != expectSeq) throw const E2eeError('seq mismatch');
  final nonce = base64.decode(env.n);
  if (nonce.length != 24) throw const E2eeError('bad nonce length');
  final raw = base64.decode(env.c);
  if (raw.length < 16) throw const E2eeError('auth failed');
  final ct = raw.sublist(0, raw.length - 16);
  final mac = raw.sublist(raw.length - 16);
  try {
    final pt = await _xchacha.decrypt(
      SecretBox(ct, nonce: nonce, mac: Mac(mac)),
      secretKey: SecretKey(key),
      aad: _aad(keyId, dir, env.s),
    );
    return Uint8List.fromList(pt);
  } catch (_) {
    throw const E2eeError('auth failed');
  }
}
