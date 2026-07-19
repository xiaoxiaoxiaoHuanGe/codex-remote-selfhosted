import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import '../config.dart';
import '../models/machine.dart';

/// Thin wrapper around the OS keychain/keystore.
///
/// Persists the list of saved machines (each holding its access token) plus the
/// id of the active one. Older builds stored a single bare token under `token`
/// (and even older ones a full ws URL under `ws_url`); [loadMachines] migrates
/// those transparently into a single "我的电脑" machine.
class SecureStore {
  static const _kToken = 'token'; // legacy single token
  static const _kUrl = 'ws_url'; // legacy full ws URL
  static const _kMachines = 'machines'; // JSON list of Machine
  static const _kActive = 'active_id';
  static const _kExpanded = 'expanded_projects'; // JSON list of expanded project cwds
  static const _kCodecs = 'codecs'; // E2EE: JSON map machineId -> {keyId, psk}

  final _s = const FlutterSecureStorage(
    aOptions: AndroidOptions(encryptedSharedPreferences: true),
  );

  // ---- machines ----

  Future<List<Machine>> loadMachines() async {
    try {
      final raw = await _s.read(key: _kMachines);
      final list = Machine.decodeList(raw);
      if (list.isNotEmpty) return list;
      // Migrate from a legacy single token ONLY on a truly fresh install — i.e. the
      // machines key was NEVER written. Once a list has been saved (even an empty
      // "[]" after removing every device), never resurrect from the legacy slot.
      // This is what let the last device "come back" and skipped the connect screen.
      if (raw == null) {
        final legacy = await loadToken();
        if (legacy != null && legacy.isNotEmpty) {
          final m = Machine(id: 'legacy', label: '我的电脑', token: legacy);
          await saveMachines([m]);
          return [m];
        }
      }
      return const [];
    } catch (_) {
      return const [];
    }
  }

  Future<void> saveMachines(List<Machine> m) async {
    try {
      await _s.write(key: _kMachines, value: Machine.encodeList(m));
    } catch (_) {}
  }

  Future<String?> loadActiveId() async {
    try {
      return await _s.read(key: _kActive);
    } catch (_) {
      return null;
    }
  }

  Future<void> saveActiveId(String id) async {
    try {
      await _s.write(key: _kActive, value: id);
    } catch (_) {}
  }

  // ---- E2EE codec material: the paired (keyId, PSK, device secret) per machine ----

  // Stored as a single JSON map machineId -> {keyId, psk, devSec} (psk + devSec
  // base64). One key keeps revocation/clear simple and avoids leaking machine ids as
  // key names. devSec is the phone's device SECRET key: kept so the phone can answer
  // the hub's device public-key challenge on every connect (HUB_REQUIRE_DEVICE_AUTH).
  // Absent on pairings made before device-auth — loadCodec then returns devSec=null.
  Future<Map<String, dynamic>> _codecMap() async {
    try {
      final raw = await _s.read(key: _kCodecs);
      if (raw == null || raw.isEmpty) return {};
      final m = jsonDecode(raw);
      return m is Map<String, dynamic> ? m : {};
    } catch (_) {
      return {};
    }
  }

  /// The paired keyId + base64 PSK (+ base64 device secret, null on pre-device-auth
  /// pairings) for [machineId], or null if not paired.
  Future<({String keyId, String psk, String? devSec})?> loadCodec(String machineId) async {
    final e = (await _codecMap())[machineId];
    if (e is Map && e['keyId'] is String && e['psk'] is String) {
      return (
        keyId: e['keyId'] as String,
        psk: e['psk'] as String,
        devSec: e['devSec'] is String ? e['devSec'] as String : null,
      );
    }
    return null;
  }

  Future<void> saveCodec(String machineId, String keyId, String psk, {String? devSec}) async {
    try {
      final m = await _codecMap();
      m[machineId] = {
        'keyId': keyId,
        'psk': psk,
        if (devSec != null) 'devSec': devSec,
      };
      await _s.write(key: _kCodecs, value: jsonEncode(m));
    } catch (_) {}
  }

  /// Forget a machine's pairing (e.g. on remove) so a re-pair starts clean.
  Future<void> deleteCodec(String machineId) async {
    try {
      final m = await _codecMap();
      if (m.remove(machineId) != null) {
        await _s.write(key: _kCodecs, value: jsonEncode(m));
      }
    } catch (_) {}
  }

  // ---- UI prefs: which project sections are expanded (default: all collapsed) ----

  Future<Set<String>> loadExpandedProjects() async {
    try {
      final raw = await _s.read(key: _kExpanded);
      if (raw == null || raw.isEmpty) return <String>{};
      final decoded = jsonDecode(raw);
      if (decoded is List) return decoded.whereType<String>().toSet();
      return <String>{};
    } catch (_) {
      return <String>{};
    }
  }

  Future<void> saveExpandedProjects(Set<String> cwds) async {
    try {
      await _s.write(key: _kExpanded, value: jsonEncode(cwds.toList()));
    } catch (_) {}
  }

  // ---- legacy single token (kept for migration & a fallback) ----

  // Storage failures (e.g. missing keychain entitlement during dev) must not
  // break the app — degrade gracefully.
  Future<void> saveToken(String token) async {
    try {
      await _s.write(key: _kToken, value: token.trim());
    } catch (_) {}
  }

  Future<String?> loadToken() async {
    try {
      final t = await _s.read(key: _kToken);
      if (t != null && t.isNotEmpty) return t;
      final old = await _s.read(key: _kUrl);
      if (old != null && old.isNotEmpty) {
        final tok = tokenFromWsUrl(old);
        if (tok != null && tok.isNotEmpty) {
          await saveToken(tok);
          return tok;
        }
      }
      return null;
    } catch (_) {
      return null;
    }
  }

  /// Forget the legacy single-token / ws-url slot. Called when the machine list is
  /// emptied so [loadMachines] doesn't resurrect a "我的电脑" entry from it on the
  /// next launch (the bug where the last device couldn't be removed).
  Future<void> clearLegacyToken() async {
    try {
      await _s.delete(key: _kToken);
      await _s.delete(key: _kUrl);
    } catch (_) {}
  }

  Future<void> clear() async {
    try {
      await _s.delete(key: _kToken);
      await _s.delete(key: _kUrl);
      await _s.delete(key: _kMachines);
      await _s.delete(key: _kActive);
      await _s.delete(key: _kExpanded);
      await _s.delete(key: _kCodecs);
    } catch (_) {}
  }
}
