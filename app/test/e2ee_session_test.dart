import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';

import 'package:codex_remote_app/src/services/e2ee/codec.dart';
import 'package:codex_remote_app/src/services/e2ee/kdf.dart';
import 'package:codex_remote_app/src/services/e2ee/session.dart';

void main() {
  test('E2eeSession round-trips both directions with monotonic seq', () async {
    final psk = Uint8List.fromList(List.generate(32, (i) => (i * 7) % 256));
    const keyId = 'kid';
    final phone = await E2eeSession.fromPsk(keyId, psk);
    final ks = await deriveKeys(psk, keyId); // simulate the bridge with the same PSK

    // phone -> bridge, two frames; bridge opens with kP2B at seq 0,1.
    final w0 = await phone.sealText({'type': 'prompt', 'text': 'hi'});
    final w1 = await phone.sealText({'type': 'interrupt'});
    final e0 = Envelope.tryParse(jsonDecode(w0) as Map<String, dynamic>)!;
    final e1 = Envelope.tryParse(jsonDecode(w1) as Map<String, dynamic>)!;
    expect(jsonDecode(utf8.decode(await open(ks.kP2B, keyId, E2eeDir.p2b, 0, e0)))['text'], 'hi');
    expect(jsonDecode(utf8.decode(await open(ks.kP2B, keyId, E2eeDir.p2b, 1, e1)))['type'], 'interrupt');

    // bridge -> phone: bridge seals with kB2P at seq 0,1; phone opens in order.
    final b0 = await seal(ks.kB2P, keyId, E2eeDir.b2p, 0, utf8.encode(jsonEncode({'type': 'sessions'})));
    final b1 = await seal(ks.kB2P, keyId, E2eeDir.b2p, 1, utf8.encode(jsonEncode({'type': 'event'})));
    expect((await phone.openText(jsonEncode(b0.toJson())))['type'], 'sessions');
    expect((await phone.openText(jsonEncode(b1.toJson())))['type'], 'event');
  });

  test('openText rejects an out-of-order (replayed) frame', () async {
    final psk = Uint8List.fromList(List.filled(32, 3));
    final phone = await E2eeSession.fromPsk('k', psk);
    final ks = await deriveKeys(psk, 'k');
    final b0 = await seal(ks.kB2P, 'k', E2eeDir.b2p, 0, utf8.encode('{}'));
    await phone.openText(jsonEncode(b0.toJson())); // seqIn -> 1
    // replay seq 0 → rejected
    expect(() => phone.openText(jsonEncode(b0.toJson())), throwsA(isA<E2eeError>()));
  });

  test('openText rejects a non-envelope frame', () async {
    final phone = await E2eeSession.fromPsk('k', Uint8List.fromList(List.filled(32, 1)));
    expect(() => phone.openText('{"type":"sessions"}'), throwsA(isA<E2eeError>()));
  });
}
