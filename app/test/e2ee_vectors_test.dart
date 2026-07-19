import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:pinenacl/x25519.dart';

import 'package:codex_remote_app/src/services/e2ee/codec.dart';
import 'package:codex_remote_app/src/services/e2ee/kdf.dart';
import 'package:codex_remote_app/src/services/e2ee/pairing.dart';

/// Cross-language interop: every "want*" constant below is produced by the Go
/// oracle tests (e2ee.TestPrintVector / TestPrintPairingVector). If these pass, the
/// Dart wire codec is byte-compatible with the bridge.
void main() {
  group('envelope codec vector (Go TestPrintVector)', () {
    const keyId = 'k_test_0001';
    const wantKP2B = 'xzZe2cT74u0vis/V13lowIQ1wQkfS5TqeFajpxlUp7Q=';
    const wantKB2P = 'ejCymRsfV/LmLdjsXZmiQWhS2OJBm1TDwL8V2CkxnU0=';
    const wantCT =
        'KXRW6Fyzo8XBB8ZyQBlMWZUbAcB59MSTRNcubrdpC2srm2U599pY3Dvv3YjWCFZyyIkQoro=';
    final plaintext = utf8.encode('{"type":"prompt","text":"hello e2ee"}');
    Uint8List vectorNonce() =>
        Uint8List.fromList(List.generate(24, (i) => 0xA0 + i));

    test('HKDF derives the Go-pinned subkeys', () async {
      final psk =
          Uint8List.fromList(List.generate(32, (i) => i + 1)); // 0x01..0x20
      final ks = await deriveKeys(psk, keyId);
      expect(base64.encode(ks.kP2B), wantKP2B);
      expect(base64.encode(ks.kB2P), wantKB2P);
    });

    test('seal matches the Go-pinned ciphertext', () async {
      final env = await sealWithNonce(base64.decode(wantKP2B), keyId,
          E2eeDir.p2b, 0, vectorNonce(), plaintext);
      expect(env.c, wantCT);
      expect(env.k, keyId);
      expect(env.s, 0);
    });

    test('open reverses the Go-pinned ciphertext', () async {
      final env = Envelope(keyId, 0, base64.encode(vectorNonce()), wantCT);
      final pt =
          await open(base64.decode(wantKP2B), keyId, E2eeDir.p2b, 0, env);
      expect(utf8.decode(pt), '{"type":"prompt","text":"hello e2ee"}');
    });
  });

  group('seal/open behavior', () {
    test('round-trip, then reject seq / keyId / tamper', () async {
      final key = Uint8List.fromList(List.filled(32, 7));
      final env =
          await seal(key, 'kid', E2eeDir.p2b, 3, utf8.encode('{"x":1}'));
      expect(
          utf8.decode(await open(key, 'kid', E2eeDir.p2b, 3, env)), '{"x":1}');
      expect(() => open(key, 'kid', E2eeDir.p2b, 4, env),
          throwsA(isA<E2eeError>()));
      expect(() => open(key, 'other', E2eeDir.p2b, 3, env),
          throwsA(isA<E2eeError>()));
      final raw = base64.decode(env.c);
      raw[0] ^= 0xFF;
      final bad = Envelope(env.k, env.s, env.n, base64.encode(raw));
      expect(() => open(key, 'kid', E2eeDir.p2b, 3, bad),
          throwsA(isA<E2eeError>()));
    });

    test('directions are independent (b2p key cannot open a p2b frame)',
        () async {
      final psk = Uint8List.fromList(List.generate(32, (i) => i + 1));
      final ks = await deriveKeys(psk, 'kid');
      final env = await seal(ks.kP2B, 'kid', E2eeDir.p2b, 0, utf8.encode('hi'));
      expect(() => open(ks.kB2P, 'kid', E2eeDir.p2b, 0, env),
          throwsA(isA<E2eeError>()));
    });
  });

  group('pairing vector (Go TestPrintPairingVector: Go seal -> Dart open)', () {
    test('opens a Go-sealed PairPayload', () {
      const enrollPubB64 = 'e06Qm75//kTEZaIgA31gjuNYl9Me+XLwf3SJLLD3PxM=';
      const deviceSecB64 = 'IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI=';
      const sealedB64 =
          'MzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzXPSuDmPC/AnN9T6dnKsA300+pfzHoIl+mpBwfOmVrzeiJwHqwCT9hxiy5fWlMaoBvO0GpYylDhWRU1NIns8CW5uUWuZe4xLhKxlQ2pstJUupgZ8y3qjyHxiKnGE=';
      final deviceSec = PrivateKey(base64.decode(deviceSecB64));
      final got = openPairing(
          base64.decode(sealedB64), base64.decode(enrollPubB64), deviceSec);
      expect(got.keyId, 'k_pair_0001');
      expect(got.psk, 'VVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVU=');
    });

    test('Dart pairing round-trips (seal -> open)', () {
      final enroll = PrivateKey.generate();
      final device = DeviceKeypair.generate();
      final box = Box(
          myPrivateKey: enroll, theirPublicKey: PublicKey(device.publicKey));
      final payload = utf8.encode(jsonEncode({
        'keyId': 'k1',
        'psk': base64.encode(Uint8List.fromList(List.filled(32, 9)))
      }));
      final enc = box.encrypt(payload);
      final sealed =
          Uint8List.fromList(enc); // EncryptedMessage bytes are nonce||box
      final got = openPairing(
          sealed, Uint8List.fromList(enroll.publicKey), device.secretKey);
      expect(got.keyId, 'k1');
    });
  });
}
