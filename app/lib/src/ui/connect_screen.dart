import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../main.dart';
import '../config.dart';
import '../services/bridge_client.dart';
import 'qr_scan_screen.dart';
import 'theme.dart';

/// Add-a-machine screen. The relay address is fixed (see [kRelayHost]); the user
/// only enters a token (or scans the desktop QR) and optionally names the machine.
/// Shown full-screen on first run, or pushed as a route from the home chips row.
class ConnectScreen extends StatefulWidget {
  const ConnectScreen({super.key});

  @override
  State<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends State<ConnectScreen> {
  final _ctrl = TextEditingController();
  final _nameCtrl = TextEditingController();
  final _hostCtrl = TextEditingController();
  bool _obscure = true;
  // The raw scanned/pasted connect string, kept so _connect can pull the one-time
  // E2EE pairing material (pid + epub) the text fields don't capture.
  String? _scanned;

  @override
  void dispose() {
    _ctrl.dispose();
    _nameCtrl.dispose();
    _hostCtrl.dispose();
    super.dispose();
  }

  Future<void> _connect() async {
    final token = _ctrl.text.trim();
    if (token.isEmpty) return;
    // Optional relay host: blank or the app default both mean "use the default".
    final hostInput = _hostCtrl.text.trim();
    final host = (hostInput.isEmpty || hostInput == kRelayHost) ? '' : hostInput;
    // E2EE pairing material + device-auth flag from the scanned QR (absent for a
    // manual token entry). Device-auth only engages on later connects (post-pairing);
    // the first pairing connect still uses the token path.
    final raw = _scanned;
    final deviceAuth = raw != null && deviceAuthFromConnectString(raw);
    // LAN seed + relay-tier flag from the QR (absent on old QRs / manual entry).
    final lan = raw != null ? lanFromConnectString(raw) : const <String>[];
    final pub = raw != null ? pubFromConnectString(raw) : null;
    final m = await machines.add(
        label: _nameCtrl.text,
        token: token,
        host: host,
        requireDeviceAuth: deviceAuth,
        lanCandidates: lan,
        pubEnabled: pub);
    await bridge.connectMachine(
      m,
      pairingId: raw != null ? pairingIdFromConnectString(raw) : null,
      enrollPubB64: raw != null ? enrollPubFromConnectString(raw) : null,
      deviceName: m.label,
    );
    _scanned = null;
    if (mounted && Navigator.of(context).canPop()) Navigator.of(context).pop();
  }

  // Pull the token AND the relay host out of a scanned/pasted connect string, so
  // a machine is pinned to the relay its QR actually came from.
  void _applyConnectString(String s) {
    _scanned = s; // retain pid/epub for the pairing handshake
    final host = hostFromWsUrl(s);
    if (host != null && host.isNotEmpty) _hostCtrl.text = host;
    _ctrl.text = (tokenFromWsUrl(s) ?? s).trim();
    _ctrl.selection = TextSelection.collapsed(offset: _ctrl.text.length);
  }

  Future<void> _scanQr() async {
    final result = await Navigator.of(context).push<String>(
      MaterialPageRoute(builder: (_) => const QrScanScreen()),
    );
    if (!mounted || result == null || result.isEmpty) return;
    _applyConnectString(result);
    if (_ctrl.text.isEmpty) return;
    await _connect();
  }

  Future<void> _pasteFromClipboard() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final t = data?.text?.trim();
    if (t == null || t.isEmpty) return;
    _applyConnectString(t);
  }

  @override
  Widget build(BuildContext context) {
    final canPop = Navigator.of(context).canPop();
    return Scaffold(
      appBar: canPop
          ? AppBar(title: const Text('添加电脑'), titleSpacing: 0)
          : null,
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.symmetric(horizontal: 28, vertical: 32),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                const _Logo(),
                const SizedBox(height: 24),
                const Text('连接你的电脑',
                    textAlign: TextAlign.center,
                    style: TextStyle(fontSize: 26, fontWeight: FontWeight.w700, color: Cx.textPrimary)),
                const SizedBox(height: 8),
                const Text('扫描电脑菜单栏的二维码，或粘贴访问令牌',
                    textAlign: TextAlign.center,
                    style: TextStyle(color: Cx.textSecondary, fontSize: 14)),
                const SizedBox(height: 28),
                FilledButton.icon(
                  style: FilledButton.styleFrom(
                    backgroundColor: Cx.ink,
                    foregroundColor: Colors.white,
                    padding: const EdgeInsets.symmetric(vertical: 15),
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                  ),
                  onPressed: _scanQr,
                  icon: const Icon(Icons.qr_code_scanner_rounded, size: 20),
                  label: const Text('扫码连接', style: TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
                ),
                const SizedBox(height: 22),
                const Row(children: [
                  Expanded(child: Divider(color: Cx.border)),
                  Padding(
                    padding: EdgeInsets.symmetric(horizontal: 12),
                    child: Text('或手动输入', style: TextStyle(color: Cx.textFaint, fontSize: 12)),
                  ),
                  Expanded(child: Divider(color: Cx.border)),
                ]),
                const SizedBox(height: 18),
                _field(
                  controller: _nameCtrl,
                  hint: '电脑名称（可选，如：工作室 Mac）',
                  icon: Icons.computer_rounded,
                ),
                const SizedBox(height: 12),
                _field(
                  controller: _ctrl,
                  hint: '粘贴访问令牌',
                  icon: Icons.key_rounded,
                  mono: true,
                  obscure: _obscure,
                  onSubmitted: (_) => _connect(),
                  suffix: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      IconButton(
                        tooltip: '粘贴',
                        icon: const Icon(Icons.content_paste_rounded, size: 18, color: Cx.textFaint),
                        onPressed: _pasteFromClipboard,
                      ),
                      IconButton(
                        tooltip: _obscure ? '显示' : '隐藏',
                        icon: Icon(_obscure ? Icons.visibility_off_rounded : Icons.visibility_rounded,
                            size: 18, color: Cx.textFaint),
                        onPressed: () => setState(() => _obscure = !_obscure),
                      ),
                    ],
                  ),
                ),
                const SizedBox(height: 12),
                _field(
                  controller: _hostCtrl,
                  hint: '中继地址（可选，默认 $kRelayHost）',
                  icon: Icons.dns_rounded,
                  mono: true,
                ),
                const SizedBox(height: 16),
                ListenableBuilder(
                  listenable: bridge,
                  builder: (context, _) {
                    final connecting = bridge.state == ConnState.connecting;
                    return Column(
                      crossAxisAlignment: CrossAxisAlignment.stretch,
                      children: [
                        OutlinedButton(
                          style: OutlinedButton.styleFrom(
                            foregroundColor: Cx.textPrimary,
                            side: const BorderSide(color: Cx.border),
                            padding: const EdgeInsets.symmetric(vertical: 14),
                            shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                          ),
                          onPressed: connecting ? null : _connect,
                          child: connecting
                              ? const SizedBox(
                                  height: 18, width: 18,
                                  child: CircularProgressIndicator(strokeWidth: 2, color: Cx.textFaint))
                              : const Text('连接', style: TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
                        ),
                        if (bridge.state == ConnState.error && bridge.error != null) ...[
                          const SizedBox(height: 14),
                          Text('连接失败：${bridge.error}',
                              textAlign: TextAlign.center,
                              style: const TextStyle(color: Cx.offline, fontSize: 13)),
                        ],
                      ],
                    );
                  },
                ),
                const SizedBox(height: 24),
                const Text('令牌由设备管理员提供',
                    textAlign: TextAlign.center,
                    style: TextStyle(color: Cx.textFaint, fontSize: 11)),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _field({
    required TextEditingController controller,
    required String hint,
    required IconData icon,
    bool mono = false,
    bool obscure = false,
    Widget? suffix,
    ValueChanged<String>? onSubmitted,
  }) {
    return TextField(
      controller: controller,
      obscureText: obscure,
      autocorrect: false,
      enableSuggestions: false,
      style: TextStyle(fontFamily: mono ? Cx.mono : null, fontSize: 14, color: Cx.textPrimary),
      textInputAction: onSubmitted != null ? TextInputAction.go : TextInputAction.next,
      onSubmitted: onSubmitted,
      decoration: InputDecoration(
        hintText: hint,
        hintStyle: const TextStyle(color: Cx.textFaint),
        filled: true,
        fillColor: Cx.surface,
        prefixIcon: Icon(icon, color: Cx.textFaint, size: 20),
        suffixIcon: suffix,
        enabledBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(16),
          borderSide: const BorderSide(color: Cx.border, width: 1),
        ),
        focusedBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(16),
          borderSide: const BorderSide(color: Cx.ink, width: 1.4),
        ),
      ),
    );
  }
}

/// The app mark: a green terminal prompt on a light rounded square.
class _Logo extends StatelessWidget {
  const _Logo();
  @override
  Widget build(BuildContext context) {
    return Center(
      child: Container(
        width: 84,
        height: 84,
        decoration: BoxDecoration(
          color: Cx.surface,
          borderRadius: BorderRadius.circular(22),
          border: Border.all(color: Cx.border, width: 1),
        ),
        child: const Center(
          child: Text('>_',
              style: TextStyle(
                  fontFamily: Cx.mono,
                  fontSize: 38,
                  fontWeight: FontWeight.w700,
                  color: Cx.accent)),
        ),
      ),
    );
  }
}
