import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';
import 'package:speech_to_text/speech_to_text.dart';

import 'theme.dart';

/// One picked image held in the composer: raw bytes (for the thumbnail) plus the
/// data: URL that gets sent to the bridge.
class _PendingImage {
  final Uint8List bytes;
  final String dataUrl;
  _PendingImage(this.bytes, this.dataUrl);
}

/// Codex-style composer card: a rounded white card holding the text field on top
/// and a toolbar row (+ / settings / model·effort / mic / send) underneath.
/// Calls [onSend] with the text, image data URLs, and the chosen effort + model.
class ChatComposer extends StatefulWidget {
  final void Function(String text, List<String> images, String effort,
      String model, String speed, String approval) onSend;
  final String hint;
  const ChatComposer(
      {super.key, required this.onSend, this.hint = '向 Codex 提问'});

  @override
  State<ChatComposer> createState() => _ChatComposerState();
}

class _ChatComposerState extends State<ChatComposer> {
  static const _maxImages = 6;
  static String _lastEffort = 'medium';
  static String _lastModel = 'gpt-5.5';
  static String _lastSpeed = 'fast';
  static String _lastApproval = 'default';

  // value -> display label, mirroring the official Codex menus.
  static const _efforts = {
    'low': '低',
    'medium': '中等',
    'high': '高',
    'xhigh': '极高'
  };
  static const _models = {
    'gpt-5.5': 'GPT-5.5',
    'gpt-5': 'GPT-5',
    'gpt-5-mini': 'GPT-5 mini'
  };
  static const _modelShort = {
    'gpt-5.5': '5.5',
    'gpt-5': '5',
    'gpt-5-mini': 'mini'
  };
  static const _speeds = {'fast': '快速', 'standard': '标准'};
  static const _approvals = <_ApprovalMode>[
    _ApprovalMode('default', '默认权限', '在沙盒中运行命令', Icons.back_hand_outlined),
    _ApprovalMode('auto', '自动审核', '自动审核提权请求', Icons.terminal_rounded),
    _ApprovalMode('full', '完全访问', '减少重复批准，仍限制在项目目录', Icons.gpp_maybe_outlined),
    _ApprovalMode('custom', '自定义 (config.toml)', '使用 config.toml 中定义的权限',
        Icons.settings_outlined),
  ];

  final _ctrl = TextEditingController();
  final _picker = ImagePicker();
  final List<_PendingImage> _images = [];

  final _smartKey = GlobalKey(); // anchor for the effort/model/speed menu
  final _shieldKey = GlobalKey(); // anchor for the approval-mode menu

  late String _effort = _lastEffort;
  late String _model = _lastModel;
  late String _speed = _lastSpeed;
  late String _approval = _lastApproval;
  bool _hasText = false;

  final SpeechToText _speech = SpeechToText();
  bool _speechReady = false;
  bool _listening = false;
  String _textBeforeListen = '';

  @override
  void initState() {
    super.initState();
    _ctrl.addListener(() {
      final has = _ctrl.text.trim().isNotEmpty;
      if (has != _hasText) setState(() => _hasText = has);
    });
  }

  @override
  void dispose() {
    _ctrl.dispose();
    _speech.cancel();
    super.dispose();
  }

  // ---- send ----

  void _send() {
    final text = _ctrl.text.trim();
    if (text.isEmpty && _images.isEmpty) return;
    final imgs = _images.map((e) => e.dataUrl).toList();
    widget.onSend(text, imgs, _effort, _model, _speed, _approval);
    _ctrl.clear();
    setState(_images.clear);
  }

  // ---- images ----

  Future<void> _pickFrom(ImageSource source) async {
    try {
      if (source == ImageSource.gallery) {
        final picked = await _picker.pickMultiImage(
            maxWidth: 1600, maxHeight: 1600, imageQuality: 75);
        for (final x in picked) {
          await _addImage(x);
        }
      } else {
        final x = await _picker.pickImage(
            source: source, maxWidth: 1600, maxHeight: 1600, imageQuality: 75);
        if (x != null) await _addImage(x);
      }
      if (mounted) setState(() {});
    } catch (e) {
      _toast('选择图片失败：$e');
    }
  }

  Future<void> _addImage(XFile x) async {
    if (_images.length >= _maxImages) {
      _toast('最多 $_maxImages 张图片');
      return;
    }
    final bytes = await x.readAsBytes();
    final mime = _mimeFor(x.name.isNotEmpty ? x.name : x.path);
    final dataUrl = 'data:$mime;base64,${base64Encode(bytes)}';
    _images.add(_PendingImage(bytes, dataUrl));
  }

  String _mimeFor(String path) {
    final p = path.toLowerCase();
    if (p.endsWith('.png')) return 'image/png';
    if (p.endsWith('.webp')) return 'image/webp';
    if (p.endsWith('.gif')) return 'image/gif';
    if (p.endsWith('.heic') || p.endsWith('.heif')) return 'image/heic';
    return 'image/jpeg';
  }

  void _attachSheet() {
    FocusScope.of(context).unfocus();
    _sheet([
      _SheetTile(Icons.photo_library_outlined, '从相册选择',
          () => _pickFrom(ImageSource.gallery)),
      _SheetTile(Icons.photo_camera_outlined, '拍照',
          () => _pickFrom(ImageSource.camera)),
    ]);
  }

  // ---- effort / model / speed menu (the ⚡ chip) ----

  Future<void> _openSmartMenu() async {
    FocusScope.of(context).unfocus();
    final r = await _menuAt<String>(_smartKey, [
      _headerItem('智能'),
      for (final e in _efforts.entries)
        _checkItem('effort:${e.key}', e.value, _effort == e.key),
      const PopupMenuDivider(),
      _navItem('model', '模型', _models[_model] ?? ''),
      _navItem('speed', '速度', _speeds[_speed] ?? ''),
    ]);
    if (r == null || !mounted) return;
    if (r.startsWith('effort:')) {
      setState(() => _lastEffort = _effort = r.substring(7));
    } else if (r == 'model') {
      _openModelMenu();
    } else if (r == 'speed') {
      _openSpeedMenu();
    }
  }

  Future<void> _openModelMenu() async {
    final r = await _menuAt<String>(_smartKey, [
      _headerItem('模型'),
      for (final m in _models.entries)
        _checkItem(m.key, m.value, _model == m.key),
    ]);
    if (r != null && mounted) setState(() => _lastModel = _model = r);
  }

  Future<void> _openSpeedMenu() async {
    final r = await _menuAt<String>(_smartKey, [
      _headerItem('速度'),
      for (final s in _speeds.entries)
        _checkItem(s.key, s.value, _speed == s.key),
    ]);
    if (r != null && mounted) setState(() => _lastSpeed = _speed = r);
  }

  // ---- approval mode menu (the shield) ----

  Future<void> _openApprovalMenu() async {
    FocusScope.of(context).unfocus();
    final r = await _menuAt<String>(
        _shieldKey, [for (final a in _approvals) _approvalItem(a)]);
    if (r != null && mounted) setState(() => _lastApproval = _approval = r);
  }

  /// Open a Codex-style popup anchored just above [key]'s widget.
  Future<T?> _menuAt<T>(GlobalKey key, List<PopupMenuEntry<T>> items) {
    final ctx = key.currentContext;
    if (ctx == null) return Future<T?>.value(null);
    final box = ctx.findRenderObject() as RenderBox;
    final overlay = Overlay.of(context).context.findRenderObject() as RenderBox;
    final topLeft = box.localToGlobal(Offset.zero, ancestor: overlay);
    final pos = RelativeRect.fromLTRB(
      topLeft.dx,
      topLeft.dy,
      overlay.size.width - topLeft.dx - box.size.width,
      overlay.size.height - topLeft.dy,
    );
    return showMenu<T>(
      context: context,
      position: pos,
      color: Cx.bg,
      surfaceTintColor: Colors.transparent,
      elevation: 10,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(14)),
      constraints: const BoxConstraints(minWidth: 240, maxWidth: 300),
      items: items,
    );
  }

  PopupMenuEntry<String> _headerItem(String t) => PopupMenuItem<String>(
        enabled: false,
        height: 30,
        child: Text(t,
            style: const TextStyle(
                color: Cx.textFaint,
                fontSize: 12,
                fontWeight: FontWeight.w600)),
      );

  PopupMenuEntry<String> _checkItem(String v, String label, bool sel) =>
      PopupMenuItem<String>(
        value: v,
        height: 44,
        child: Row(children: [
          Expanded(
              child: Text(label,
                  style: const TextStyle(color: Cx.textPrimary, fontSize: 15))),
          if (sel) const Icon(Icons.check_rounded, size: 18, color: Cx.ink),
        ]),
      );

  PopupMenuEntry<String> _navItem(String v, String label, String current) =>
      PopupMenuItem<String>(
        value: v,
        height: 48,
        child: Row(children: [
          Text(label,
              style: const TextStyle(
                  color: Cx.textPrimary,
                  fontSize: 15,
                  fontWeight: FontWeight.w500)),
          const Spacer(),
          Text(current,
              style: const TextStyle(color: Cx.textSecondary, fontSize: 14)),
          const SizedBox(width: 4),
          const Icon(Icons.chevron_right_rounded,
              size: 18, color: Cx.textFaint),
        ]),
      );

  PopupMenuEntry<String> _approvalItem(_ApprovalMode a) {
    final sel = _approval == a.id;
    return PopupMenuItem<String>(
      value: a.id,
      height: 60,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.center,
        children: [
          Icon(a.icon, size: 20, color: Cx.textSecondary),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(a.title,
                    style: const TextStyle(
                        color: Cx.textPrimary,
                        fontSize: 15,
                        fontWeight: FontWeight.w500)),
                const SizedBox(height: 2),
                Text(a.sub,
                    style: const TextStyle(
                        color: Cx.textFaint, fontSize: 12, height: 1.2)),
              ],
            ),
          ),
          if (sel)
            const Padding(
              padding: EdgeInsets.only(left: 8),
              child: Icon(Icons.check_rounded, size: 18, color: Cx.ink),
            ),
        ],
      ),
    );
  }

  // ---- voice ----

  Future<void> _toggleMic() async {
    if (_listening) {
      await _speech.stop();
      if (mounted) setState(() => _listening = false);
      return;
    }
    if (!_speechReady) {
      _speechReady = await _speech.initialize(
        onStatus: (s) {
          if ((s == 'done' || s == 'notListening') && mounted) {
            setState(() => _listening = false);
          }
        },
        onError: (e) {
          if (mounted) setState(() => _listening = false);
        },
      );
    }
    if (!_speechReady) {
      _toast('语音识别不可用（请检查麦克风权限）');
      return;
    }
    _textBeforeListen = _ctrl.text;
    setState(() => _listening = true);
    await _speech.listen(
      onResult: (r) {
        if (!mounted) return; // a trailing result can land after dispose()
        final spoken = r.recognizedWords;
        final base = _textBeforeListen.trim();
        _ctrl.text = base.isEmpty ? spoken : '$base $spoken';
        _ctrl.selection = TextSelection.collapsed(offset: _ctrl.text.length);
      },
      localeId: 'zh_CN',
      listenFor: const Duration(seconds: 60),
      pauseFor: const Duration(seconds: 4),
      listenOptions:
          SpeechListenOptions(partialResults: true, cancelOnError: true),
    );
  }

  // ---- helpers ----

  void _sheet(List<_SheetTile> tiles) {
    showModalBottomSheet<void>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            for (final t in tiles)
              ListTile(
                leading: Icon(t.icon, color: Cx.textPrimary),
                title: Text(t.label,
                    style: const TextStyle(color: Cx.textPrimary)),
                onTap: () {
                  Navigator.pop(ctx);
                  t.onTap();
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
          behavior: SnackBarBehavior.floating),
    );
  }

  // ---- ui ----

  @override
  Widget build(BuildContext context) {
    final canSend = _hasText || _images.isNotEmpty;
    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 6, 12, 10),
        child: Container(
          decoration: BoxDecoration(
            color: Cx.card,
            borderRadius: BorderRadius.circular(26),
            border: Border.all(color: Cx.border, width: 1),
            boxShadow: Cx.cardShadow,
          ),
          padding: const EdgeInsets.fromLTRB(8, 6, 8, 8),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              if (_images.isNotEmpty) _thumbs(),
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                child: TextField(
                  controller: _ctrl,
                  minLines: 1,
                  maxLines: 6,
                  style: const TextStyle(
                      fontSize: 16, color: Cx.textPrimary, height: 1.35),
                  decoration: InputDecoration(
                    isDense: true,
                    border: InputBorder.none,
                    enabledBorder: InputBorder.none,
                    focusedBorder: InputBorder.none,
                    contentPadding: const EdgeInsets.symmetric(vertical: 8),
                    hintText: _listening ? '正在聆听…' : widget.hint,
                    hintStyle: TextStyle(
                        color: _listening ? Cx.ink : Cx.textFaint,
                        fontSize: 16),
                  ),
                ),
              ),
              const SizedBox(height: 4),
              _toolbar(canSend),
            ],
          ),
        ),
      ),
    );
  }

  Widget _toolbar(bool canSend) {
    return Row(
      children: [
        _toolIcon(Icons.add, _attachSheet, tip: '附件'),
        _shieldButton(),
        const Spacer(),
        _modelChip(),
        _toolIcon(_listening ? Icons.mic : Icons.mic_none_rounded, _toggleMic,
            tip: '语音', active: _listening),
        const SizedBox(width: 2),
        _sendButton(canSend),
      ],
    );
  }

  Widget _shieldButton() {
    // Orange when the chosen mode loosens sandboxing (full access / auto-approve).
    final loosened = _approval == 'full' || _approval == 'auto';
    return KeyedSubtree(
      key: _shieldKey,
      child: _toolIcon(Icons.gpp_maybe_rounded, _openApprovalMenu,
          tip: '权限模式', active: loosened, activeColor: Cx.warn),
    );
  }

  Widget _modelChip() {
    return KeyedSubtree(
      key: _smartKey,
      child: GestureDetector(
        onTap: _openSmartMenu,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.bolt_rounded, size: 16, color: Cx.textSecondary),
              const SizedBox(width: 2),
              Text('${_modelShort[_model]} ${_efforts[_effort]}',
                  style: const TextStyle(
                      color: Cx.textSecondary,
                      fontSize: 13,
                      fontWeight: FontWeight.w500)),
            ],
          ),
        ),
      ),
    );
  }

  Widget _toolIcon(IconData icon, VoidCallback onTap,
      {String? tip, bool active = false, Color activeColor = Cx.ink}) {
    return IconButton(
      onPressed: onTap,
      iconSize: 22,
      visualDensity: VisualDensity.compact,
      color: active ? activeColor : Cx.textSecondary,
      icon: Icon(icon),
      tooltip: tip,
    );
  }

  Widget _sendButton(bool canSend) {
    return Material(
      color: canSend ? Cx.ink : Cx.surfaceAlt,
      shape: const CircleBorder(),
      child: InkWell(
        customBorder: const CircleBorder(),
        onTap: canSend ? _send : null,
        child: Padding(
          padding: const EdgeInsets.all(9),
          child: Icon(Icons.arrow_upward_rounded,
              size: 22, color: canSend ? Colors.white : Cx.textFaint),
        ),
      ),
    );
  }

  Widget _thumbs() {
    return Container(
      height: 76,
      margin: const EdgeInsets.only(bottom: 6, left: 4, right: 4),
      child: ListView.separated(
        scrollDirection: Axis.horizontal,
        itemCount: _images.length,
        separatorBuilder: (_, __) => const SizedBox(width: 8),
        itemBuilder: (context, i) {
          final img = _images[i];
          return Stack(
            children: [
              ClipRRect(
                borderRadius: BorderRadius.circular(10),
                child: Image.memory(img.bytes,
                    width: 68, height: 68, fit: BoxFit.cover),
              ),
              Positioned(
                top: 2,
                right: 2,
                child: GestureDetector(
                  onTap: () => setState(() => _images.removeAt(i)),
                  child: Container(
                    decoration: const BoxDecoration(
                        color: Colors.black54, shape: BoxShape.circle),
                    padding: const EdgeInsets.all(2),
                    child:
                        const Icon(Icons.close, size: 14, color: Colors.white),
                  ),
                ),
              ),
            ],
          );
        },
      ),
    );
  }
}

class _SheetTile {
  final IconData icon;
  final String label;
  final VoidCallback onTap;
  _SheetTile(this.icon, this.label, this.onTap);
}

/// One approval (permission) mode shown in the shield menu.
class _ApprovalMode {
  final String id;
  final String title;
  final String sub;
  final IconData icon;
  const _ApprovalMode(this.id, this.title, this.sub, this.icon);
}
