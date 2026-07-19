import 'package:flutter/material.dart';

import '../../main.dart';
import '../config.dart';
import '../models/machine.dart';
import '../models/session.dart';
import '../services/bridge_client.dart';
import 'chat_screen.dart';
import 'connect_screen.dart';
import 'theme.dart';

/// The Codex home: a big title, a row of machine chips (with online dots), the
/// "项目" hierarchy of threads grouped by working directory, and a bottom search
/// box + dark "聊天" button to start a new thread.
class SessionsScreen extends StatefulWidget {
  const SessionsScreen({super.key});

  @override
  State<SessionsScreen> createState() => _SessionsScreenState();
}

class _SessionsScreenState extends State<SessionsScreen> {
  final _scaffold = GlobalKey<ScaffoldState>();
  final _searchCtrl = TextEditingController();
  // Which project sections are expanded. Empty = all collapsed (the default);
  // persisted to the keychain so the fold state survives restarts.
  final Set<String> _expanded = {};
  String _query = '';
  // cwd -> display label. Usually the leaf folder name; when two different paths
  // share a leaf name, the shortest trailing path that makes them unique.
  Map<String, String> _labels = const {};

  // Optional deep-link for testing: --dart-define=OPEN_THREAD=<id>.
  static const _openThread = String.fromEnvironment('OPEN_THREAD');
  bool _autoOpened = false;

  @override
  void initState() {
    super.initState();
    _searchCtrl.addListener(
        () => setState(() => _query = _searchCtrl.text.trim().toLowerCase()));
    _loadExpanded();
    if (_openThread.isNotEmpty) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (_autoOpened || !mounted) return;
        _autoOpened = true;
        bridge.openThread(_openThread);
        Navigator.of(context).push(
          MaterialPageRoute(
              builder: (_) =>
                  const ChatScreen(threadId: _openThread, title: '会话')),
        );
      });
    }
  }

  @override
  void dispose() {
    _searchCtrl.dispose();
    super.dispose();
  }

  // Restore the saved fold state (anything not in the set stays collapsed).
  Future<void> _loadExpanded() async {
    final saved = await store.loadExpandedProjects();
    if (!mounted) return;
    setState(() {
      _expanded
        ..clear()
        ..addAll(saved);
    });
  }

  // Flip one project section and persist the new fold state.
  void _toggleSection(String cwd, bool currentlyCollapsed) {
    setState(() {
      if (currentlyCollapsed) {
        _expanded.add(cwd);
      } else {
        _expanded.remove(cwd);
      }
    });
    store.saveExpandedProjects(_expanded);
  }

  // ---- actions ----

  void _newThread({String? cwd}) {
    bridge.newThread();
    Navigator.of(context).push(
      MaterialPageRoute(
          builder: (_) => ChatScreen(threadId: null, presetCwd: cwd)),
    );
  }

  void _open(Session s) {
    bridge.openThread(s.id);
    Navigator.of(context).push(
      MaterialPageRoute(
          builder: (_) => ChatScreen(threadId: s.id, title: s.title)),
    );
  }

  Future<void> _switch(Machine m) async {
    final sameMachine = m.id == machines.activeId;
    // Already on it (connected) or a connect is in flight to it — ignore the tap.
    if (sameMachine &&
        (bridge.state == ConnState.connected ||
            bridge.state == ConnState.connecting)) {
      return;
    }
    await machines.setActive(m.id);
    await bridge.connectMachine(m);
  }

  void _retry() {
    final a = machines.active;
    if (a != null) {
      bridge.connectMachine(a);
    }
  }

  Future<void> _addMachine() async {
    await Navigator.of(context)
        .push(MaterialPageRoute(builder: (_) => const ConnectScreen()));
  }

  // ---- build ----

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      key: _scaffold,
      drawer: _drawer(),
      resizeToAvoidBottomInset: true,
      body: SafeArea(
        child: ListenableBuilder(
          listenable: Listenable.merge([bridge, machines]),
          builder: (context, _) => Column(
            children: [
              _header(),
              _chips(),
              const SizedBox(height: 8),
              Expanded(child: _content()),
              _footer(),
            ],
          ),
        ),
      ),
    );
  }

  Widget _header() {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              CxCircleButton(Icons.menu_rounded,
                  onTap: () => _scaffold.currentState?.openDrawer(),
                  tooltip: '菜单'),
              const Spacer(),
              CxCircleButton(Icons.more_horiz_rounded,
                  onTap: _moreMenu, tooltip: '更多'),
            ],
          ),
          const SizedBox(height: 12),
        ],
      ),
    );
  }

  // ---- machine chips ----

  Widget _chips() {
    return SizedBox(
      height: 40,
      child: ListView(
        scrollDirection: Axis.horizontal,
        padding: const EdgeInsets.symmetric(horizontal: 16),
        children: [
          for (final m in machines.machines) ...[
            _machineChip(m),
            const SizedBox(width: 8),
          ],
          _addChip(),
        ],
      ),
    );
  }

  Widget _machineChip(Machine m) {
    final active = m.id == machines.activeId;
    final dot = active ? _liveColor() : Cx.textFaint;
    return GestureDetector(
      onTap: () => _switch(m),
      onLongPress: () => _machineMenu(m),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
        decoration: BoxDecoration(
          color: active ? Cx.ink : Cx.surface,
          borderRadius: BorderRadius.circular(20),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            CxDot(dot, size: 8),
            const SizedBox(width: 8),
            Icon(Icons.computer_rounded,
                size: 15, color: active ? Colors.white : Cx.textSecondary),
            const SizedBox(width: 6),
            Text(m.label,
                style: TextStyle(
                    color: active ? Colors.white : Cx.textSecondary,
                    fontSize: 14,
                    fontWeight: FontWeight.w500)),
            if (active && bridge.state == ConnState.connected) ...[
              const SizedBox(width: 6),
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                decoration: BoxDecoration(
                  color: Colors.white24,
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Text(bridge.directTransport ? '直连' : '中继',
                    style: const TextStyle(fontSize: 10, color: Colors.white)),
              ),
            ],
          ],
        ),
      ),
    );
  }

  Widget _addChip() {
    return GestureDetector(
      onTap: _addMachine,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
        decoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(20),
        ),
        child: const Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.add_rounded, size: 17, color: Cx.textSecondary),
            SizedBox(width: 4),
            Text('添加',
                style: TextStyle(
                    color: Cx.textSecondary,
                    fontSize: 14,
                    fontWeight: FontWeight.w500)),
          ],
        ),
      ),
    );
  }

  Color _liveColor() {
    switch (bridge.state) {
      case ConnState.connected:
        return Cx.online;
      case ConnState.connecting:
        return Cx.warn;
      default:
        return Cx.offline;
    }
  }

  // ---- content ----

  Widget _content() {
    if (bridge.state == ConnState.connecting && bridge.sessions.isEmpty) {
      return const Center(
        child: SizedBox(
          width: 22,
          height: 22,
          child: CircularProgressIndicator(strokeWidth: 2, color: Cx.textFaint),
        ),
      );
    }
    if (bridge.state == ConnState.error ||
        bridge.state == ConnState.disconnected) {
      return _offline();
    }
    final filtered = _filtered();
    final groups = _group(filtered); // real project folders (grouped)
    final loose =
        _looseSessions(filtered); // one-off "quick chats" → flat 对话 list
    if (groups.isEmpty && loose.isEmpty) {
      return _emptyHint(
          _query.isEmpty ? '这台电脑还没有会话\n点下面「聊天」开始' : '没有匹配「$_query」的会话');
    }
    _labels = _computeLabels(groups.map((e) => e.key));
    return RefreshIndicator(
      color: Cx.ink,
      backgroundColor: Cx.bg,
      onRefresh: () async => bridge.listSessions(),
      child: ListView(
        padding: const EdgeInsets.only(top: 4, bottom: 12),
        children: [
          // 项目: folders the user actually works in (grouped, collapsible).
          if (groups.isNotEmpty) ...[
            const Padding(
              padding: EdgeInsets.fromLTRB(20, 8, 20, 4),
              child: CxLabel('项目'),
            ),
            for (final g in groups) ..._section(g.key, g.value),
          ],
          // 对话: Codex's one-off quick chats (scratch cwds) — flat, by recency,
          // mirroring the official Codex desktop layout.
          if (loose.isNotEmpty) ...[
            const Padding(
              padding: EdgeInsets.fromLTRB(20, 14, 20, 4),
              child: CxLabel('对话'),
            ),
            for (final s in loose) _looseTile(s),
          ],
        ],
      ),
    );
  }

  Widget _offline() {
    final label = machines.active?.label ?? '电脑';
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.cloud_off_rounded, size: 40, color: Cx.textFaint),
            const SizedBox(height: 12),
            Text('「$label」未连接',
                textAlign: TextAlign.center,
                style: const TextStyle(color: Cx.textSecondary, fontSize: 15)),
            if (machines.active?.linkMode == 'lanOnly') ...[
              const SizedBox(height: 6),
              const Text('仅局域网模式：确认手机和电脑在同一 WiFi',
                  style: TextStyle(color: Cx.textFaint, fontSize: 12)),
            ],
            if (bridge.error != null) ...[
              const SizedBox(height: 6),
              Text(bridge.error!,
                  textAlign: TextAlign.center,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(color: Cx.textFaint, fontSize: 12)),
            ],
            const SizedBox(height: 16),
            FilledButton(
              style: FilledButton.styleFrom(
                backgroundColor: Cx.ink,
                foregroundColor: Colors.white,
                shape: RoundedRectangleBorder(
                    borderRadius: BorderRadius.circular(14)),
              ),
              onPressed: _retry,
              child: const Text('重新连接'),
            ),
          ],
        ),
      ),
    );
  }

  Widget _emptyHint(String text) {
    return ListView(
      // ListView so RefreshIndicator-less empty state can still scroll/center.
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(32, 80, 32, 0),
          child: Text(text,
              textAlign: TextAlign.center,
              style: const TextStyle(
                  color: Cx.textFaint, height: 1.6, fontSize: 15)),
        ),
      ],
    );
  }

  // ---- project sections ----

  List<Widget> _section(String cwd, List<Session> sessions) {
    // Default collapsed: a section is open only if explicitly expanded (or while
    // searching, when everything is forced open).
    final collapsed = !_expanded.contains(cwd) && _query.isEmpty;
    final working = sessions.any((s) => bridge.activeThreadIds.contains(s.id));
    return [
      _sectionHeader(cwd, collapsed, working),
      if (!collapsed)
        for (final s in sessions) _tile(s),
      const SizedBox(height: 6),
    ];
  }

  Widget _sectionHeader(String cwd, bool collapsed, bool working) {
    final label = _labels[cwd] ?? _wsName(cwd);
    return InkWell(
      onTap: _query.isNotEmpty ? null : () => _toggleSection(cwd, collapsed),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(20, 14, 12, 8),
        child: Row(
          children: [
            const Icon(Icons.folder_open_rounded,
                size: 20, color: Cx.textSecondary),
            const SizedBox(width: 10),
            // One Expanded eats all the free space so the pencil sits flush right,
            // regardless of folder-name length (a Flexible + Spacer would compete).
            Expanded(
              child: Row(
                children: [
                  Flexible(
                    child: Text(label,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                            color: Cx.textPrimary,
                            fontSize: 17,
                            fontWeight: FontWeight.w600)),
                  ),
                  const SizedBox(width: 4),
                  if (_query.isEmpty)
                    AnimatedRotation(
                      turns: collapsed ? -0.25 : 0,
                      duration: const Duration(milliseconds: 150),
                      child: const Icon(Icons.expand_more_rounded,
                          size: 20, color: Cx.textFaint),
                    ),
                ],
              ),
            ),
            if (working) ...[
              const SizedBox(width: 6),
              _activeSpinner(size: 16),
            ],
            IconButton(
              onPressed: () => _newThread(cwd: cwd.isEmpty ? null : cwd),
              icon: const Icon(Icons.edit_outlined,
                  size: 19, color: Cx.textFaint),
              tooltip: '在此项目新建会话',
              visualDensity: VisualDensity.compact,
            ),
          ],
        ),
      ),
    );
  }

  Widget _tile(Session s) {
    final working = bridge.activeThreadIds.contains(s.id);
    return Dismissible(
      key: ValueKey(s.id),
      direction: DismissDirection.endToStart,
      background: Container(
        alignment: Alignment.centerRight,
        color: Cx.offline,
        padding: const EdgeInsets.only(right: 20),
        child: const Icon(Icons.delete_outline, color: Colors.white),
      ),
      confirmDismiss: (_) => _confirmDelete(s),
      onDismissed: (_) {
        bridge.deleteThread(s.id);
        _toast('已删除会话');
      },
      child: InkWell(
        onTap: () => _open(s),
        // Full-width container so the WHOLE row is tappable, not just the text:
        // a short title would otherwise shrink-wrap the InkWell to the text width.
        child: Container(
          width: double.infinity,
          alignment: Alignment.centerLeft,
          padding: const EdgeInsets.fromLTRB(20, 15, 16, 15),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  s.title,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(
                      color: Cx.textPrimary, fontSize: 16, height: 1.3),
                ),
              ),
              if (working) ...[
                const SizedBox(width: 12),
                _activeSpinner(),
              ],
            ],
          ),
        ),
      ),
    );
  }

  // A 对话 row: a one-off quick chat, shown flat with a relative timestamp on the
  // right (like the official Codex desktop's "对话" list).
  Widget _looseTile(Session s) {
    final working = bridge.activeThreadIds.contains(s.id);
    return Dismissible(
      key: ValueKey(s.id),
      direction: DismissDirection.endToStart,
      background: Container(
        alignment: Alignment.centerRight,
        color: Cx.offline,
        padding: const EdgeInsets.only(right: 20),
        child: const Icon(Icons.delete_outline, color: Colors.white),
      ),
      confirmDismiss: (_) => _confirmDelete(s),
      onDismissed: (_) {
        bridge.deleteThread(s.id);
        _toast('已删除会话');
      },
      child: InkWell(
        onTap: () => _open(s),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(20, 14, 16, 14),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  s.title,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(
                      color: Cx.textPrimary, fontSize: 16, height: 1.3),
                ),
              ),
              if (_relTime(s.updatedAt).isNotEmpty) ...[
                const SizedBox(width: 12),
                Text(_relTime(s.updatedAt),
                    style: const TextStyle(color: Cx.textFaint, fontSize: 12)),
              ],
              if (working) ...[
                const SizedBox(width: 10),
                _activeSpinner(),
              ],
            ],
          ),
        ),
      ),
    );
  }

  Widget _activeSpinner({double size = 18}) {
    return SizedBox(
      width: size,
      height: size,
      child: const CircularProgressIndicator(strokeWidth: 2, color: Cx.ink),
    );
  }

  Future<bool> _confirmDelete(Session s) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        backgroundColor: Cx.bg,
        title: const Text('删除会话', style: TextStyle(color: Cx.textPrimary)),
        content: Text('确定删除「${s.title}」吗？该会话将从列表移除。',
            style: const TextStyle(color: Cx.textSecondary)),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('取消', style: TextStyle(color: Cx.textSecondary)),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('删除', style: TextStyle(color: Cx.offline)),
          ),
        ],
      ),
    );
    return ok ?? false;
  }

  // ---- footer (search + 聊天) ----

  Widget _footer() {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 6, 16, 10),
      child: Row(
        children: [
          Expanded(
            child: TextField(
              controller: _searchCtrl,
              style: const TextStyle(fontSize: 15, color: Cx.textPrimary),
              decoration: InputDecoration(
                isDense: true,
                hintText: '搜索聊天记录',
                hintStyle: const TextStyle(color: Cx.textFaint),
                prefixIcon: const Icon(Icons.search_rounded,
                    size: 20, color: Cx.textFaint),
                filled: true,
                fillColor: Cx.surface,
                contentPadding: const EdgeInsets.symmetric(vertical: 12),
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(24),
                  borderSide: BorderSide.none,
                ),
              ),
            ),
          ),
          const SizedBox(width: 10),
          Material(
            color: Cx.ink,
            borderRadius: BorderRadius.circular(24),
            child: InkWell(
              borderRadius: BorderRadius.circular(24),
              onTap: () => _newThread(),
              child: const Padding(
                padding: EdgeInsets.symmetric(horizontal: 18, vertical: 13),
                child: Row(
                  children: [
                    Icon(Icons.edit_outlined, size: 18, color: Colors.white),
                    SizedBox(width: 6),
                    Text('聊天',
                        style: TextStyle(
                            color: Colors.white,
                            fontSize: 15,
                            fontWeight: FontWeight.w600)),
                  ],
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }

  // ---- menus & drawer ----

  void _moreMenu() async {
    final v = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            _menuTile(ctx, Icons.refresh_rounded, '刷新会话', 'refresh'),
            _menuTile(ctx, Icons.add_rounded, '添加电脑', 'add'),
            _menuTile(
                ctx, Icons.power_settings_new_rounded, '断开连接', 'disconnect'),
            _menuTile(ctx, Icons.info_outline_rounded, '关于', 'about'),
          ],
        ),
      ),
    );
    if (!mounted) return;
    switch (v) {
      case 'refresh':
        bridge.listSessions();
        _toast('已刷新');
        break;
      case 'add':
        _addMachine();
        break;
      case 'disconnect':
        bridge.disconnect();
        break;
      case 'about':
        _about();
        break;
    }
  }

  ListTile _menuTile(
      BuildContext ctx, IconData icon, String label, String value) {
    return ListTile(
      leading: Icon(icon, color: Cx.textPrimary),
      title: Text(label, style: const TextStyle(color: Cx.textPrimary)),
      onTap: () => Navigator.pop(ctx, value),
    );
  }

  void _machineMenu(Machine m) async {
    final v = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 14, 20, 6),
              child: Text(m.label,
                  style: const TextStyle(
                      color: Cx.textSecondary,
                      fontSize: 13,
                      fontWeight: FontWeight.w600)),
            ),
            _menuTile(ctx, Icons.swap_horiz_rounded,
                '连接方式：${_linkModeLabel(m.linkMode)}', 'link'),
            _menuTile(
                ctx, Icons.drive_file_rename_outline_rounded, '重命名', 'rename'),
            _menuTile(ctx, Icons.delete_outline_rounded, '移除这台电脑', 'remove'),
          ],
        ),
      ),
    );
    if (!mounted) return;
    if (v == 'link') _pickLinkMode(m);
    if (v == 'rename') _renameMachine(m);
    if (v == 'remove') _removeMachine(m);
  }

  String _linkModeLabel(String mode) => switch (mode) {
        'lanOnly' => '仅局域网',
        'relayOnly' => '仅公网',
        _ => '自动',
      };

  Future<void> _pickLinkMode(Machine m) async {
    final v = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Padding(
              padding: EdgeInsets.fromLTRB(20, 14, 20, 6),
              child: Text('连接方式',
                  style: TextStyle(
                      color: Cx.textSecondary,
                      fontSize: 13,
                      fontWeight: FontWeight.w600)),
            ),
            _menuTile(ctx, Icons.autorenew_rounded, '自动（推荐）', 'auto'),
            _menuTile(ctx, Icons.wifi_rounded, '仅局域网', 'lanOnly'),
            if (m.pubEnabled)
              _menuTile(ctx, Icons.public_rounded, '仅公网', 'relayOnly')
            else
              const ListTile(
                leading: Icon(Icons.public_off_rounded, color: Cx.textFaint),
                title: Text('仅公网（订阅后可用）',
                    style: TextStyle(color: Cx.textFaint)),
              ),
          ],
        ),
      ),
    );
    if (!mounted || v == null) return;
    await machines.setLinkMode(m.id, v);
    final updated = machines.machines.firstWhere((x) => x.id == m.id);
    if (m.id == machines.activeId) await bridge.connectMachine(updated);
  }

  Future<void> _renameMachine(Machine m) async {
    final ctrl = TextEditingController(text: m.label);
    try {
      final name = await showDialog<String>(
        context: context,
        builder: (ctx) => AlertDialog(
          backgroundColor: Cx.bg,
          title: const Text('重命名电脑'),
          content: TextField(
            controller: ctrl,
            autofocus: true,
            decoration: const InputDecoration(hintText: '电脑名称'),
            onSubmitted: (v) => Navigator.pop(ctx, v),
          ),
          actions: [
            TextButton(
                onPressed: () => Navigator.pop(ctx), child: const Text('取消')),
            TextButton(
                onPressed: () => Navigator.pop(ctx, ctrl.text),
                child: const Text('保存')),
          ],
        ),
      );
      if (name != null && name.trim().isNotEmpty) {
        await machines.rename(m.id, name);
      }
    } finally {
      ctrl.dispose();
    }
  }

  Future<void> _removeMachine(Machine m) async {
    final wasActive = m.id == machines.activeId;
    await machines.remove(m.id);
    await store
        .deleteCodec(m.id); // forget its E2EE pairing so a re-add re-pairs
    if (wasActive) {
      final a = machines.active;
      if (a != null) {
        await bridge.connectMachine(a);
      } else {
        await bridge.disconnect();
      }
    }
  }

  void _about() {
    showAboutDialog(
      context: context,
      applicationName: 'Caret',
      applicationVersion: kRelayHost,
      children: const [
        Padding(
          padding: EdgeInsets.only(top: 8),
          child: Text('从手机远程控制你电脑上的编程助手。'),
        ),
      ],
    );
  }

  Widget _drawer() {
    return Drawer(
      backgroundColor: Cx.bg,
      child: SafeArea(
        child: ListenableBuilder(
          listenable: Listenable.merge([bridge, machines]),
          builder: (context, _) => Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Padding(
                padding: EdgeInsets.fromLTRB(20, 18, 20, 6),
                child: Text('我的电脑',
                    style:
                        TextStyle(fontSize: 20, fontWeight: FontWeight.w700)),
              ),
              Expanded(
                child: ListView(
                  children: [
                    for (final m in machines.machines)
                      ListTile(
                        leading: CxDot(
                            m.id == machines.activeId
                                ? _liveColor()
                                : Cx.textFaint,
                            size: 10),
                        title: Text(m.label,
                            style: const TextStyle(color: Cx.textPrimary)),
                        subtitle: Text(
                            m.id == machines.activeId ? _stateText() : '点按切换',
                            style: const TextStyle(
                                color: Cx.textFaint, fontSize: 12)),
                        trailing: m.id == machines.activeId
                            ? const Icon(Icons.check_rounded, color: Cx.ink)
                            : null,
                        onTap: () {
                          Navigator.pop(context);
                          _switch(m);
                        },
                        onLongPress: () {
                          Navigator.pop(context);
                          _machineMenu(m);
                        },
                      ),
                    ListTile(
                      leading:
                          const Icon(Icons.add_rounded, color: Cx.textPrimary),
                      title: const Text('添加电脑',
                          style: TextStyle(color: Cx.textPrimary)),
                      onTap: () {
                        Navigator.pop(context);
                        _addMachine();
                      },
                    ),
                  ],
                ),
              ),
              const Divider(height: 1),
              ListTile(
                leading:
                    const Icon(Icons.refresh_rounded, color: Cx.textPrimary),
                title:
                    const Text('刷新会话', style: TextStyle(color: Cx.textPrimary)),
                onTap: () {
                  Navigator.pop(context);
                  bridge.listSessions();
                  _toast('已刷新');
                },
              ),
              ListTile(
                leading: const Icon(Icons.info_outline_rounded,
                    color: Cx.textPrimary),
                title:
                    const Text('关于', style: TextStyle(color: Cx.textPrimary)),
                onTap: () {
                  Navigator.pop(context);
                  _about();
                },
              ),
            ],
          ),
        ),
      ),
    );
  }

  String _stateText() {
    switch (bridge.state) {
      case ConnState.connected:
        return '已连接';
      case ConnState.connecting:
        return '连接中…';
      case ConnState.error:
        return '连接失败';
      default:
        return '已断开';
    }
  }

  // ---- helpers ----

  void _toast(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(
        content: Text(msg),
        duration: const Duration(milliseconds: 900),
        behavior: SnackBarBehavior.floating,
        backgroundColor: Cx.ink,
      ));
  }

  List<Session> _filtered() {
    if (_query.isEmpty) return bridge.sessions;
    return bridge.sessions
        .where((s) => s.title.toLowerCase().contains(_query))
        .toList();
  }

  // Project folders only — loose/"quick chat" sessions are pulled out into the
  // flat 对话 section instead of forming fake one-session folders.
  List<MapEntry<String, List<Session>>> _group(List<Session> all) {
    final m = <String, List<Session>>{};
    for (final s in all) {
      if (_isLoose(s.cwd)) continue;
      m.putIfAbsent(s.cwd, () => []).add(s);
    }
    final entries = m.entries.toList();
    entries.sort((a, b) => _latest(b.value).compareTo(_latest(a.value)));
    for (final e in entries) {
      e.value.sort((a, b) => b.updatedAt.compareTo(a.updatedAt));
    }
    return entries;
  }

  // The flat 对话 list: every loose session, newest first.
  List<Session> _looseSessions(List<Session> all) {
    final loose = all.where((s) => _isLoose(s.cwd)).toList();
    loose.sort((a, b) => b.updatedAt.compareTo(a.updatedAt));
    return loose;
  }

  // A session is "loose"/temporary (a one-off quick chat, not a real project)
  // when its cwd is a Codex scratch dir or otherwise not a workspace folder.
  bool _isLoose(String cwd) {
    if (cwd.isEmpty) return true;
    final segs = _segs(cwd);
    if (segs.isEmpty) return true; // "/"
    // Codex parks every one-off "quick chat" under <Documents>/Codex/<date>/<slug>
    // (verified on the Windows desktop). Any "Codex" path segment marks one.
    if (segs.any((s) => s.toLowerCase() == 'codex')) return true;
    final lower = cwd.replaceAll('\\', '/').toLowerCase();
    // POSIX temp dirs.
    if (lower == '/tmp' ||
        lower.startsWith('/tmp/') ||
        lower.startsWith('/private/tmp') ||
        lower.startsWith('/private/var/folders') ||
        lower.startsWith('/var/folders')) {
      return true;
    }
    // Windows system dirs (a chat run in System32 isn't a project).
    if (lower == 'c:/windows' || lower.startsWith('c:/windows/')) {
      return true;
    }
    // The home directory itself: /Users/<x>, /home/<x>, or C:/Users/<x>.
    if (segs.length == 2 && (segs[0] == 'Users' || segs[0] == 'home')) {
      return true;
    }
    if (segs.length == 3 &&
        segs[0].endsWith(':') &&
        segs[1].toLowerCase() == 'users') {
      return true;
    }
    return false;
  }

  int _latest(List<Session> v) =>
      v.fold(0, (p, e) => e.updatedAt > p ? e.updatedAt : p);

  String _wsName(String cwd) {
    if (cwd.isEmpty) return '未分组';
    // Split on both POSIX (/) and Windows (\) separators so a Windows cwd like
    // C:\Users\me\proj shows just "proj" instead of the whole absolute path.
    final parts = _segs(cwd);
    return parts.isEmpty ? cwd : parts.last;
  }

  // Relative time like the official "对话" list (updatedAt is a Unix second).
  String _relTime(int sec) {
    if (sec <= 0) return '';
    final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
    final d = now - sec;
    if (d < 60) return '刚刚';
    if (d < 3600) return '${d ~/ 60} 分钟';
    if (d < 86400) return '${d ~/ 3600} 小时';
    if (d < 604800) return '${d ~/ 86400} 天';
    if (d < 2592000) return '${d ~/ 604800} 周';
    return '${d ~/ 2592000} 月';
  }

  static List<String> _segs(String p) =>
      p.split(RegExp(r'[/\\]')).where((e) => e.isNotEmpty).toList();

  /// Build the cwd→label map for the current groups. A folder name shown alone
  /// is ambiguous when two different paths share it (e.g. .../web/app and
  /// .../mobile/app both leaf to "app"); for those, fall back to the shortest
  /// trailing path that uniquely identifies each ("web/app" vs "mobile/app").
  Map<String, String> _computeLabels(Iterable<String> cwds) {
    final byLeaf = <String, List<String>>{};
    for (final c in cwds) {
      byLeaf.putIfAbsent(_wsName(c), () => <String>[]).add(c);
    }
    final out = <String, String>{};
    byLeaf.forEach((leaf, paths) {
      if (paths.length <= 1) {
        out[paths.first] = leaf;
      } else {
        for (final c in paths) {
          out[c] = _uniqueTail(c, paths);
        }
      }
    });
    return out;
  }

  String _uniqueTail(String cwd, List<String> siblings) {
    final mine = _segs(cwd);
    for (var take = 2; take <= mine.length; take++) {
      final tail = mine.sublist(mine.length - take).join('/');
      final hits = siblings.where((s) {
        final ss = _segs(s);
        return ss.length >= take &&
            ss.sublist(ss.length - take).join('/') == tail;
      }).length;
      if (hits == 1) return tail;
    }
    return cwd; // fully distinct only at the absolute path
  }
}
