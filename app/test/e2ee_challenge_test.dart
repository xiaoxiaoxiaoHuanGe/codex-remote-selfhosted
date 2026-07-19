import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:pinenacl/x25519.dart';

import 'package:codex_remote_app/src/services/e2ee/challenge.dart';
import 'package:codex_remote_app/src/services/e2ee/codec.dart';

/// Cross-language interop for the hub device-auth challenge: the "want*" constants
/// are produced by the Go oracle test e2ee.TestPrintChallengeVector (CHALVEC lines).
/// If these pass, the phone's NaCl-box open is byte-compatible with the hub's seal.
void main() {
  group('challenge vector (Go TestPrintChallengeVector: Go seal -> Dart open)', () {
    const ephPubB64 = 'IZ5NgA2paNKl/LAJx4T0dGxxOO257khEtznoMLBc9CQ=';
    const deviceSecB64 = 'd3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3c=';
    const sealedB64 =
        'iIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI6cI12fb06cgCM7UupaXcN2V9dGiKzYhsf6RwPj1855kdXtERLuB5NF4jMcMCOf9z';
    const nonceB64 = 'mZmZmZmZmZmZmZmZmZmZmZmZmZmZmZmZmZmZmZmZmZk=';

    test('opens a Go-sealed nonce with the device secret', () {
      final deviceSec = PrivateKey(base64.decode(deviceSecB64));
      final got = openChallenge(
          base64.decode(sealedB64), base64.decode(ephPubB64), deviceSec);
      expect(base64.encode(got), nonceB64);
    });

    test('the wrong device secret fails to open', () {
      final wrong = PrivateKey(Uint8List.fromList(List.filled(32, 0x12)));
      expect(
        () => openChallenge(base64.decode(sealedB64), base64.decode(ephPubB64), wrong),
        throwsA(isA<E2eeError>()),
      );
    });

    test('a truncated blob fails', () {
      final deviceSec = PrivateKey(base64.decode(deviceSecB64));
      final sealed = base64.decode(sealedB64);
      expect(
        () => openChallenge(Uint8List.fromList(sealed.sublist(0, 10)),
            base64.decode(ephPubB64), deviceSec),
        throwsA(isA<E2eeError>()),
      );
    });
  });

  group('challenge round-trip (Dart seal -> Dart open)', () {
    test('recovers the nonce', () {
      final eph = PrivateKey.generate();
      final device = PrivateKey.generate();
      final nonce = Uint8List.fromList(List.generate(32, (i) => 0x40 + i));
      final box = Box(myPrivateKey: eph, theirPublicKey: device.publicKey);
      final sealed = Uint8List.fromList(box.encrypt(nonce)); // bytes are nonce||box
      final got = openChallenge(
          sealed, Uint8List.fromList(eph.publicKey), device);
      expect(got, nonce);
    });
  });
}
