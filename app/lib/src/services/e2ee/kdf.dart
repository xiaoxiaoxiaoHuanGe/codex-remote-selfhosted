import 'dart:convert';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';

/// HKDF-SHA256 key derivation — the Dart mirror of `bridge/internal/e2ee/kdf.go`.
///
/// A 32-byte per-device PSK is expanded into two 32-byte directional subkeys with
/// salt = keyId bytes and distinct info strings, so the two directions never share
/// a nonce space. The phone seals phone->bridge frames with kP2B and opens
/// bridge->phone frames with kB2P; the bridge mirrors.

const _infoP2B = 'codex-e2ee/v1/phone->bridge';
const _infoB2P = 'codex-e2ee/v1/bridge->phone';

class DerivedKeys {
  final Uint8List kP2B; // phone->bridge (seal outbound)
  final Uint8List kB2P; // bridge->phone (open inbound)
  const DerivedKeys(this.kP2B, this.kB2P);
}

Future<DerivedKeys> deriveKeys(List<int> psk, String keyId) async {
  final salt = ascii.encode(keyId);
  final kP2B = await _hkdf(psk, salt, _infoP2B);
  final kB2P = await _hkdf(psk, salt, _infoB2P);
  return DerivedKeys(kP2B, kB2P);
}

Future<Uint8List> _hkdf(List<int> psk, List<int> salt, String info) async {
  final hkdf = Hkdf(hmac: Hmac.sha256(), outputLength: 32);
  // In `cryptography`, deriveKey's `nonce` IS the HKDF salt.
  final out = await hkdf.deriveKey(
    secretKey: SecretKey(psk),
    nonce: salt,
    info: ascii.encode(info),
  );
  return Uint8List.fromList(await out.extractBytes());
}
