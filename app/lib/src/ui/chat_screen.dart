import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart' show ScrollDirection;
import 'package:flutter/services.dart';
import 'package:flutter_markdown/flutter_markdown.dart';
import 'package:video_player/video_player.dart';

import '../../main.dart';
import '../services/bridge_client.dart';
import 'chat_composer.dart';
import 'theme.dart';

/// A thread (conversation). When [threadId] is null it's a fresh thread and shows
/// the "我们该做什么?" prompt with a project (cwd) selector; once messages exist it
/// becomes a transcript. The composer is shared by both states.
///
/// The transcript mirrors the official Codex layout: the user's prompt sits in a
/// right-aligned gray bubble, the model's intermediate steps (tool calls, reasoning)
/// fold into a tappable "前 N 条消息 ›" row, and only the final answer is shown in
/// full — with file changes summarised as a compact centered pill.
class ChatScreen extends StatefulWidget {
  final String? threadId;
  final String title;
  final String? presetCwd;
  const ChatScreen(
      {super.key, required this.threadId, this.title = '新线程', this.presetCwd});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> with WidgetsBindingObserver {
  final _scroll = ScrollController();
  String? _cwd; // selected working directory for a new thread
  final Set<int> _expanded = {}; // turn-keys whose "前 N 条消息" group is open
  bool _pinPending = false; // pin hard to the bottom once this thread loads
  bool _showJumpLatest = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _cwd = widget.presetCwd;
    // Opening an existing thread: jump to the newest message once it loads.
    _pinPending = widget.threadId != null;
    _ensureThreadLoaded();
  }

  @override
  void didUpdateWidget(covariant ChatScreen oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.threadId != widget.threadId) {
      _pinPending = widget.threadId != null;
      _ensureThreadLoaded();
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _scroll.dispose();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (!_isTopRoute) return;
    if (state != AppLifecycleState.resumed) return;
    // RootScreen owns resume sync (syncNow → list + refreshCurrentThread); a
    // second sync here used to stack 3 reads + 2 lists per unlock and blank the
    // visible transcript via openThread's _resetChat. We only re-arm the
    // bottom-pin so the refreshed history lands scrolled to the newest message.
    _pinPending = true;
  }

  bool get _isTopRoute => ModalRoute.of(context)?.isCurrent ?? true;

  void _ensureThreadLoaded() {
    final tid = widget.threadId;
    if (tid == null || tid.isEmpty) return;
    _pinPending = true;
    // The session-list tap already called openThread synchronously before
    // pushing this screen — skip the redundant second read (it doubled every
    // open's multi-MB transfer and reset the just-arriving transcript).
    // openThread's own in-flight dedup backstops any remaining race.
    if (bridge.currentThreadId == tid &&
        (bridge.readingThread || bridge.items.isNotEmpty)) {
      return;
    }
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_isTopRoute) return;
      bridge.openThread(tid);
    });
  }

  void _scrollToBottom() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_scroll.hasClients) return;
      // Conditional setState — an unconditional one here re-entered build,
      // which re-triggered this method, which setState'd again… a rebuild
      // loop that re-armed animateTo every frame. Each animateTo REPLACES the
      // user's in-progress drag activity, so the list refused to scroll.
      if (_showJumpLatest) setState(() => _showJumpLatest = false);
      final min = _scroll.position.minScrollExtent;
      if ((_scroll.position.pixels - min).abs() < 2) return; // already there
      _scroll.animateTo(
        min,
        duration: const Duration(milliseconds: 180),
        curve: Curves.easeOut,
      );
    });
  }

  Widget _jumpLatestButton() {
    return Positioned(
      right: 18,
      bottom: 18,
      child: Material(
        color: Colors.transparent,
        child: InkWell(
          onTap: _scrollToBottom,
          borderRadius: BorderRadius.circular(22),
          child: Container(
            height: 40,
            padding: const EdgeInsets.symmetric(horizontal: 12),
            decoration: BoxDecoration(
              color: Cx.ink,
              borderRadius: BorderRadius.circular(22),
              boxShadow: [
                BoxShadow(
                  color: Colors.black.withOpacity(0.16),
                  blurRadius: 18,
                  offset: const Offset(0, 8),
                ),
              ],
            ),
            child: const Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(Icons.keyboard_arrow_down_rounded,
                    size: 20, color: Colors.white),
                SizedBox(width: 4),
                Text('最新',
                    style: TextStyle(
                        color: Colors.white,
                        fontSize: 14,
                        fontWeight: FontWeight.w600)),
              ],
            ),
          ),
        ),
      ),
    );
  }

  /// On initial thread load, jump once to the latest message. Do not keep
  /// re-pinning: repeated jumps fight the user's finger and make the page feel
  /// frozen.
  void _pinToBottom() {
    if (!mounted) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      if (_scroll.hasClients) {
        final min = _scroll.position.minScrollExtent;
        if ((_scroll.position.pixels - min).abs() > 1) _scroll.jumpTo(min);
        if (_showJumpLatest) setState(() => _showJumpLatest = false);
      }
    });
  }

  void _onSend(String text, List<String> images, String effort, String model,
      String speed, String approval) {
    bridge.sendPrompt(text,
        images: images,
        effort: effort,
        model: model,
        speed: speed,
        approval: approval,
        cwd: _cwd);
    _scrollToBottom();
  }

  void _composeNew() {
    bridge.newThread();
    Navigator.of(context).pushReplacement(
      MaterialPageRoute(builder: (_) => const ChatScreen(threadId: null)),
    );
  }

  void _moreMenu() {
    final tid = bridge.currentThreadId;
    showModalBottomSheet<void>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (tid != null)
              ListTile(
                leading:
                    const Icon(Icons.refresh_rounded, color: Cx.textPrimary),
                title:
                    const Text('刷新会话', style: TextStyle(color: Cx.textPrimary)),
                onTap: () {
                  Navigator.pop(ctx);
                  _pinPending = true;
                  bridge.openThread(tid);
                },
              ),
            if (tid != null)
              ListTile(
                leading: const Icon(Icons.tag_rounded, color: Cx.textPrimary),
                title: const Text('复制线程 ID',
                    style: TextStyle(color: Cx.textPrimary)),
                onTap: () {
                  Navigator.pop(ctx);
                  Clipboard.setData(ClipboardData(text: tid));
                  _toast('已复制线程 ID');
                },
              ),
            ListTile(
              leading:
                  const Icon(Icons.list_alt_rounded, color: Cx.textPrimary),
              title:
                  const Text('回到列表', style: TextStyle(color: Cx.textPrimary)),
              onTap: () {
                Navigator.pop(ctx);
                Navigator.of(context).maybePop();
              },
            ),
          ],
        ),
      ),
    );
  }

  void _toast(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(msg),
        backgroundColor: Cx.ink,
        behavior: SnackBarBehavior.floating,
        duration: const Duration(milliseconds: 900),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final machineLabel = machines.active?.label ?? '';
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        leadingWidth: 52,
        leading: Padding(
          padding: const EdgeInsets.only(left: 8),
          child: CxCircleButton(Icons.arrow_back_ios_new_rounded,
              onTap: () => Navigator.of(context).maybePop(), tooltip: '返回'),
        ),
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(widget.title,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style:
                    const TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
            if (machineLabel.isNotEmpty)
              Text(machineLabel,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style:
                      const TextStyle(fontSize: 12, color: Cx.textSecondary)),
          ],
        ),
        actions: [
          ListenableBuilder(
            listenable: bridge,
            builder: (context, _) => bridge.turnRunning
                ? Padding(
                    padding: const EdgeInsets.only(right: 2),
                    child: TextButton.icon(
                      onPressed: bridge.interrupt,
                      icon: const Icon(Icons.stop_rounded,
                          size: 18, color: Cx.offline),
                      label:
                          const Text('停止', style: TextStyle(color: Cx.offline)),
                    ),
                  )
                : const SizedBox.shrink(),
          ),
          CxCircleButton(Icons.edit_square,
              onTap: _composeNew, tooltip: '新线程', size: 38),
          const SizedBox(width: 8),
          CxCircleButton(Icons.more_horiz_rounded,
              onTap: _moreMenu, tooltip: '更多', size: 38),
          const SizedBox(width: 10),
        ],
      ),
      body: Column(
        children: [
          const Divider(height: 1),
          ListenableBuilder(
            listenable: bridge,
            builder: (context, _) {
              if (widget.threadId == null) return const SizedBox.shrink();
              final text =
                  bridge.readingThread ? '读取中…' : bridge.lastReadStatus;
              if (text.isEmpty) return const SizedBox.shrink();
              return _syncStatus(text, bridge.readingThread);
            },
          ),
          Expanded(
            child: Stack(
              children: [
                ListenableBuilder(
                  listenable: bridge,
                  builder: (context, _) {
                    if (widget.threadId != null &&
                        bridge.currentThreadId != widget.threadId) {
                      _ensureThreadLoaded();
                      return _loadingTranscript();
                    }
                    final items = bridge.items;
                    final running = bridge.turnRunning;
                    // Show the bottom "正在思考…" spinner only in the gaps — before any
                    // content, or between steps. While a reasoning/answer block is
                    // actively streaming, that block itself conveys progress.
                    final last = items.isEmpty ? null : items.last;
                    final thinking = running &&
                        (last == null ||
                            (last.role != 'assistant' &&
                                last.role != 'reasoning') ||
                            last.text.isEmpty);
                    final approval = bridge.pendingApproval;

                    if (items.isEmpty && !thinking && approval == null) {
                      // Opening an existing thread: its history is still streaming in
                      // over the relay. Show a loader instead of the "new thread" hero,
                      // which would otherwise flash for a beat then vanish — the jank
                      // that read as "slow loading". But once the read completed and
                      // yielded zero renderable items, say so — an eternal spinner here
                      // is indistinguishable from "stuck loading".
                      if (widget.threadId == null) return _newThreadHero();
                      return bridge.readingThread
                          ? _loadingTranscript()
                          : _emptyTranscript();
                    }

                    if (_pinPending && items.isNotEmpty) {
                      // First load of an existing thread: jump once to the
                      // newest message.
                      _pinPending = false;
                      _pinToBottom();
                    }
                    // NO per-rebuild follow logic beyond the pin: with
                    // reverse:true the viewport auto-anchors to the newest
                    // content while at offset 0, and re-running animateTo on
                    // every streaming rebuild replaced the user's drag activity
                    // each frame — the list felt frozen ("无法滑动") whenever a
                    // finger touched it near the bottom.

                    final rows = _buildTranscript(items, running);
                    // Heavy session opened with only its tail: offer to pull the
                    // rest. Sits at the very top so it scrolls in above the oldest
                    // shown message instead of covering the live conversation.
                    if (bridge.historyTruncated) {
                      rows.insert(0, _loadEarlierHeader());
                    }
                    if (thinking) rows.add(const _Thinking());
                    if (approval != null) rows.add(_approvalCard(approval));

                    return NotificationListener<UserScrollNotification>(
                      // A user-driven drag cancels the open-time bottom-pin so the list
                      // scrolls freely immediately, instead of being yanked back down.
                      // (Programmatic jumps don't emit UserScrollNotification, so this
                      // only trips on a real finger drag/fling.)
                      onNotification: (n) {
                        if (n.direction != ScrollDirection.idle &&
                            _scroll.hasClients) {
                          final off = _scroll.position.pixels -
                              _scroll.position.minScrollExtent;
                          if (off > 180 && !_showJumpLatest) {
                            setState(() => _showJumpLatest = true);
                          } else if (off <= 180 && _showJumpLatest) {
                            // Hide it again when the user scrolls back down
                            // themselves (only _scrollToBottom cleared it before).
                            setState(() => _showJumpLatest = false);
                          }
                        }
                        return false;
                      },
                      // SelectionArea + plain Text rows instead of per-block
                      // SelectableText: on iOS, SelectableText competes with the
                      // scrollable for the drag gesture, so the transcript
                      // wouldn't scroll whenever the finger landed on text —
                      // which in a chat is most of the screen. (Android's
                      // gesture arena resolves the same fight in the list's
                      // favor, which is why only iOS felt stuck.) SelectionArea
                      // selects on long-press only, so plain drags always
                      // scroll — and selection can span blocks as a bonus.
                      child: SelectionArea(
                        child: ListView.builder(
                          controller: _scroll,
                          reverse: true,
                          padding: const EdgeInsets.fromLTRB(16, 12, 16, 16),
                          // Lazy: only on-screen rows lay out, so a long transcript paints
                          // fast on open instead of building every markdown block up front.
                          itemCount: rows.length,
                          itemBuilder: (_, i) => rows[rows.length - 1 - i],
                        ),
                      ),
                    );
                  },
                ),
                if (_showJumpLatest) _jumpLatestButton(),
              ],
            ),
          ),
          ChatComposer(onSend: _onSend),
        ],
      ),
    );
  }

  Widget _syncStatus(String text, bool loading) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.fromLTRB(18, 7, 18, 7),
      decoration: const BoxDecoration(
        color: Color(0xFFF8F8F8),
        border: Border(bottom: BorderSide(color: Cx.border, width: 1)),
      ),
      child: Row(
        children: [
          if (loading) ...[
            const SizedBox(
              width: 12,
              height: 12,
              child: CircularProgressIndicator(
                  strokeWidth: 1.5, color: Cx.textFaint),
            ),
            const SizedBox(width: 8),
          ],
          Expanded(
            child: Text(
              text,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(color: Cx.textFaint, fontSize: 11.5),
            ),
          ),
        ],
      ),
    );
  }

  // ---- transcript grouping ----

  /// Walk the flat item list and group it into turns. A "user" message renders
  /// on its own (right-aligned); the run of model items after it folds its
  /// intermediate steps into a "前 N 条消息" row and shows only the final answer.
  /// The trailing run is left fully expanded while a turn is [running] so the
  /// user can watch it stream live.
  List<Widget> _buildTranscript(List<ChatItem> items, bool running) {
    final out = <Widget>[];
    final n = items.length;
    var i = 0;
    while (i < n) {
      final role = items[i].role;
      if (role == 'user') {
        out.add(_userBubble(items[i]));
        i++;
        continue;
      }
      if (role == 'image' || role == 'media') {
        out.add(_row(items[i]));
        i++;
        continue;
      }
      final runStart = i;
      final run = <ChatItem>[];
      while (i < n &&
          items[i].role != 'user' &&
          items[i].role != 'image' &&
          items[i].role != 'media') {
        run.add(items[i]);
        i++;
      }
      final live = (i >= n) && running;
      out.addAll(_renderRun(run, runStart, live));
    }
    return out;
  }

  bool _isAnswer(ChatItem r) =>
      r.role != 'tool' &&
      r.role != 'command' &&
      r.role != 'file' &&
      r.role != 'system' &&
      r.role != 'reasoning' &&
      r.text.trim().isNotEmpty;

  List<Widget> _renderRun(List<ChatItem> run, int key, bool live) {
    // While streaming, show every step in order so the user sees it live.
    if (live) return run.map(_row).toList();

    var answerIdx = -1;
    for (var k = 0; k < run.length; k++) {
      if (_isAnswer(run[k])) answerIdx = k;
    }

    final hidden = <ChatItem>[];
    final files = <ChatItem>[];
    final systems = <ChatItem>[];
    for (var k = 0; k < run.length; k++) {
      if (k == answerIdx) continue;
      final r = run[k];
      if (r.role == 'file') {
        files.add(r);
      } else if (r.role == 'system') {
        systems.add(r);
      } else {
        hidden.add(r);
      }
    }

    final out = <Widget>[];
    if (hidden.isNotEmpty) out.add(_collapseRow(key, hidden));
    if (answerIdx >= 0) out.add(_answerBlock(run[answerIdx]));
    if (files.isNotEmpty) out.add(_fileChangeCard(key, files));
    for (final s in systems) {
      out.add(_row(s));
    }
    return out;
  }

  Widget _collapseRow(int key, List<ChatItem> hidden) {
    final open = _expanded.contains(key);
    final label = _hiddenLabel(hidden);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        InkWell(
          onTap: () =>
              setState(() => open ? _expanded.remove(key) : _expanded.add(key)),
          child: Padding(
            padding: const EdgeInsets.symmetric(vertical: 10),
            child: Row(
              children: [
                Text('前 ${hidden.length} 条消息',
                    style: const TextStyle(
                        color: Cx.textSecondary,
                        fontSize: 14,
                        fontWeight: FontWeight.w500)),
                if (label != null) ...[
                  const SizedBox(width: 8),
                  Flexible(
                    child: Text(label,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style:
                            const TextStyle(color: Cx.textFaint, fontSize: 13)),
                  ),
                ],
                const SizedBox(width: 3),
                Icon(
                    open
                        ? Icons.expand_more_rounded
                        : Icons.chevron_right_rounded,
                    size: 18,
                    color: Cx.textFaint),
              ],
            ),
          ),
        ),
        if (open)
          Padding(
            padding: const EdgeInsets.only(bottom: 4),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: hidden.map(_row).toList(),
            ),
          ),
      ],
    );
  }

  String? _hiddenLabel(List<ChatItem> hidden) {
    final commands = hidden.where((e) => e.role == 'command').toList();
    if (commands.length > 1) return '已运行 ${commands.length} 条命令';
    if (commands.length == 1) {
      return '已运行 ${_shortCommand(commands.single.text)}';
    }
    if (hidden.any((e) => e.role == 'tool')) return '已运行命令';
    if (hidden.any((e) => e.role == 'reasoning')) return '思考过程';
    return null;
  }

  // Shown while an existing thread's history is still arriving over the relay, so
  // the screen gives immediate feedback instead of flashing the new-thread hero.
  Widget _loadingTranscript() {
    return const Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          SizedBox(
            width: 22,
            height: 22,
            child:
                CircularProgressIndicator(strokeWidth: 2, color: Cx.textFaint),
          ),
          SizedBox(height: 14),
          Text('正在加载会话…', style: TextStyle(color: Cx.textFaint, fontSize: 14)),
        ],
      ),
    );
  }

  // The read completed but produced nothing renderable (e.g. a thread whose
  // tail is all tool/update records). Distinct from the loading spinner so the
  // user sees a settled state with a retry, not an infinite wait.
  Widget _emptyTranscript() {
    // A truncated thread whose 40-turn tail rendered nothing must offer the
    // FULL-history pull here — the load-earlier header lives inside the list,
    // which this empty state replaces, and a plain re-read of the same tail
    // would just reproduce the same emptiness.
    final truncated = bridge.historyTruncated;
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Text('此会话暂无可显示内容',
              style: TextStyle(color: Cx.textFaint, fontSize: 14)),
          const SizedBox(height: 10),
          TextButton(
            onPressed: truncated
                ? bridge.loadEarlier
                : () => bridge.refreshCurrentThread(force: true),
            child: Text(
              truncated ? '加载完整历史（共 ${bridge.historyTotal} 段对话）' : '重新加载',
              style: const TextStyle(color: Cx.textSecondary, fontSize: 13),
            ),
          ),
        ],
      ),
    );
  }

  // Top-of-list affordance shown when a heavy thread was opened with only its
  // tail. Tapping pulls the full transcript (slower, but on demand); the spinner
  // covers the round-trip, after which the bridge clears `historyTruncated` and
  // this row disappears.
  Widget _loadEarlierHeader() {
    final loading = bridge.loadingEarlier;
    // Disabled mid-turn: pulling full history would reset the live stream.
    final disabled = loading || bridge.turnRunning;
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Center(
        child: TextButton(
          onPressed: disabled ? null : bridge.loadEarlier,
          style: TextButton.styleFrom(
            foregroundColor: Cx.textFaint,
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
          ),
          child: loading
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(
                      strokeWidth: 2, color: Cx.textFaint),
                )
              : Text(
                  // historyOffset = 还在更早处、尚未加载的轮数（游标分页）。
                  bridge.historyOffset > 0
                      ? '↑ 加载更早消息（还有 ${bridge.historyOffset} 段）'
                      : '↑ 加载更早消息（完整 ${bridge.historyTotal} 段对话）',
                  style: const TextStyle(color: Cx.textFaint, fontSize: 13),
                ),
        ),
      ),
    );
  }

  // ---- new-thread hero ("我们该做什么?") ----

  Widget _newThreadHero() {
    return Center(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(32, 0, 32, 80),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Text('我们该做什么？',
                textAlign: TextAlign.center,
                style: TextStyle(
                    fontSize: 26,
                    fontWeight: FontWeight.w600,
                    color: Cx.textPrimary)),
            const SizedBox(height: 14),
            _projectSelector(),
          ],
        ),
      ),
    );
  }

  Widget _projectSelector() {
    return GestureDetector(
      onTap: _pickProject,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
        decoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(20),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.folder_open_rounded,
                size: 18, color: Cx.textSecondary),
            const SizedBox(width: 8),
            Text(_cwdLabel(_cwd),
                style: const TextStyle(
                    color: Cx.textPrimary,
                    fontSize: 15,
                    fontWeight: FontWeight.w500)),
            const SizedBox(width: 4),
            const Icon(Icons.unfold_more_rounded,
                size: 16, color: Cx.textFaint),
          ],
        ),
      ),
    );
  }

  void _pickProject() {
    final cwds = <String>{};
    for (final s in bridge.sessions) {
      if (s.cwd.isNotEmpty) cwds.add(s.cwd);
    }
    final list = cwds.toList()..sort();
    showModalBottomSheet<void>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: ListView(
          shrinkWrap: true,
          padding: const EdgeInsets.symmetric(vertical: 8),
          children: [
            _projectOption(ctx, null, '默认目录'),
            for (final c in list) _projectOption(ctx, c, _cwdLabel(c)),
          ],
        ),
      ),
    );
  }

  Widget _projectOption(BuildContext ctx, String? cwd, String label,
      {String? sub}) {
    final sel = _cwd == cwd;
    return ListTile(
      leading: Icon(
          cwd == null ? Icons.home_outlined : Icons.folder_open_rounded,
          color: Cx.textSecondary),
      title: Text(label, style: const TextStyle(color: Cx.textPrimary)),
      subtitle: sub == null
          ? null
          : Text(sub,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(
                  color: Cx.textFaint, fontSize: 12, fontFamily: Cx.mono)),
      trailing: sel ? const Icon(Icons.check_rounded, color: Cx.ink) : null,
      onTap: () {
        setState(() => _cwd = cwd);
        Navigator.pop(ctx);
      },
    );
  }

  String _cwdLabel(String? cwd) {
    if (cwd == null || cwd.isEmpty) return '默认目录';
    // Split on both / and \ so Windows paths (C:\Users\me\proj) reduce to "proj".
    final parts =
        cwd.split(RegExp(r'[/\\]')).where((e) => e.isNotEmpty).toList();
    return parts.isEmpty ? cwd : parts.last;
  }

  // ---- transcript rows ----

  Widget _userBubble(ChatItem it) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Align(
        alignment: Alignment.centerRight,
        child: Container(
          constraints: BoxConstraints(
              maxWidth: MediaQuery.of(context).size.width * 0.82),
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
          decoration: BoxDecoration(
            color: Cx.surface,
            borderRadius: BorderRadius.circular(18),
          ),
          // Plain Text — the transcript's SelectionArea handles copying.
          // SelectableText here blocked iOS scrolling (see the ListView).
          child: Text(it.text,
              style: const TextStyle(
                  fontSize: 15.5, height: 1.4, color: Cx.textPrimary)),
        ),
      ),
    );
  }

  /// Codex-style thinking row: quiet while live, readable when expanded later.
  Widget _reasoningBlock(ChatItem it) {
    final live = bridge.turnRunning &&
        bridge.items.isNotEmpty &&
        identical(it, bridge.items.last);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 7),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              live
                  ? const SizedBox(
                      width: 14,
                      height: 14,
                      child: CircularProgressIndicator(
                          strokeWidth: 1.5, color: Cx.textFaint),
                    )
                  : const Icon(Icons.psychology_outlined,
                      size: 15, color: Cx.textFaint),
              const SizedBox(width: 8),
              Text(live ? '正在思考' : '思考过程',
                  style: const TextStyle(color: Cx.textFaint, fontSize: 14)),
            ],
          ),
          if (it.text.trim().isNotEmpty) ...[
            const SizedBox(height: 7),
            Padding(
              padding: const EdgeInsets.only(left: 22),
              child: Text(
                it.text,
                style: const TextStyle(
                  color: Cx.textSecondary,
                  fontSize: 13.5,
                  height: 1.5,
                ),
              ),
            ),
          ],
        ],
      ),
    );
  }

  Widget _answerBlock(ChatItem it) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _markdown(it.text),
          const SizedBox(height: 4),
          _copyButton(it.text),
        ],
      ),
    );
  }

  Widget _copyButton(String text) {
    return InkWell(
      borderRadius: BorderRadius.circular(8),
      onTap: () {
        Clipboard.setData(ClipboardData(text: text));
        _toast('已复制');
      },
      child: const Padding(
        padding: EdgeInsets.all(4),
        child: Icon(Icons.content_copy_rounded, size: 15, color: Cx.textFaint),
      ),
    );
  }

  Widget _fileChangeCard(int key, List<ChatItem> files) {
    final deltas = _fileDeltas(files);
    final total = deltas.isNotEmpty ? deltas.length : files.length;
    final additions = deltas.fold<int>(0, (n, d) => n + d.additions);
    final deletions = deltas.fold<int>(0, (n, d) => n + d.deletions);
    final expandKey = -key.abs() - 1000;
    final open = _expanded.contains(expandKey);
    final visible = open ? deltas : deltas.take(3).toList();
    final hidden = deltas.length - visible.length;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Container(
        decoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(14),
          border: Border.all(color: Cx.border),
        ),
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(14, 12, 12, 10),
              child: Row(
                children: [
                  Expanded(
                    child: Text('已更改 $total 个文件',
                        style: const TextStyle(
                            color: Cx.textPrimary,
                            fontSize: 15,
                            fontWeight: FontWeight.w700)),
                  ),
                  if (additions > 0)
                    Text('+$additions',
                        style: const TextStyle(
                            color: Color(0xFF208A3C),
                            fontSize: 13,
                            fontWeight: FontWeight.w700)),
                  if (deletions > 0) ...[
                    const SizedBox(width: 6),
                    Text('-$deletions',
                        style: const TextStyle(
                            color: Color(0xFFC93B4A),
                            fontSize: 13,
                            fontWeight: FontWeight.w700)),
                  ],
                  const SizedBox(width: 6),
                  Icon(
                      open
                          ? Icons.expand_more_rounded
                          : Icons.chevron_right_rounded,
                      size: 20,
                      color: Cx.textFaint),
                ],
              ),
            ),
            if (deltas.isNotEmpty) const Divider(height: 1, color: Cx.border),
            for (final d in visible) _fileDeltaRow(d),
            if (hidden > 0)
              InkWell(
                onTap: () => setState(() => _expanded.add(expandKey)),
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(14, 11, 14, 12),
                  child: Row(
                    children: [
                      Text('查看另外 $hidden 个文件',
                          style: const TextStyle(
                              color: Cx.textSecondary,
                              fontSize: 14,
                              fontWeight: FontWeight.w500)),
                      const Spacer(),
                      const Icon(Icons.chevron_right_rounded,
                          size: 18, color: Cx.textFaint),
                    ],
                  ),
                ),
              )
            else if (open && deltas.length > 3)
              InkWell(
                onTap: () => setState(() => _expanded.remove(expandKey)),
                child: const Padding(
                  padding: EdgeInsets.fromLTRB(14, 10, 14, 12),
                  child: Row(
                    children: [
                      Text('收起文件',
                          style: TextStyle(
                              color: Cx.textSecondary,
                              fontSize: 14,
                              fontWeight: FontWeight.w500)),
                      Spacer(),
                      Icon(Icons.expand_less_rounded,
                          size: 18, color: Cx.textFaint),
                    ],
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }

  List<FileDelta> _fileDeltas(List<ChatItem> files) {
    final out = <FileDelta>[];
    for (final f in files) {
      if (f.fileDeltas.isNotEmpty) {
        out.addAll(f.fileDeltas);
      } else {
        final label = f.text.replaceFirst('📝 ', '').trim();
        if (label.isNotEmpty && label != 'file change' && label != '文件改动') {
          out.add(FileDelta(
              path: label, additions: 0, deletions: 0, kind: '', diff: ''));
        }
      }
    }
    return out;
  }

  Widget _fileDeltaRow(FileDelta d) {
    final name = _fileName(d.path);
    final dir = _parentPath(d.path);
    return InkWell(
      onTap: d.diff.trim().isEmpty ? null : () => _showDiffSheet(d),
      child: Container(
        decoration: const BoxDecoration(
          border: Border(top: BorderSide(color: Cx.border, width: 1)),
        ),
        padding: const EdgeInsets.fromLTRB(14, 10, 12, 10),
        child: Row(
          children: [
            const Icon(Icons.description_outlined,
                size: 17, color: Cx.textFaint),
            const SizedBox(width: 10),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(name,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(
                          color: Cx.textPrimary,
                          fontSize: 14,
                          fontWeight: FontWeight.w600)),
                  if (dir.isNotEmpty) ...[
                    const SizedBox(height: 2),
                    Text(dir,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                            color: Cx.textFaint, fontSize: 12, height: 1.25)),
                  ],
                ],
              ),
            ),
            if (d.additions > 0)
              Text('+${d.additions}',
                  style: const TextStyle(
                      color: Color(0xFF208A3C),
                      fontSize: 12.5,
                      fontWeight: FontWeight.w700)),
            if (d.deletions > 0) ...[
              const SizedBox(width: 7),
              Text('-${d.deletions}',
                  style: const TextStyle(
                      color: Color(0xFFC93B4A),
                      fontSize: 12.5,
                      fontWeight: FontWeight.w700)),
            ],
            if (d.diff.trim().isNotEmpty) ...[
              const SizedBox(width: 8),
              const Icon(Icons.open_in_full_rounded,
                  size: 15, color: Cx.textFaint),
            ],
          ],
        ),
      ),
    );
  }

  void _showDiffSheet(FileDelta d) {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: DraggableScrollableSheet(
          expand: false,
          initialChildSize: 0.72,
          minChildSize: 0.35,
          maxChildSize: 0.92,
          builder: (_, controller) => Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Center(
                child: Container(
                  width: 42,
                  height: 4,
                  margin: const EdgeInsets.only(top: 10, bottom: 12),
                  decoration: BoxDecoration(
                    color: Cx.border,
                    borderRadius: BorderRadius.circular(99),
                  ),
                ),
              ),
              Padding(
                padding: const EdgeInsets.fromLTRB(18, 0, 18, 10),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(_fileName(d.path),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                            color: Cx.textPrimary,
                            fontSize: 18,
                            fontWeight: FontWeight.w700)),
                    const SizedBox(height: 4),
                    Text(_parentPath(d.path),
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                            color: Cx.textFaint, fontSize: 12, height: 1.3)),
                  ],
                ),
              ),
              const Divider(height: 1, color: Cx.border),
              Expanded(
                child: SingleChildScrollView(
                  controller: controller,
                  padding: const EdgeInsets.all(14),
                  // SelectionArea+Text, not SelectableText — same iOS
                  // drag-stealing issue as the transcript: with SelectableText
                  // the diff sheet wouldn't scroll under a finger on text.
                  child: SelectionArea(
                    child: Text(
                      d.diff,
                      style: const TextStyle(
                        fontFamily: Cx.mono,
                        fontSize: 12.5,
                        height: 1.45,
                        color: Cx.textPrimary,
                      ),
                    ),
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  String _fileName(String path) {
    final parts = path.split(RegExp(r'[/\\]')).where((e) => e.isNotEmpty);
    return parts.isEmpty ? path : parts.last;
  }

  String _parentPath(String path) {
    final clean = path.replaceAll('\\', '/');
    final i = clean.lastIndexOf('/');
    if (i <= 0) return '';
    return clean.substring(0, i);
  }

  Widget _row(ChatItem it) {
    switch (it.role) {
      case 'user':
        return _userBubble(it);
      case 'reasoning':
        return _reasoningBlock(it);
      case 'command':
        return _commandRow(it.text);
      case 'tool':
        return _codeBlock('命令输出', it.text);
      case 'file':
        return _fileChangeCard(it.hashCode, [it]);
      case 'system':
        return _block(
          label: '系统',
          labelColor: Cx.offline,
          child: Text(it.text,
              style: const TextStyle(fontSize: 13, color: Cx.offline)),
        );
      case 'image':
        return _block(
            label: '图片', labelColor: Cx.textFaint, child: _imageView(it));
      case 'media':
        return _block(
          label: '媒体',
          labelColor: Cx.textFaint,
          child: _mediaView(it.text),
        );
      default:
        return Padding(
          padding: const EdgeInsets.symmetric(vertical: 8),
          child: _markdown(it.text),
        );
    }
  }

  Widget _block(
      {required String label,
      required Color labelColor,
      required Widget child}) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label,
              style: TextStyle(
                  color: labelColor,
                  fontSize: 12,
                  fontWeight: FontWeight.w600)),
          const SizedBox(height: 6),
          child,
        ],
      ),
    );
  }

  Widget _markdown(String text) {
    if (text.isEmpty) {
      return const Text('…',
          style: TextStyle(color: Cx.textPrimary, fontSize: 15));
    }
    return MarkdownBody(
      data: text,
      // NOT selectable:true — that wraps every block in SelectableText, which
      // on iOS steals the vertical drag and freezes transcript scrolling. The
      // transcript-level SelectionArea provides long-press copy instead
      // (flutter_markdown ≥0.7 renders Text.rich, which SelectionArea picks up).
      onTapLink: (_, __, ___) {},
      imageBuilder: (uri, title, alt) => _mdMedia(uri.toString()),
      styleSheet: MarkdownStyleSheet(
        p: const TextStyle(fontSize: 15.5, height: 1.5, color: Cx.textPrimary),
        a: const TextStyle(color: Cx.accent),
        strong:
            const TextStyle(fontWeight: FontWeight.w700, color: Cx.textPrimary),
        em: const TextStyle(fontStyle: FontStyle.italic, color: Cx.textPrimary),
        h1: const TextStyle(
            fontSize: 20, fontWeight: FontWeight.w700, color: Cx.textPrimary),
        h2: const TextStyle(
            fontSize: 18, fontWeight: FontWeight.w700, color: Cx.textPrimary),
        h3: const TextStyle(
            fontSize: 16, fontWeight: FontWeight.w600, color: Cx.textPrimary),
        listBullet: const TextStyle(fontSize: 15.5, color: Cx.textPrimary),
        code: const TextStyle(
            fontFamily: Cx.mono,
            fontSize: 13,
            color: Cx.textPrimary,
            backgroundColor: Cx.surface),
        codeblockPadding: const EdgeInsets.all(12),
        codeblockDecoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(10),
          border: Border.all(color: Cx.border, width: 1),
        ),
        blockquoteDecoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(8),
        ),
        horizontalRuleDecoration: const BoxDecoration(
          border: Border(top: BorderSide(color: Cx.border, width: 1)),
        ),
      ),
    );
  }

  Widget _mdMedia(String src) {
    final lower = src.toLowerCase();
    final isVideo = lower.endsWith('.mp4') ||
        lower.endsWith('.mov') ||
        lower.endsWith('.webm') ||
        lower.endsWith('.m4v');
    final furl = bridge.fileUrl(src);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: ClipRRect(
        borderRadius: BorderRadius.circular(10),
        child: isVideo
            ? VideoBubble(url: furl, name: src.split('/').last)
            : Image.network(
                furl,
                fit: BoxFit.cover,
                gaplessPlayback: true,
                errorBuilder: (c, e, s) {
                  Future.microtask(bridge.refreshMediaAuth);
                  return _mediaError('图片加载失败，正在刷新');
                },
              ),
      ),
    );
  }

  Widget _mediaError(String text) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
      decoration: BoxDecoration(
        color: Cx.surface,
        borderRadius: BorderRadius.circular(10),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.broken_image_outlined,
              size: 17, color: Cx.textFaint),
          const SizedBox(width: 8),
          Text(text,
              style: const TextStyle(color: Cx.textSecondary, fontSize: 13)),
        ],
      ),
    );
  }

  Widget _mediaView(String src) {
    final s = src.trim();
    // Only absolute paths and web URLs are fetchable media. Placeholder texts
    // (e.g. '🖼️ 图片', '🖼️ 生成的图片（生成中…）') used to be fileUrl'd into a
    // guaranteed-404 request and rendered as a broken-image card — show them
    // as plain text instead.
    final fetchable = s.startsWith('/') ||
        s.startsWith('http://') ||
        s.startsWith('https://') ||
        s.startsWith('file://');
    if (s.isEmpty || !fetchable) {
      return Container(
        padding: const EdgeInsets.all(14),
        decoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(10),
        ),
        child: Text(s.isEmpty ? '媒体无法加载' : s,
            style: const TextStyle(color: Cx.textSecondary, fontSize: 13)),
      );
    }
    return _mdMedia(s);
  }

  Widget _imageView(ChatItem it) {
    Uint8List? bytes;
    final b64 = it.imageB64;
    if (b64 != null && b64.isNotEmpty) {
      try {
        bytes = base64Decode(b64.replaceAll(RegExp(r'\s'), ''));
      } catch (_) {}
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (bytes != null)
          ClipRRect(
            borderRadius: BorderRadius.circular(10),
            child:
                Image.memory(bytes, fit: BoxFit.cover, gaplessPlayback: true),
          )
        else
          Container(
            padding: const EdgeInsets.all(14),
            decoration: BoxDecoration(
              color: Cx.surface,
              borderRadius: BorderRadius.circular(10),
            ),
            child: const Text('🖼️ 图片无法解码',
                style: TextStyle(color: Cx.textSecondary, fontSize: 13)),
          ),
        if (it.text.isNotEmpty) ...[
          const SizedBox(height: 6),
          Text(it.text,
              style: const TextStyle(
                  color: Cx.textFaint, fontSize: 12, height: 1.3)),
        ],
      ],
    );
  }

  Widget _codeBlock(String label, String text) {
    return _block(
      label: label,
      labelColor: Cx.textFaint,
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(10),
          border: Border.all(color: Cx.border, width: 1),
        ),
        child: Text(
          text.isEmpty ? '…' : text,
          style: const TextStyle(
              fontFamily: Cx.mono,
              fontSize: 13,
              height: 1.45,
              color: Cx.textSecondary),
        ),
      ),
    );
  }

  Widget _commandRow(String command) {
    final cmd = _shortCommand(command);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 7),
      child: Row(
        children: [
          const Icon(Icons.terminal_rounded, size: 16, color: Cx.textFaint),
          const SizedBox(width: 8),
          Expanded(
            child: Text('已运行 $cmd',
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(color: Cx.textSecondary, fontSize: 14)),
          ),
          const Icon(Icons.chevron_right_rounded,
              size: 18, color: Cx.textFaint),
        ],
      ),
    );
  }

  String _shortCommand(String command) {
    final line = command
        .replaceAll('\r', '')
        .split('\n')
        .map((e) => e.trim())
        .firstWhere((e) => e.isNotEmpty, orElse: () => '命令');
    if (line.length <= 72) return line;
    return '${line.substring(0, 69)}…';
  }

  Widget _approvalCard(Approval a) {
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 12),
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: const Color(0xFFFDF6EC),
        borderRadius: BorderRadius.circular(14),
        border: Border.all(color: Cx.warn.withOpacity(0.5)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Row(children: [
            Icon(Icons.shield_outlined, size: 16, color: Cx.warn),
            SizedBox(width: 6),
            Text('需要你批准',
                style: TextStyle(color: Cx.warn, fontWeight: FontWeight.w600)),
          ]),
          const SizedBox(height: 4),
          Text(a.method,
              style: const TextStyle(fontSize: 11, color: Cx.textFaint)),
          const SizedBox(height: 8),
          Text(a.command,
              style: const TextStyle(
                  fontFamily: Cx.mono, fontSize: 13, color: Cx.textPrimary)),
          const SizedBox(height: 12),
          Row(
            mainAxisAlignment: MainAxisAlignment.end,
            children: [
              TextButton(
                onPressed: () => bridge.respondApproval('decline'),
                child:
                    const Text('拒绝', style: TextStyle(color: Cx.textSecondary)),
              ),
              const SizedBox(width: 8),
              FilledButton(
                style: FilledButton.styleFrom(
                    backgroundColor: Cx.ink, foregroundColor: Colors.white),
                onPressed: () => bridge.respondApproval('accept'),
                child: const Text('批准'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _Thinking extends StatelessWidget {
  const _Thinking();
  @override
  Widget build(BuildContext context) {
    return const Padding(
      padding: EdgeInsets.symmetric(vertical: 10),
      child: Row(
        children: [
          SizedBox(
            width: 13,
            height: 13,
            child: CircularProgressIndicator(
                strokeWidth: 1.6, color: Cx.textFaint),
          ),
          SizedBox(width: 10),
          Text('正在思考…', style: TextStyle(color: Cx.textFaint, fontSize: 14)),
        ],
      ),
    );
  }
}

/// Video shown as a tap-to-play poster card. Tapping downloads the whole file
/// once via /file (the hub proxy serves it complete, with NO HTTP Range) into a
/// local temp file, then plays from disk. Streaming networkUrl over the relay
/// fails with iOS AVPlayer -12939 (the proxy answers 200 with no byte-range /
/// content-length); a local file has no such requirement. Mirrors the official
/// app's download-then-play UX and avoids re-streaming a multi-MB clip on open.
class VideoBubble extends StatefulWidget {
  final String url;
  final String name;
  const VideoBubble({super.key, required this.url, required this.name});

  @override
  State<VideoBubble> createState() => _VideoBubbleState();
}

enum _VidPhase { idle, loading, ready, error }

class _VideoBubbleState extends State<VideoBubble> {
  VideoPlayerController? _c;
  _VidPhase _phase = _VidPhase.idle;
  String? _err;
  double? _progress; // 0..1 when the response advertises a length

  @override
  void dispose() {
    _c?.dispose();
    super.dispose();
  }

  Future<void> _loadAndPlay() async {
    setState(() {
      _phase = _VidPhase.loading;
      _err = null;
      _progress = null;
    });
    try {
      final file = await _download(widget.url);
      final c = VideoPlayerController.file(file);
      await c.initialize();
      if (!mounted) {
        c.dispose();
        return;
      }
      setState(() {
        _c = c;
        _phase = _VidPhase.ready;
      });
      await c.play();
    } catch (e) {
      if (mounted) {
        setState(() {
          _phase = _VidPhase.error;
          _err = e.toString();
        });
      }
    }
  }

  // Pull the whole file (the hub serves it complete, no Range) to a temp file so
  // AVPlayer plays it locally instead of choking on the non-range HTTP stream.
  Future<File> _download(String url) async {
    final client = HttpClient();
    try {
      final resp = await (await client.getUrl(Uri.parse(url))).close();
      if (resp.statusCode != 200) {
        throw 'HTTP ${resp.statusCode}';
      }
      final total = resp.contentLength;
      final bytes = <int>[];
      await for (final chunk in resp) {
        bytes.addAll(chunk);
        if (total > 0 && mounted) {
          setState(() => _progress = bytes.length / total);
        }
      }
      final f =
          File('${Directory.systemTemp.path}/caret_vid_${url.hashCode}.mp4');
      await f.writeAsBytes(bytes, flush: true);
      return f;
    } finally {
      client.close();
    }
  }

  @override
  Widget build(BuildContext context) {
    switch (_phase) {
      case _VidPhase.ready:
        return _player(_c!);
      case _VidPhase.loading:
        return _poster(loading: true);
      case _VidPhase.error:
        return _poster(error: _err);
      case _VidPhase.idle:
        return _poster();
    }
  }

  Widget _poster({bool loading = false, String? error}) {
    return GestureDetector(
      onTap: loading ? null : _loadAndPlay,
      child: AspectRatio(
        aspectRatio: 16 / 9,
        child: Container(
          color: const Color(0xFF1A1A1A),
          alignment: Alignment.center,
          padding: const EdgeInsets.symmetric(horizontal: 16),
          child: loading
              ? Column(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    SizedBox(
                      width: 30,
                      height: 30,
                      child: CircularProgressIndicator(
                        strokeWidth: 2.5,
                        color: Colors.white70,
                        value: _progress,
                      ),
                    ),
                    const SizedBox(height: 10),
                    Text(
                      _progress == null
                          ? '正在下载…'
                          : '正在下载 ${(_progress! * 100).round()}%',
                      style:
                          const TextStyle(color: Colors.white60, fontSize: 12),
                    ),
                  ],
                )
              : Column(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    Icon(
                      error == null
                          ? Icons.play_circle_fill
                          : Icons.error_outline,
                      size: 54,
                      color: error == null ? Colors.white : Colors.redAccent,
                    ),
                    const SizedBox(height: 8),
                    Text(
                      error == null ? '点按播放 · ${widget.name}' : '加载失败,点按重试',
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      textAlign: TextAlign.center,
                      style:
                          const TextStyle(color: Colors.white70, fontSize: 12),
                    ),
                  ],
                ),
        ),
      ),
    );
  }

  Widget _player(VideoPlayerController c) {
    return AspectRatio(
      aspectRatio: c.value.aspectRatio == 0 ? 16 / 9 : c.value.aspectRatio,
      child: Stack(
        alignment: Alignment.center,
        children: [
          VideoPlayer(c),
          ValueListenableBuilder<VideoPlayerValue>(
            valueListenable: c,
            builder: (context, v, _) => GestureDetector(
              onTap: () => v.isPlaying ? c.pause() : c.play(),
              child: AnimatedOpacity(
                opacity: v.isPlaying ? 0.0 : 1.0,
                duration: const Duration(milliseconds: 200),
                child: Container(
                  color: Colors.black26,
                  child: const Center(
                    child: Icon(Icons.play_circle_fill,
                        size: 56, color: Colors.white70),
                  ),
                ),
              ),
            ),
          ),
          Positioned(
            left: 0,
            right: 0,
            bottom: 0,
            child: VideoProgressIndicator(
              c,
              allowScrubbing: true,
              colors: const VideoProgressColors(playedColor: Cx.ink),
            ),
          ),
        ],
      ),
    );
  }
}
