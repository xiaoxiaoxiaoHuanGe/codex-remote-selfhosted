import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:pinenacl/x25519.dart' show PrivateKey;
import 'package:web_socket_channel/web_socket_channel.dart';

import '../config.dart';
import '../models/machine.dart';
import '../models/session.dart';
import 'connection_racer.dart';
import 'secure_store.dart';
import 'e2ee/challenge.dart';
import 'e2ee/pairing.dart';
import 'e2ee/session.dart';

enum ConnState { disconnected, connecting, connected, error }

/// One rendered line in the chat. `text` is mutable so streaming deltas can be
/// appended in place.
class ChatItem {
  final String role; // user | assistant | tool | file | system | image | media
  String text;
  final String? imageB64; // role == 'image': base64-encoded PNG bytes
  List<FileDelta> fileDeltas;
  ChatItem(this.role, this.text, {this.imageB64, List<FileDelta>? fileDeltas})
      : fileDeltas = fileDeltas ?? <FileDelta>[];
}

class FileDelta {
  final String path;
  final int additions;
  final int deletions;
  final String kind;
  final String diff;
  const FileDelta({
    required this.path,
    required this.additions,
    required this.deletions,
    required this.kind,
    required this.diff,
  });
}

/// An approval request surfaced by the bridge (a shell command / patch the
/// agent wants to run). The user must accept/decline.
class Approval {
  final String id;
  final String method;
  final Map<String, dynamic> params;
  Approval({required this.id, required this.method, required this.params});

  String get command {
    final c = params['command'];
    if (c is List) return c.join(' ');
    if (c is String) return c;
    final actions = params['commandActions'];
    if (actions is List && actions.isNotEmpty) {
      final first = actions.first;
      if (first is Map && first['command'] is String) {
        return first['command'] as String;
      }
    }
    final reason = params['reason'];
    if (reason is String && reason.trim().isNotEmpty) return reason.trim();
    if (method.contains('fileChange')) return '文件改动请求';
    return '需要批准此操作';
  }
}

/// BridgeClient is the single source of truth for the phone UI. It speaks the
/// bridge's simple JSON-over-WebSocket protocol and exposes state via
/// ChangeNotifier so widgets can ListenableBuilder over it.
class BridgeClient extends ChangeNotifier {
  BridgeClient(this._store);

  final SecureStore _store;

  WebSocketChannel? _ch;
  StreamSubscription<dynamic>? _sub;

  ConnState state = ConnState.disconnected;
  String? error;
  String? url;
  bool suppressAutoConnect = false; // true after a user-initiated disconnect

  // ---- E2EE control-plane state (null/false unless the machine is paired) ----
  // _e2ee is the armed per-connection codec; while null the link is cleartext.
  // _pairing is true between sending pair_init and receiving pair_result, during
  // which the first inbound frame is the plaintext pairing reply, not an envelope.
  // Inbound and outbound are each serialized through a Future chain so the async
  // seal/open calls can't reorder frames or race seqIn/seqOut.
  E2eeSession? _e2ee;
  bool _pairing = false;
  // Device-auth (hub HUB_REQUIRE_DEVICE_AUTH): the phone proves possession of its
  // device key in a challenge-response right after the WS upgrade, BEFORE any app
  // frame, and connects with ?keyId= (no reusable token in the URL).
  // _activeRequireDeviceAuth is the machine's setting (survives reconnect);
  // _awaitDeviceAuth is per-connection — true until the hub's auth_challenge is
  // answered, during which the first inbound frame is that plaintext challenge.
  bool _activeRequireDeviceAuth = false;
  bool _awaitDeviceAuth = false;
  bool _awaitAuthOk = false; // sent the response, waiting for the hub's auth_ok
  PrivateKey? _deviceSec;
  String?
      _fileTicket; // short-lived /file session ticket from auth_ok (device-auth)
  String?
      _activeMachineId; // which machine's PSK this connection uses (reconnect)
  String? _pairMachineId;
  DeviceKeypair? _pairDeviceKp;
  Uint8List? _pairEnrollPub;
  Future<void> _inChain = Future<void>.value();
  Future<void> _outChain = Future<void>.value();

  List<Session> sessions = [];
  String? currentThreadId;
  final List<ChatItem> items = [];
  Approval? pendingApproval;
  bool turnRunning = false;
  final Set<String> activeThreadIds = <String>{};
  int threadRevision = 0;
  int mediaRevision = 0;
  DateTime? _lastMediaAuthRefresh;
  bool readingThread = false;
  String lastReadStatus = '';

  // History paging: open a thread with only its last N turns so a heavy session
  // paints fast over the relay; "load earlier" then pages into the past with a
  // before=<historyOffset> cursor, one bounded window per request — any size of
  // session stays reachable without ever producing an oversized frame.
  // historyTruncated drives the top "load earlier" affordance; historyOffset is
  // the index of the earliest loaded turn (the next page's cursor).
  static const int _kHistoryTail = 40;
  int _historyLimit = _kHistoryTail;
  bool historyTruncated = false;
  int historyTotal = 0;
  int historyOffset = 0;
  bool loadingEarlier = false;
  // Old bridges don't send `offset` and ignore `before` — fall back to the
  // legacy "load earlier = full re-read" there (set on each thread frame).
  bool _bridgePages = false;

  // In-flight read dedup. Multiple triggers (tap-open + ChatScreen init, app
  // resume observers, reconnect) used to stack identical multi-MB reads on the
  // relay. A read younger than this window suppresses duplicates; an older one
  // doesn't block, so a lost response can never lock a thread out of refresh.
  DateTime? _readSentAt;
  static const _kReadFresh = Duration(seconds: 8);
  bool get _readInFlight =>
      readingThread &&
      _readSentAt != null &&
      DateTime.now().difference(_readSentAt!) < _kReadFresh;

  ChatItem? _streamingAssistant;
  ChatItem? _streamingTool;
  ChatItem? _streamingReasoning;
  ChatItem? _streamingFile;
  // True once the current turn has produced a reasoning *summary*; raw reasoning
  // deltas are then dropped so a config that emits both never doubles the text.
  bool _sawReasoningSummary = false;

  // Bumped on every connect(); lets a newer connect supersede an in-flight one
  // (e.g. rapid machine switches) without leaking the older channel.
  int _connectGen = 0;

  // Auto-reconnect: the hub closes the phone link whenever the machine's agent
  // link drops (e.g. the relay's ~30-min recycle), so we keep retrying — with a
  // capped backoff — until the agent is back, unless the user disconnected.
  Timer? _reconnectTimer;
  int _reconnectAttempts = 0;

  // LAN direct racing state: the active machine's candidate URLs kept for
  // reconnect re-racing, and which transport won (drives the 直连/中继 badge).
  List<String> _activeLanUrls = const [];
  String? _activeRelayUrl;
  bool directTransport = false;

  /// Fires when the bridge pushes lanInfo: (machineId, candidates, pub).
  /// Wired to MachineStore.updateLanInfo in RootScreen.
  void Function(String machineId, List<String> candidates, bool pub)? onLanInfo;

  void _scheduleReconnect() {
    if (suppressAutoConnect) return;
    if (_activeRelayUrl == null && _activeLanUrls.isEmpty) return;
    _reconnectTimer?.cancel();
    final ms = (500 * (1 << _reconnectAttempts)).clamp(500, 8000);
    if (_reconnectAttempts < 5) _reconnectAttempts++;
    _reconnectTimer = Timer(Duration(milliseconds: ms), () {
      if (!suppressAutoConnect && state != ConnState.connected) {
        // Re-RACE every time: coming home flips to direct, leaving falls back
        // to relay — and the PSK re-arms / device-auth re-runs as before.
        _connectRaced(
            relayUrl: _activeRelayUrl,
            lanUrls: _activeLanUrls,
            machineId: _activeMachineId,
            requireDeviceAuth: _activeRequireDeviceAuth);
      }
    });
  }

  /// Connect to a saved machine: build its LAN + relay candidate URLs from the
  /// cached lanInfo and the user's link mode, then race them (relay 300ms
  /// behind). The FIRST pairing connect (pairingId set) never races — the
  /// one-time pid must be consumed on exactly one link: LAN first with a short
  /// timeout, then relay (sequential, inside _pairingDial).
  Future<void> connectMachine(Machine m,
      {String? pairingId, String? enrollPubB64, String? deviceName}) {
    final lan = [for (final c in m.lanCandidates) lanWsUrl(m.token, c)];
    return _connectRaced(
      relayUrl:
          m.linkMode == 'lanOnly' ? null : wsUrlForToken(m.token, host: m.host),
      lanUrls: m.linkMode == 'relayOnly' ? const [] : lan,
      machineId: m.id,
      pairingId: pairingId,
      enrollPubB64: enrollPubB64,
      deviceName: deviceName,
      requireDeviceAuth: m.requireDeviceAuth,
    );
  }

  /// Legacy single-URL connect (kept for the --dart-define=WS_URL boot path).
  Future<void> connect(
    String wsUrl, {
    String? machineId,
    String? pairingId,
    String? enrollPubB64,
    String? deviceName,
    bool requireDeviceAuth = false,
  }) =>
      _connectRaced(
        relayUrl: wsUrl,
        lanUrls: const [],
        machineId: machineId,
        pairingId: pairingId,
        enrollPubB64: enrollPubB64,
        deviceName: deviceName,
        requireDeviceAuth: requireDeviceAuth,
      );

  /// Race-and-connect core. [machineId] selects which paired machine's PSK arms
  /// the E2EE codec (and is reused on auto-reconnect); pass it for every real
  /// machine. [pairingId] + [enrollPubB64] are present ONLY for the first
  /// connect right after scanning a fresh QR — they trigger the one-time
  /// pairing handshake. With neither a stored PSK nor pairing material the link
  /// stays legacy cleartext.
  Future<void> _connectRaced({
    required String? relayUrl,
    required List<String> lanUrls,
    String? machineId,
    String? pairingId,
    String? enrollPubB64,
    String? deviceName,
    bool requireDeviceAuth = false,
  }) async {
    if (relayUrl == null && lanUrls.isEmpty) {
      // lanOnly with an empty candidate cache: nothing to dial.
      state = ConnState.error;
      error = '仅局域网模式：还没有直连地址（先在同一 WiFi 连一次，或改回自动）';
      notifyListeners();
      return;
    }
    _activeRelayUrl = relayUrl;
    _activeLanUrls = lanUrls;
    suppressAutoConnect = false; // explicit connect re-enables auto-reconnect
    final gen = ++_connectGen;
    await _teardown();
    if (machineId != _activeMachineId) {
      // Switching machines: the thread/session state belongs to the OLD one.
      // Without this reset _onReady would read machine A's threadId on machine
      // B (a guaranteed error frame + '读取失败') and A's session list would
      // linger under B's banner until B's list arrives.
      currentThreadId = null;
      sessions = [];
      activeThreadIds.clear();
      _resetChat();
      historyTruncated = false;
      historyTotal = 0;
      historyOffset = 0;
      lastReadStatus = '';
    }
    url = relayUrl ?? lanUrls.first; // placeholder — overwritten by the winner
    _activeMachineId = machineId;
    _activeRequireDeviceAuth = requireDeviceAuth;
    state = ConnState.connecting;
    error = null;
    notifyListeners();
    try {
      // Decide the codec BEFORE opening the socket: arm from a stored PSK, else pair
      // if the QR carried pairing material, else legacy cleartext.
      E2eeSession? armed;
      var willPair = false;
      String? deviceAuthKeyId;
      PrivateKey? deviceSec;
      if (machineId != null) {
        final stored = await _store.loadCodec(machineId);
        if (stored != null) {
          armed = await E2eeSession.fromPsk(
              stored.keyId, base64.decode(stored.psk));
          // Device-auth needs the device SECRET saved at pairing. A pre-device-auth
          // pairing lacks it, so the user must re-pair before HUB_REQUIRE_DEVICE_AUTH
          // can be used (the connect then fails closed at the hub).
          if (requireDeviceAuth && stored.devSec != null) {
            deviceAuthKeyId = stored.keyId;
            deviceSec = PrivateKey(base64.decode(stored.devSec!));
          }
        } else if (pairingId != null &&
            enrollPubB64 != null &&
            enrollPubB64.isNotEmpty) {
          willPair = true;
        }
      }

      final useDeviceAuth = deviceSec != null && deviceAuthKeyId != null;
      // Device-auth rewriting applies ONLY to the relay URL — the hub runs the
      // challenge. Direct LAN always authenticates with the token query.
      final String? relayDial = relayUrl == null
          ? null
          : (useDeviceAuth ? _deviceAuthUrl(relayUrl, deviceAuthKeyId) : relayUrl);
      final RaceOutcome<WebSocketChannel> out;
      if (willPair) {
        out = await _pairingDial(lanUrls, relayDial);
      } else {
        out = await raceConnect<WebSocketChannel>(
          lanUrls: lanUrls,
          relayUrl: relayDial,
          dialer: _dialWs,
          closeLoser: (ch) => ch.sink.close(),
        );
      }
      final ch = out.channel;
      if (gen != _connectGen) {
        await ch.sink.close(); // superseded by a newer connect()
        return;
      }
      url = out.url;
      directTransport = out.direct;
      _ch = ch;
      _e2ee = armed;
      _pairing = false;
      // The hub's device-auth challenge only happens on the relay path; a LAN
      // win authenticates with the token and speaks app frames immediately.
      _awaitDeviceAuth = useDeviceAuth && !out.direct;
      _deviceSec = deviceSec;
      _inChain = Future<void>.value();
      _outChain = Future<void>.value();
      _sub = ch.stream.listen(_onData, onError: _onError, onDone: _onDone);
      state = ConnState.connected;
      // NOTE: _reconnectAttempts is reset in _dispatch on the first valid app
      // frame, NOT here. Resetting on a bare TCP/WS upgrade meant a link that
      // connects-then-dies (ailing hub) retried at a flat 500ms forever — a
      // metronome of reconnect floods instead of an honest backoff.
      _reconnectTimer?.cancel();
      notifyListeners();

      if (willPair) {
        _beginPairing(machineId!, pairingId!, enrollPubB64!, deviceName);
      } else if (_awaitDeviceAuth) {
        // Defer bootstrap: the hub speaks first with auth_challenge (see _handleFrame).
        // Sending any app frame before answering would be read as the auth response.
      } else {
        _onReady(); // armed or legacy → bootstrap now (pairing defers it)
      }
    } catch (e) {
      if (gen != _connectGen) return; // a newer connect() is now in charge
      state = ConnState.error;
      error = e.toString();
      notifyListeners();
      _scheduleReconnect();
    }
  }

  /// Rewrite a token URL into the device-auth form: drop the bearer token and add the
  /// keyId the hub routes + challenges by, so no reusable secret travels in the URL.
  String _deviceAuthUrl(String wsUrl, String keyId) {
    final u = Uri.parse(wsUrl);
    final qp = Map<String, String>.from(u.queryParameters)
      ..remove('token')
      ..['keyId'] = keyId;
    return u.replace(queryParameters: qp).toString();
  }

  /// Real WS dialer for the racer: connect + wait for the upgrade.
  Future<WebSocketChannel> _dialWs(String u) async {
    final ch = WebSocketChannel.connect(Uri.parse(u));
    await ch.ready;
    return ch;
  }

  /// Pairing never races (the pid is single-use): try each LAN candidate
  /// sequentially with a short timeout, then fall back to the relay.
  Future<RaceOutcome<WebSocketChannel>> _pairingDial(
      List<String> lanUrls, String? relayUrl) async {
    for (final u in lanUrls) {
      try {
        return await raceConnect<WebSocketChannel>(
          lanUrls: [u],
          relayUrl: null,
          dialer: _dialWs,
          closeLoser: (ch) => ch.sink.close(),
          perTryTimeout: const Duration(milliseconds: 1500),
        );
      } catch (_) {/* try the next candidate */}
    }
    if (relayUrl == null) throw StateError('no candidates');
    return raceConnect<WebSocketChannel>(
      lanUrls: const [],
      relayUrl: relayUrl,
      dialer: _dialWs,
      closeLoser: (ch) => ch.sink.close(),
    );
  }

  /// Post-connect bootstrap, run once the link can carry app frames (armed codec,
  /// legacy cleartext, or right after a successful pair).
  void _onReady() {
    listSessions();
    // Recover after a drop: re-read the thread the user is viewing so a reply that
    // completed while we were disconnected shows up. Reuse the current tail bound
    // so a reconnect doesn't silently re-truncate a fully-loaded transcript. Goes
    // through the same in-flight state as every other read so dedup works.
    if (currentThreadId != null) {
      readingThread = true;
      lastReadStatus = '读取中…';
      _readSentAt = DateTime.now();
      _send({
        'type': 'read',
        'threadId': currentThreadId,
        'limit': _historyLimit
      });
    }
  }

  /// User-initiated disconnect: stays disconnected (suppresses auto-reconnect).
  Future<void> disconnect() async {
    suppressAutoConnect = true;
    _reconnectTimer?.cancel();
    await _teardown();
    if (state != ConnState.error) state = ConnState.disconnected;
    notifyListeners();
  }

  Future<void> _teardown() async {
    // The subscription is cancelled before the socket closes, so _onDone never
    // fires for THIS path — abort any in-flight read here or a user-initiated
    // disconnect leaves '读取中…' frozen forever. Idempotent; on a reconnect
    // _onReady re-arms the read state immediately after.
    _abortPendingRead();
    await _sub?.cancel();
    _sub = null;
    await _ch?.sink.close();
    _ch = null;
    _e2ee = null;
    _pairing = false;
    _awaitDeviceAuth = false;
    _awaitAuthOk = false;
    _deviceSec = null;
    _fileTicket = null;
    _pairDeviceKp = null;
    _pairEnrollPub = null;
    _pairMachineId = null;
  }

  /// Send a message. Once the codec is armed it is sealed into an envelope; before
  /// arming (or with E2EE off) it goes out as cleartext. Sends are serialized
  /// through [_outChain] so the async seal can't reorder frames or race seqOut.
  /// Dropped while pairing (the codec isn't ready — nothing should send then).
  void _send(Map<String, dynamic> m) {
    final ch = _ch;
    if (ch == null || _pairing) return;
    final s = _e2ee;
    if (s == null) {
      ch.sink.add(jsonEncode(m));
      return;
    }
    _outChain = _outChain.then((_) async {
      ch.sink.add(await s.sealText(m));
    }).catchError((_) {});
  }

  /// Build an authenticated bridge URL for a desktop media file path so the
  /// phone can fetch images/videos it cannot access directly.
  String fileUrl(String pathOrUri) {
    final u = url;
    if (u == null) return pathOrUri;
    final ws = Uri.parse(u);
    final scheme = ws.scheme == 'wss' ? 'https' : 'http';
    var p = pathOrUri;
    if (p.startsWith('file://')) {
      try {
        p = Uri.parse(p).toFilePath();
      } catch (_) {}
    }
    // A web URL loads directly — proxying it through /file would only fail the
    // bridge's absolute-path clamp.
    if (p.startsWith('http://') || p.startsWith('https://')) return p;
    // Markdown in replies references media RELATIVE to the thread's cwd (e.g.
    // "assets/foo.png"). The bridge's /file clamp requires an absolute path
    // (a relative one resolves against the BRIDGE's cwd and 404s), so anchor
    // it at the open session's cwd — which the session list already carries.
    if (!p.startsWith('/')) {
      final tid = currentThreadId;
      var cwd = '';
      for (final s in sessions) {
        if (s.id == tid) {
          cwd = s.cwd;
          break;
        }
      }
      if (cwd.isNotEmpty) p = cwd.endsWith('/') ? '$cwd$p' : '$cwd/$p';
    }
    // Device-auth: authorize /file with the short-lived session ticket from auth_ok
    // (no bearer token in the URL). Otherwise fall back to the per-machine token.
    final qp = <String, String>{'path': p};
    if (_activeRequireDeviceAuth && _fileTicket != null) {
      qp['ticket'] = _fileTicket!;
    } else {
      qp['token'] = ws.queryParameters['token'] ?? '';
    }
    qp['v'] = mediaRevision.toString();
    return Uri(
      scheme: scheme,
      host: ws.host,
      port: ws.port,
      path: '/file',
      queryParameters: qp,
    ).toString();
  }

  void listSessions() => _send({'type': 'list'});

  void refreshCurrentThread({bool force = false}) {
    final tid = currentThreadId;
    if (tid == null || state != ConnState.connected) return;
    if ((loadingEarlier || _readInFlight) && !force) return;
    readingThread = true;
    lastReadStatus = '读取中…';
    _readSentAt = DateTime.now();
    _send({'type': 'read', 'threadId': tid, 'limit': _historyLimit});
    notifyListeners();
  }

  void syncNow() {
    if (state != ConnState.connected) return;
    listSessions();
    refreshCurrentThread();
  }

  void refreshMediaAuth() {
    if (!_activeRequireDeviceAuth || state != ConnState.connected) return;
    final now = DateTime.now();
    final last = _lastMediaAuthRefresh;
    if (last != null && now.difference(last) < const Duration(seconds: 12)) {
      return;
    }
    _lastMediaAuthRefresh = now;
    final u = url;
    if (u == null) return;
    unawaited(connect(u,
        machineId: _activeMachineId,
        requireDeviceAuth: _activeRequireDeviceAuth));
  }

  void openThread(String id) {
    // Dedup: the session-list tap and ChatScreen's init both land here within a
    // frame; the second call used to reset state and double the multi-MB read
    // over the relay. A fresh in-flight read of the SAME thread makes this a
    // no-op (a stale one — lost response — passes, so refresh always works).
    if (currentThreadId == id && _readInFlight) return;
    currentThreadId = id;
    _historyLimit = _kHistoryTail;
    historyTruncated = false;
    historyTotal = 0;
    historyOffset = 0;
    loadingEarlier = false;
    _resetChat();
    readingThread = true;
    lastReadStatus = '读取中…';
    _readSentAt = DateTime.now();
    turnRunning = activeThreadIds.contains(id);
    _send({'type': 'read', 'threadId': id, 'limit': _historyLimit});
    notifyListeners();
  }

  /// Pull one earlier page of the transcript (the "load earlier" action). The
  /// bridge windows by turn index: we pass the offset of our earliest loaded
  /// turn and get the bounded page just before it, grafted in front on arrival
  /// (no flash, the visible tail never moves). Old bridges don't page — there
  /// we keep the legacy full re-read.
  void loadEarlier() {
    // Don't reshape history mid-turn: a full-reload response runs _loadHistory
    // -> _resetChat, which would wipe the in-flight streaming blocks and zero
    // turnRunning. Wait for the turn to finish (the header is disabled then too).
    if (currentThreadId == null ||
        !historyTruncated ||
        loadingEarlier ||
        turnRunning) {
      return;
    }
    loadingEarlier = true;
    readingThread = true;
    lastReadStatus = '读取中…';
    _readSentAt = DateTime.now();
    if (_bridgePages && historyOffset > 0) {
      _send({
        'type': 'read',
        'threadId': currentThreadId,
        'limit': _kHistoryTail,
        'before': historyOffset,
      });
    } else {
      _historyLimit = 0; // legacy bridge: 0 → full history in one frame
      _send({'type': 'read', 'threadId': currentThreadId, 'limit': 0});
    }
    notifyListeners();
  }

  void newThread() {
    currentThreadId = null;
    _resetChat();
    // Clear read/truncation state too (not covered by _resetChat): the tightened
    // thread-frame guard now DROPS a late frame for the old thread, so nothing
    // would otherwise reset these — a stale '读取中…', an 8s dedup window, or a
    // leftover load-earlier header must not leak into a fresh conversation.
    readingThread = false;
    loadingEarlier = false;
    _readSentAt = null;
    lastReadStatus = '';
    historyTruncated = false;
    historyTotal = 0;
    historyOffset = 0;
    notifyListeners();
  }

  /// Delete (archive) a session. Optimistically removes it locally; the bridge
  /// also pushes a refreshed session list.
  void deleteThread(String id) {
    sessions = sessions.where((s) => s.id != id).toList();
    _send({'type': 'delete', 'threadId': id});
    notifyListeners();
  }

  /// Send a prompt with optional image attachments. [images] are full data URLs
  /// (data:image/jpeg;base64,...) which the bridge passes to Codex as image
  /// turn-input items.
  ///
  /// [effort] (low|medium|high|xhigh), [model], [speed] and [approval] are
  /// optional turn hints carried to the bridge; bridges that don't understand
  /// them ignore the extra fields.
  void sendPrompt(String text,
      {String? cwd,
      List<String> images = const [],
      String? effort,
      String? model,
      String? speed,
      String? approval}) {
    final hasText = text.trim().isNotEmpty;
    if (!hasText && images.isEmpty) return;
    if (state != ConnState.connected || _ch == null) {
      items.add(ChatItem('system', '⚠️ 未连接到电脑，正在重连…连上后请重发。'));
      notifyListeners();
      _scheduleReconnect();
      return;
    }
    // Echo what the user sent into the transcript: images first, then the text.
    for (final dataUrl in images) {
      final b64 = dataUrl.contains(',') ? dataUrl.split(',').last : dataUrl;
      items.add(ChatItem('image', '', imageB64: b64));
    }
    if (hasText) items.add(ChatItem('user', text));
    _streamingAssistant = null;
    _streamingTool = null;
    _streamingReasoning = null;
    _streamingFile = null;
    _sawReasoningSummary = false;
    turnRunning = true;
    if (currentThreadId != null) activeThreadIds.add(currentThreadId!);
    final Map<String, dynamic> msg = {'type': 'prompt', 'text': text};
    if (images.isNotEmpty) msg['images'] = images;
    if (currentThreadId != null) msg['threadId'] = currentThreadId;
    if (cwd != null && cwd.isNotEmpty) msg['cwd'] = cwd;
    if (effort != null && effort.isNotEmpty) msg['effort'] = effort;
    if (model != null && model.isNotEmpty) msg['model'] = model;
    if (speed != null && speed.isNotEmpty) msg['speed'] = speed;
    if (approval != null && approval.isNotEmpty) msg['approval'] = approval;
    _send(msg);
    notifyListeners();
  }

  void respondApproval(String decision) {
    final a = pendingApproval;
    if (a == null) return;
    _send({'type': 'approvalDecision', 'id': a.id, 'decision': decision});
    pendingApproval = null;
    notifyListeners();
  }

  void interrupt() {
    if (currentThreadId != null) {
      _send({'type': 'interrupt', 'threadId': currentThreadId});
    }
  }

  void _resetChat() {
    items.clear();
    _streamingAssistant = null;
    _streamingTool = null;
    _streamingReasoning = null;
    _streamingFile = null;
    _sawReasoningSummary = false;
    pendingApproval = null;
    turnRunning = false;
  }

  // ---- incoming ----

  // Frames are enqueued onto a single chain so the async pairing-open / decrypt
  // can't overlap or reorder (which would corrupt the monotonic seqIn).
  void _onData(dynamic raw) {
    if (raw is! String) return;
    // Log instead of pure-swallowing: a schema drift that throws inside
    // _dispatch must at least be diagnosable from a debug console, not vanish
    // as a silent dropped frame (how the kind-cast truncation hid for so long).
    _inChain = _inChain
        .then((_) => _handleFrame(raw))
        .catchError((e) => debugPrint('bridge frame error: $e'));
  }

  Future<void> _handleFrame(String raw) async {
    // Device-auth: the hub's plaintext auth_challenge is the FIRST frame, before any
    // app/pairing frame. Answer it (proving we hold the device key), then bootstrap.
    if (_awaitDeviceAuth) {
      await _onAuthChallenge(raw);
      return;
    }
    if (_awaitAuthOk) {
      await _onAuthOk(raw);
      return;
    }
    // During pairing the first frame is the plaintext pair_result / pair_error.
    if (_pairing) {
      await _onPairFrame(raw);
      return;
    }
    Map<String, dynamic> m;
    final s = _e2ee;
    if (s != null) {
      // Armed: decrypt the envelope. ANY failure tears the link down (fail closed)
      // rather than processing an unverified frame.
      try {
        m = await s.openText(raw);
      } catch (_) {
        await _failSecure('安全帧校验失败');
        return;
      }
    } else {
      try {
        m = jsonDecode(raw) as Map<String, dynamic>;
      } catch (_) {
        return;
      }
    }
    _dispatch(m);
    notifyListeners();
  }

  void _dispatch(Map<String, dynamic> m) {
    // A decoded app frame proves the link is genuinely healthy — only now does
    // the reconnect backoff reset (see the note in connect()).
    _reconnectAttempts = 0;
    switch (m['type']) {
      case 'sessions':
        final data = (m['data'] as List?) ?? const [];
        sessions = data
            .whereType<Map<String, dynamic>>()
            .map(Session.fromJson)
            .toList();
        break;
      case 'thread':
        // Drop a late frame for a thread we are no longer viewing. The bridge
        // echoes threadId on every thread frame. currentThreadId == null (the
        // user jumped to a new-thread screen mid-read) also counts as navigated
        // away — treating it as a match used to flood the fresh screen with the
        // previous thread's transcript.
        final tidRaw = m['threadId'];
        final tid = tidRaw is String ? tidRaw : null;
        if (tid != null && tid != currentThreadId) {
          break;
        }
        historyTruncated = m['truncated'] == true;
        final total = m['total'];
        historyTotal = total is num ? total.toInt() : 0;
        final off = m['offset'];
        if (off is num) {
          historyOffset = off.toInt();
          _bridgePages = true; // bridge speaks cursor paging
        } else if (m['page'] != true) {
          historyOffset = 0;
        }
        loadingEarlier = false;
        if (m['page'] == true) {
          // An earlier-history page: graft it in FRONT of the transcript —
          // never reset, the visible tail (and any in-flight stream) stays.
          _prependHistory(m['thread']);
        } else {
          // Full (re)load. Preserve an unanswered approval across
          // _loadHistory's _resetChat: a resume/reconnect refresh must not wipe
          // the card while the bridge is still waiting on the decision (it
          // fail-closes after 180s otherwise).
          final ap = pendingApproval;
          _loadHistory(m['thread']);
          if (ap != null && pendingApproval == null) pendingApproval = ap;
        }
        readingThread = false;
        _readSentAt = null;
        threadRevision++;
        turnRunning = currentThreadId != null &&
            activeThreadIds.contains(currentThreadId);
        break;
      case 'promptAccepted':
        final tid = m['threadId'] as String?;
        currentThreadId = tid ?? currentThreadId;
        if (tid != null && tid.isNotEmpty) {
          activeThreadIds.add(tid);
          turnRunning = true;
        }
        break;
      case 'event':
        _onEvent((m['method'] as String?) ?? '', m['params']);
        break;
      case 'approval':
        pendingApproval = Approval(
          id: (m['id'] as String?) ?? '',
          method: (m['method'] as String?) ?? '',
          params: (m['params'] as Map<String, dynamic>?) ?? const {},
        );
        break;
      case 'lanInfo':
        // Refresh the machine's direct-connect cache (authoritative: an empty
        // candidate list clears it) + relay-tier flag for the UI.
        final mid = _activeMachineId;
        if (mid != null) {
          final cands = ((m['candidates'] as List?) ?? const [])
              .whereType<String>()
              .toList();
          onLanInfo?.call(mid, cands, m['pub'] != false);
        }
        break;
      case 'error':
        error = (m['message'] as String?) ?? 'error';
        turnRunning = false;
        _streamingAssistant = null;
        _streamingTool = null;
        // A failed read (e.g. the heavy full-history "load earlier" pull) replies
        // with error, not thread — so clear loadingEarlier here too, otherwise the
        // header spins forever with onPressed:null and the user can't retry.
        // error is the bridge's GENERIC failure frame (prompt/interrupt/approval
        // errors land here too) — only flip the read state when a read is
        // actually in flight, or an unrelated error overwrites a healthy
        // '已读取…' status line with a misleading '读取失败'.
        if (readingThread || loadingEarlier) {
          loadingEarlier = false;
          readingThread = false;
          _readSentAt = null;
          lastReadStatus = '读取失败';
        }
        items.add(ChatItem('system', '⚠️ $error'));
        break;
    }
  }

  // ---- device-auth handshake (hub HUB_REQUIRE_DEVICE_AUTH) ----

  /// Answer the hub's auth_challenge: open the box sealed to our device key with the
  /// stored device secret + the challenge's ephemeral public key, then echo the
  /// recovered nonce as proof. On any failure, fail closed (stop auto-reconnect — a
  /// re-pair is the recovery path, not a tight reconnect loop against a hub that
  /// rejects us). On success, the link can now carry app frames → bootstrap.
  Future<void> _onAuthChallenge(String raw) async {
    Map<String, dynamic> m;
    try {
      m = jsonDecode(raw) as Map<String, dynamic>;
    } catch (_) {
      await _failSecure('设备认证应答无法解析', stop: true);
      return;
    }
    final epkB64 = m['epk'], boxB64 = m['box'];
    final sec = _deviceSec;
    if (m['type'] != 'auth_challenge' ||
        epkB64 is! String ||
        boxB64 is! String ||
        sec == null) {
      await _failSecure('设备认证流程异常', stop: true);
      return;
    }
    try {
      final nonce =
          openChallenge(base64.decode(boxB64), base64.decode(epkB64), sec);
      _ch?.sink.add(jsonEncode({
        'type': 'auth_response',
        'keyId': m['keyId'] ?? '',
        'proof': base64.encode(nonce),
      }));
      // Don't bootstrap yet: the hub confirms with auth_ok (carrying the /file
      // ticket) BEFORE relaying, and that frame is plaintext — sending an app frame
      // now would make the hub read it as the response and would arrive as an
      // un-decryptable plaintext on our armed codec.
      _awaitDeviceAuth = false;
      _awaitAuthOk = true;
    } catch (_) {
      await _failSecure('设备认证失败（无法应答挑战）', stop: true);
    }
  }

  /// Receive the hub's auth_ok: stash the short-lived /file session ticket (used by
  /// [fileUrl] in place of the bearer token), then bootstrap — the link is now
  /// authenticated. A malformed/missing auth_ok fails closed.
  Future<void> _onAuthOk(String raw) async {
    Map<String, dynamic> m;
    try {
      m = jsonDecode(raw) as Map<String, dynamic>;
    } catch (_) {
      await _failSecure('设备认证确认无法解析', stop: true);
      return;
    }
    if (m['type'] != 'auth_ok') {
      await _failSecure('设备认证确认异常', stop: true);
      return;
    }
    final t = m['ticket'];
    _fileTicket = t is String ? t : null;
    mediaRevision++;
    _awaitAuthOk = false;
    notifyListeners();
    _onReady(); // authed → bootstrap (listSessions / re-read thread)
  }

  // ---- pairing handshake (E2EE first-connect) ----

  /// Start the one-time pairing: generate a device keypair and send the plaintext
  /// pair_init as the FIRST frame. The bridge replies with the sealed PSK.
  void _beginPairing(String machineId, String pairingId, String enrollPubB64,
      String? deviceName) {
    _pairing = true;
    _pairMachineId = machineId;
    try {
      _pairEnrollPub = base64.decode(enrollPubB64);
    } catch (_) {
      _failSecure('配对参数无效');
      return;
    }
    final kp = DeviceKeypair.generate();
    _pairDeviceKp = kp;
    _ch?.sink.add(jsonEncode({
      'type': 'pair_init',
      'pairingId': pairingId,
      'devicePub': base64.encode(kp.publicKey),
      'name': (deviceName != null && deviceName.trim().isNotEmpty)
          ? deviceName.trim()
          : 'phone',
    }));
  }

  Future<void> _onPairFrame(String raw) async {
    Map<String, dynamic> m;
    try {
      m = jsonDecode(raw) as Map<String, dynamic>;
    } catch (_) {
      await _failSecure('配对应答无法解析', stop: true);
      return;
    }
    if (m['type'] == 'pair_error') {
      await _failSecure('配对被拒绝（${m['reason'] ?? 'pairing'}）', stop: true);
      return;
    }
    final sealedB64 = m['sealed'];
    if (m['type'] != 'pair_result' || sealedB64 is! String) {
      await _failSecure('配对应答异常', stop: true);
      return;
    }
    try {
      final payload = openPairing(
          base64.decode(sealedB64), _pairEnrollPub!, _pairDeviceKp!.secretKey);
      // Persist the device SECRET too: the hub's later device-auth challenge is sealed
      // to this keypair's public half, so we need the secret to answer it.
      await _store.saveCodec(_pairMachineId!, payload.keyId, payload.psk,
          devSec: base64.encode(_pairDeviceKp!.secretKey));
      _e2ee =
          await E2eeSession.fromPsk(payload.keyId, base64.decode(payload.psk));
      _pairing = false;
      _pairDeviceKp = null;
      _pairEnrollPub = null;
      notifyListeners();
      _onReady(); // now armed → bootstrap (listSessions / re-read thread)
    } catch (_) {
      await _failSecure('无法解封配对密钥', stop: true);
    }
  }

  /// Fail closed on any E2EE error: surface it, drop the link, and (for a pairing
  /// failure) suppress auto-reconnect so we don't loop a cleartext bridge into a
  /// hard-cut — the user re-scans to retry. A decrypt failure on an armed link keeps
  /// auto-reconnect on (it re-arms from the stored PSK).
  Future<void> _failSecure(String reason, {bool stop = false}) async {
    error = reason;
    items.add(ChatItem('system', '⚠️ $reason'));
    state = ConnState.error;
    if (stop) suppressAutoConnect = true;
    _abortPendingRead(); // a dropped response frame must not freeze '读取中…'
    await _teardown();
    notifyListeners();
    if (!stop) _scheduleReconnect();
  }

  void _onEvent(String method, dynamic params) {
    final p =
        (params is Map<String, dynamic>) ? params : const <String, dynamic>{};
    final tid = _str(p['threadId']);

    // ---- session-list sync (the list is otherwise only fetched on connect) ----
    // The desktop renamed a thread: patch its name in place so the list reflects
    // it live. If we don't know the thread yet, re-fetch the authoritative list.
    if (method.endsWith('thread/name/updated')) {
      final tid = _str(p['threadId']);
      final newName = _str(p['threadName']);
      final idx = sessions.indexWhere((s) => s.id == tid);
      if (idx >= 0) {
        final next = [...sessions];
        next[idx] = sessions[idx].copyWith(name: newName);
        sessions = next;
      } else if (tid.isNotEmpty) {
        listSessions();
      }
      return;
    }
    // Archived elsewhere (desktop) → drop it from the list immediately so we
    // never show archived sessions.
    if (method.endsWith('thread/archived')) {
      final tid = _str(p['threadId']);
      if (tid.isNotEmpty) {
        sessions = sessions.where((s) => s.id != tid).toList();
      }
      return;
    }
    // A thread was created or restored elsewhere → re-fetch the (non-archived) list.
    if (method.endsWith('thread/unarchived') ||
        method.endsWith('thread/started')) {
      listSessions();
      return;
    }

    // ---- live streaming ----
    if (method.endsWith('item/started')) {
      final cmd = _commandFromStartedItem(p['item']);
      if (cmd.isNotEmpty) {
        _streamingAssistant = null;
        _streamingReasoning = null;
        _streamingTool = null;
        _streamingFile = null;
        items.add(ChatItem('command', cmd));
      }
    } else if (method.endsWith('commandExecution/terminalInteraction')) {
      final cmd = _cleanCommand(_str(p['stdin']));
      if (cmd.isNotEmpty) {
        _streamingAssistant = null;
        _streamingReasoning = null;
        _streamingTool = null;
        _streamingFile = null;
        items.add(ChatItem('command', cmd));
      }
    } else if (method.endsWith('agentMessage/delta')) {
      // The answer for this step starts → the reasoning block for it is done.
      _streamingReasoning = null;
      _streamingFile = null;
      _streamingAssistant ??= _append(ChatItem('assistant', ''));
      _streamingAssistant!.text += _str(p['delta']);
    } else if (method.endsWith('reasoning/summaryTextDelta')) {
      // The model's visible "thinking" summary (Codex default). Stream it into a
      // dimmed reasoning block so the thinking process is actually shown.
      _sawReasoningSummary = true;
      _streamingAssistant = null;
      _streamingReasoning ??= _append(ChatItem('reasoning', ''));
      _streamingReasoning!.text += _str(p['delta']);
    } else if (method.endsWith('reasoning/textDelta')) {
      // Raw reasoning — only used when no summary is emitted (avoid doubling).
      if (!_sawReasoningSummary) {
        _streamingAssistant = null;
        _streamingReasoning ??= _append(ChatItem('reasoning', ''));
        _streamingReasoning!.text += _str(p['delta']);
      }
    } else if (method.endsWith('reasoning/summaryPartAdded')) {
      // A new summary paragraph begins — separate it from the previous one.
      final r = _streamingReasoning;
      if (r != null && r.text.isNotEmpty) r.text += '\n\n';
    } else if (method.endsWith('commandExecution/outputDelta')) {
      _streamingFile = null;
      _streamingTool ??= _append(ChatItem('tool', ''));
      _streamingTool!.text += _str(p['delta']);
    } else if (method.endsWith('command/exec/outputDelta')) {
      _streamingFile = null;
      _streamingTool ??= _append(ChatItem('tool', ''));
      final b64 = p['deltaBase64'];
      if (b64 is String && b64.isNotEmpty) {
        try {
          _streamingTool!.text +=
              utf8.decode(base64.decode(b64), allowMalformed: true);
        } catch (_) {}
      }
    } else if (method.endsWith('fileChange/patchUpdated')) {
      final deltas = _fileDeltasFromChanges(p['changes']);
      if (deltas.isNotEmpty) {
        _streamingAssistant = null;
        _streamingReasoning = null;
        _streamingTool = null;
        _streamingFile ??= _append(ChatItem('file', '', fileDeltas: deltas));
        _streamingFile!.fileDeltas = deltas;
        _streamingFile!.text = _fileSummaryText(deltas);
      }
    } else if (method.endsWith('fileChange/outputDelta')) {
      _streamingFile ??= _append(ChatItem('file', '文件改动'));
      _streamingFile!.text += _str(p['delta']);
    } else if (method.endsWith('turn/started') || method == 'turnStarted') {
      if (tid.isNotEmpty) activeThreadIds.add(tid);
      turnRunning = true;
      _streamingReasoning = null;
      _streamingFile = null;
      _sawReasoningSummary = false;
    } else if (method.endsWith('turn/completed') ||
        method.endsWith('turn/failed') ||
        method == 'turnCompleted') {
      if (tid.isNotEmpty) activeThreadIds.remove(tid);
      turnRunning =
          currentThreadId != null && activeThreadIds.contains(currentThreadId);
      _streamingAssistant = null;
      _streamingTool = null;
      _streamingReasoning = null;
      _streamingFile = null;
      _streamingFile = null;
      if (method.endsWith('turn/completed') || method == 'turnCompleted') {
        unawaited(HapticFeedback.mediumImpact());
      }
    }
  }

  String _commandFromStartedItem(dynamic item) {
    if (item is! Map) return '';
    if (item['type'] != 'commandExecution') return '';
    final cmd = item['command'];
    if (cmd is String) return _cleanCommand(cmd);
    if (cmd is List) return _cleanCommand(cmd.whereType<String>().join(' '));
    final actions = item['commandActions'];
    if (actions is List && actions.isNotEmpty) {
      final first = actions.first;
      if (first is Map && first['command'] is String) {
        return _cleanCommand(first['command'] as String);
      }
    }
    return '';
  }

  String _cleanCommand(String raw) {
    final s = raw.replaceAll('\r', '').trim();
    if (s.isEmpty) return '';
    return s.split('\n').where((line) => line.trim().isNotEmpty).join('\n');
  }

  ChatItem _append(ChatItem it) {
    items.add(it);
    return it;
  }

  /// Parse an earlier-history page and graft it in front of the existing
  /// transcript in one synchronous frame. Unlike _loadHistory this never
  /// resets: the visible tail, streaming blocks and pending approval all stay.
  void _prependHistory(dynamic threadMsg) {
    final keep = List<ChatItem>.from(items);
    items.clear();
    var failed = 0;
    try {
      dynamic obj = threadMsg;
      if (obj is Map && obj['thread'] is Map) obj = obj['thread'];
      final turns = obj is Map ? obj['turns'] : null;
      if (turns is List) {
        for (final t in turns) {
          try {
            _collect(t);
          } catch (_) {
            failed++;
          }
        }
      }
    } catch (_) {}
    items.addAll(keep);
    lastReadStatus =
        failed == 0 ? _readStatus() : '${_readStatus()} · $failed 条解析失败';
  }

  void _loadHistory(dynamic threadMsg) {
    _resetChat();
    // Fault isolation is per-turn: one malformed item must only lose itself,
    // never truncate the rest of the transcript. And the status line is updated
    // unconditionally — the old single try/catch around everything left a stale
    // '读取中…' whenever any turn threw.
    var failed = 0;
    try {
      dynamic obj = threadMsg;
      if (obj is Map && obj['thread'] is Map) obj = obj['thread'];
      final turns = obj is Map ? obj['turns'] : null;
      if (turns is List) {
        for (final t in turns) {
          try {
            _collect(t);
          } catch (_) {
            failed++;
          }
        }
      }
    } catch (_) {}
    lastReadStatus =
        failed == 0 ? _readStatus() : '${_readStatus()} · $failed 条解析失败';
  }

  String _readStatus() {
    final user = items.where((e) => e.role == 'user').length;
    final assistant = items.where((e) => e.role == 'assistant').length;
    final media =
        items.where((e) => e.role == 'media' || e.role == 'image').length;
    final files = items.where((e) => e.role == 'file').fold<int>(
        0, (n, e) => n + (e.fileDeltas.isEmpty ? 1 : e.fileDeltas.length));
    final now = DateTime.now();
    final hh = now.hour.toString().padLeft(2, '0');
    final mm = now.minute.toString().padLeft(2, '0');
    final ss = now.second.toString().padLeft(2, '0');
    return '已读取 $hh:$mm:$ss · 用户 $user · 回复 $assistant · 媒体 $media · 文件 $files';
  }

  void _collect(dynamic node) {
    if (node is Map) {
      switch (node['type']) {
        case 'userMessage':
          final text = _userText(node);
          if (text.isNotEmpty) items.add(ChatItem('user', text));
          for (final img in _localImages(node['content'])) {
            items.add(img);
          }
          return;
        case 'agentMessage':
          items.add(ChatItem('assistant', _text(node)));
          return;
        case 'reasoning':
          final text = _reasoningText(node);
          if (text.isNotEmpty) items.add(ChatItem('reasoning', text));
          return;
        case 'fileChange':
          final deltas = _fileDeltasFromChanges(node['changes']);
          items.add(
              ChatItem('file', _fileSummaryText(deltas), fileDeltas: deltas));
          return;
        case 'mcpToolCall':
          items.add(
              ChatItem('tool', '🔧 ${_text(node, fallback: 'tool call')}'));
          return;
        case 'commandExecution':
          final cmd = _commandFromStartedItem(node);
          if (cmd.isNotEmpty) {
            items.add(ChatItem('command', cmd));
          } else {
            items.add(ChatItem('tool', '已运行命令'));
          }
          return;
        case 'imageGeneration':
          final b64 = _str(node['result']);
          final caption = _str(node['revisedPrompt']);
          final rpath = _str(node['resultPath']);
          if (rpath.isNotEmpty) {
            // The bridge stripped the multi-MB inline base64 and staged the
            // image; render it as path media — fetched lazily via /file only
            // when it scrolls into view.
            items.add(ChatItem('media', rpath));
          } else if (b64.isNotEmpty) {
            items.add(ChatItem('image', caption, imageB64: b64));
          } else {
            items.add(ChatItem('media', '🖼️ 生成的图片（生成中…）'));
          }
          return;
        case 'image':
          var url = _str(node['image_url']);
          if (url.isEmpty) url = _str(node['imageUrl']);
          if (url.startsWith('data:image') && url.contains(',')) {
            items.add(ChatItem('image', '', imageB64: url.split(',').last));
          } else if (url.startsWith('/')) {
            // Bridge-staged absolute path (stripped inline data) → lazy /file.
            items.add(ChatItem('media', url));
          } else {
            items.add(ChatItem('media', '🖼️ 图片'));
          }
          return;
        case 'localImage':
          final path = _str(node['path']);
          if (path.isNotEmpty) {
            items.add(ChatItem('media', path));
          }
          return;
        // Minimal placeholders for the remaining ThreadItem variants so no turn
        // is ever completely invisible — an all-placeholder tail still renders
        // SOMETHING instead of a confusing empty transcript.
        case 'plan':
          items.add(ChatItem('reasoning', _text(node, fallback: '📋 计划')));
          return;
        case 'webSearch':
          items.add(ChatItem('tool', '🔍 已搜索网页'));
          return;
        case 'dynamicToolCall':
          items.add(ChatItem('tool', '🔧 工具调用'));
          return;
        case 'collabAgentToolCall':
          items.add(ChatItem('tool', '🤝 子代理任务'));
          return;
        case 'hookPrompt':
          items.add(ChatItem('tool', '⚙️ Hook 提示'));
          return;
        case 'imageView':
          items.add(ChatItem('media', '🖼️ 图片查看'));
          return;
        case 'enteredReviewMode':
          items.add(ChatItem('system', '进入审查模式'));
          return;
        case 'exitedReviewMode':
          items.add(ChatItem('system', '退出审查模式'));
          return;
        case 'contextCompaction':
          items.add(ChatItem('system', '上下文已压缩'));
          return;
      }
      for (final v in node.values) {
        _collect(v);
      }
    } else if (node is List) {
      for (final v in node) {
        _collect(v);
      }
    }
  }

  String _text(Map node, {String fallback = ''}) {
    final buf = StringBuffer();
    void walk(dynamic n, int depth) {
      if (depth > 4) return;
      if (n is Map) {
        final t = n['text'];
        if (t is String) buf.write(t);
        n.forEach((k, v) {
          if (k != 'text') walk(v, depth + 1);
        });
      } else if (n is List) {
        for (final e in n) {
          walk(e, depth + 1);
        }
      }
    }

    walk(node, 0);
    final s = buf.toString().trim();
    return s.isNotEmpty ? s : fallback;
  }

  String _userText(Map node) => _sanitizeUserText(_text(node));

  List<ChatItem> _localImages(dynamic content) {
    if (content is! List) return const [];
    final out = <ChatItem>[];
    for (final item in content) {
      if (item is Map && item['type'] == 'localImage') {
        final path = _str(item['path']);
        if (path.isNotEmpty) out.add(ChatItem('media', path));
      }
    }
    return out;
  }

  String _sanitizeUserText(String raw) {
    var s = raw.replaceAll('\r', '').trim();
    if (s.isEmpty) return s;

    for (final marker in const [
      '## My request for Codex:',
      '## My request for Codex：',
      '## My request:',
      '## My request：',
    ]) {
      final i = s.indexOf(marker);
      if (i >= 0) {
        return s.substring(i + marker.length).trim();
      }
    }

    final lines = s.split('\n');
    final kept = <String>[];
    var skippingFiles = false;
    for (final line in lines) {
      final t = line.trim();
      if (t == '# Files mentioned by the user:' ||
          t == '# Files mentioned by the user：') {
        skippingFiles = true;
        continue;
      }
      if (skippingFiles) {
        final isAttachmentLine =
            t.startsWith('## ') && t.contains('/tmp/codex-remote-attachments/');
        final isTmpPath = t.startsWith('/tmp/codex-remote-attachments/') ||
            t.contains('codex-remote-attachments/');
        if (t.isEmpty || isAttachmentLine || isTmpPath) {
          continue;
        }
        skippingFiles = false;
      }
      kept.add(line);
    }

    return kept.join('\n').trim();
  }

  String _reasoningText(Map node) {
    final parts = <String>[];
    void addStrings(dynamic v) {
      if (v is String && v.trim().isNotEmpty) {
        parts.add(v.trim());
      } else if (v is List) {
        for (final e in v) {
          if (e is String && e.trim().isNotEmpty) {
            parts.add(e.trim());
          } else if (e is Map) {
            addStrings(e['text']);
          }
        }
      }
    }

    addStrings(node['summary']);
    if (parts.isEmpty) addStrings(node['content']);
    if (parts.isNotEmpty) return parts.join('\n\n');
    return _text(node);
  }

  List<FileDelta> _fileDeltasFromChanges(dynamic changes) {
    if (changes is! List) return const [];
    final out = <FileDelta>[];
    for (final c in changes) {
      if (c is! Map) continue;
      final path = _str(c['path']);
      if (path.isEmpty) continue;
      final diff = _str(c['diff']);
      final counts = _countDiff(diff);
      // Per the app-server schema, FileUpdateChange.kind is an OBJECT
      // (PatchChangeKind {"type":"add"|"delete"|"update"}), NOT a string. The
      // old `as String?` cast threw a TypeError on every fileChange, which the
      // _loadHistory catch swallowed — silently truncating the transcript and
      // leaving the "读取中…" banner forever (the "sessions won't load" bug).
      final k = c['kind'];
      final kind = k is String ? k : (k is Map ? _str(k['type']) : '');
      out.add(FileDelta(
        path: path,
        additions: counts.$1,
        deletions: counts.$2,
        kind: kind,
        diff: diff,
      ));
    }
    return out;
  }

  /// Tolerant string read: protocol fields evolve (string ↔ object), and a
  /// hard `as String?` cast turning into a TypeError mid-parse truncates the
  /// whole transcript. Non-strings read as ''.
  String _str(dynamic v) => v is String ? v : '';

  (int, int) _countDiff(String diff) {
    var add = 0;
    var del = 0;
    for (final line in diff.split('\n')) {
      if (line.startsWith('+++') || line.startsWith('---')) continue;
      if (line.startsWith('+')) add++;
      if (line.startsWith('-')) del++;
    }
    return (add, del);
  }

  String _fileSummaryText(List<FileDelta> deltas) {
    if (deltas.isEmpty) return '文件改动';
    final add = deltas.fold<int>(0, (n, d) => n + d.additions);
    final del = deltas.fold<int>(0, (n, d) => n + d.deletions);
    return '已更改 ${deltas.length} 个文件 +$add -$del';
  }

  void _onError(Object e) {
    error = e.toString();
    state = ConnState.error;
    turnRunning = false;
    _abortPendingRead();
    notifyListeners();
    _scheduleReconnect();
  }

  void _onDone() {
    if (state != ConnState.error) state = ConnState.disconnected;
    turnRunning = false;
    _abortPendingRead();
    notifyListeners();
    _scheduleReconnect();
  }

  /// The link died with a read in flight: its response is never coming, so
  /// release the pending-read state. Without this the '读取中…' banner (and a
  /// disabled load-earlier spinner) survived the disconnect indefinitely; the
  /// reconnect's _onReady re-read re-arms the state cleanly.
  void _abortPendingRead() {
    if (!readingThread && !loadingEarlier) return;
    readingThread = false;
    loadingEarlier = false;
    _readSentAt = null;
    lastReadStatus = '连接中断';
  }
}
