import 'dart:convert';

import 'package:pinenacl/x25519.dart';

import 'codec.dart' show E2eeError;

/// One-time pairing — the Dart mirror of `bridge/internal/e2ee/pairing.go` (the
/// phone's OPEN side). The bridge seals the per-device PSK to the phone's public
/// key with a NaCl box; the phone unseals it with its secret key + the bridge
/// enrollment public key. The sealed blob is framed nonce||box, matching Go.

/// The phone's Curve25519 keypair. Keep [secretKey]; send [publicKey] in pair_init.
class DeviceKeypair {
  final PrivateKey secretKey;
  DeviceKeypair(this.secretKey);
  factory DeviceKeypair.generate() => DeviceKeypair(PrivateKey.generate());

  Uint8List get publicKey => Uint8List.fromList(secretKey.publicKey);
}

/// The secret the bridge delivers during pairing.
class PairPayload {
  final String keyId;
  final String psk; // base64 std, 32 bytes
  const PairPayload(this.keyId, this.psk);
}

/// openPairing reverses the bridge's SealPairing: it splits nonce||box and opens
/// the box with the bridge enrollment PUBLIC key + this device's SECRET key. Any
/// failure (bad length, auth failure, malformed JSON) throws [E2eeError].
PairPayload openPairing(Uint8List sealed, Uint8List enrollPub, PrivateKey deviceSec) {
  if (sealed.length < 24) throw const E2eeError('pairing open failed');
  final nonce = Uint8List.fromList(sealed.sublist(0, 24));
  final cipher = Uint8List.fromList(sealed.sublist(24));
  try {
    final box = Box(myPrivateKey: deviceSec, theirPublicKey: PublicKey(enrollPub));
    final pt = box.decrypt(EncryptedMessage(nonce: nonce, cipherText: cipher));
    final m = jsonDecode(utf8.decode(pt)) as Map<String, dynamic>;
    final keyId = m['keyId'], psk = m['psk'];
    if (keyId is! String || psk is! String) {
      throw const E2eeError('pairing open failed');
    }
    return PairPayload(keyId, psk);
  } on E2eeError {
    rethrow;
  } catch (_) {
    throw const E2eeError('pairing open failed');
  }
}
