import 'package:pinenacl/x25519.dart';

import 'codec.dart' show E2eeError;

/// Device public-key challenge — the Dart mirror of `bridge/internal/e2ee/challenge.go`
/// (the phone's OPEN side). When the hub runs HUB_REQUIRE_DEVICE_AUTH it seals a fresh
/// random nonce to this device's PUBLIC key under a one-time ephemeral key; the phone
/// opens it with its SECRET key + the ephemeral public key and returns the nonce as
/// proof of possession. No reusable bearer token ever travels in the URL. The sealed
/// blob is framed nonce||box, matching Go (and openPairing).

/// openChallenge splits nonce||box and opens the box with the hub's EPHEMERAL public
/// key + this device's SECRET key, returning the recovered challenge nonce. Any
/// failure (bad length, auth failure, tamper) throws [E2eeError] — the caller drops
/// the connection rather than send an unverifiable response.
Uint8List openChallenge(Uint8List sealed, Uint8List ephPub, PrivateKey deviceSec) {
  if (sealed.length < 24) throw const E2eeError('challenge open failed');
  final nonce = Uint8List.fromList(sealed.sublist(0, 24));
  final cipher = Uint8List.fromList(sealed.sublist(24));
  try {
    final box = Box(myPrivateKey: deviceSec, theirPublicKey: PublicKey(ephPub));
    return Uint8List.fromList(box.decrypt(EncryptedMessage(nonce: nonce, cipherText: cipher)));
  } on E2eeError {
    rethrow;
  } catch (_) {
    throw const E2eeError('challenge open failed');
  }
}
