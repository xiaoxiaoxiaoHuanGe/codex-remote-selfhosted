import 'package:flutter/foundation.dart';

import '../models/machine.dart';
import 'secure_store.dart';

/// Holds the list of saved machines and which one is active, persisting changes
/// to the keychain via [SecureStore]. The phone connects to exactly one machine
/// at a time (the active one); switching just reconnects to a different token.
class MachineStore extends ChangeNotifier {
  final SecureStore _store;
  MachineStore(this._store);

  List<Machine> machines = const [];
  String? activeId;

  Machine? get active {
    for (final m in machines) {
      if (m.id == activeId) return m;
    }
    return machines.isNotEmpty ? machines.first : null;
  }

  Future<void> load() async {
    machines = await _store.loadMachines();
    activeId = await _store.loadActiveId();
    if (activeId == null || machines.every((m) => m.id != activeId)) {
      activeId = machines.isNotEmpty ? machines.first.id : null;
    }
    notifyListeners();
  }

  /// Add a machine (de-duped by token) and make it active. Returns the machine.
  /// [host] is the relay host this machine is reached on ('' = app default);
  /// re-pairing an existing token updates that machine's relay/auth/LAN seed.
  Future<Machine> add(
      {required String label,
      required String token,
      String host = '',
      bool requireDeviceAuth = false,
      List<String> lanCandidates = const [],
      bool? pubEnabled}) async {
    final tok = token.trim();
    final h = host.trim();
    final dup = machines.where((m) => m.token == tok).toList();
    if (dup.isNotEmpty) {
      // Re-pair: refresh relay host / auth mode / LAN seed from the new QR.
      machines = machines
          .map((m) => m.id == dup.first.id
              ? m.copyWith(
                  host: h.isNotEmpty ? h : null,
                  requireDeviceAuth: requireDeviceAuth,
                  lanCandidates:
                      lanCandidates.isNotEmpty ? lanCandidates : null,
                  pubEnabled: pubEnabled)
              : m)
          .toList();
      activeId = dup.first.id;
      await _persist();
      return active ?? dup.first;
    }
    final m = Machine(
      id: DateTime.now().microsecondsSinceEpoch.toString(),
      label: label.trim().isEmpty ? '我的电脑' : label.trim(),
      token: tok,
      host: h,
      requireDeviceAuth: requireDeviceAuth,
      lanCandidates: lanCandidates,
      pubEnabled: pubEnabled ?? true,
    );
    machines = [...machines, m];
    activeId = m.id;
    await _persist();
    return m;
  }

  /// Refresh a machine's LAN candidates + relay-tier flag from a lanInfo
  /// frame. The frame is authoritative: an empty list CLEARS the cache.
  Future<void> updateLanInfo(String id, List<String> cands, bool pub) async {
    var changed = false;
    machines = machines.map((m) {
      if (m.id != id) return m;
      if (listEquals(m.lanCandidates, cands) && m.pubEnabled == pub) return m;
      changed = true;
      return m.copyWith(lanCandidates: cands, pubEnabled: pub);
    }).toList();
    if (changed) await _persist();
  }

  Future<void> setLinkMode(String id, String mode) async {
    machines = machines
        .map((m) => m.id == id ? m.copyWith(linkMode: mode) : m)
        .toList();
    await _persist();
  }

  Future<void> setActive(String id) async {
    if (activeId == id) return;
    activeId = id;
    await _persist();
  }

  Future<void> rename(String id, String label) async {
    final name = label.trim();
    if (name.isEmpty) return;
    machines = machines.map((m) => m.id == id ? m.copyWith(label: name) : m).toList();
    await _persist();
  }

  Future<void> remove(String id) async {
    machines = machines.where((m) => m.id != id).toList();
    if (activeId == id) activeId = machines.isNotEmpty ? machines.first.id : null;
    // Removing the LAST machine must also clear the legacy single-token slot, or
    // loadMachines() resurrects it as "我的电脑" on the next launch — the reason the
    // last device could never be removed and the app skipped the connect screen.
    if (machines.isEmpty) await _store.clearLegacyToken();
    await _persist();
  }

  Future<void> _persist() async {
    await _store.saveMachines(machines);
    if (activeId != null) await _store.saveActiveId(activeId!);
    notifyListeners();
  }
}
