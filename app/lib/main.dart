import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'src/config.dart';
import 'src/services/bridge_client.dart';
import 'src/services/machine_store.dart';
import 'src/services/secure_store.dart';
import 'src/ui/connect_screen.dart';
import 'src/ui/sessions_screen.dart';
import 'src/ui/theme.dart';

/// Single app-wide instances (kept simple for the MVP; no DI container).
final SecureStore store = SecureStore();
final BridgeClient bridge = BridgeClient(store);
final MachineStore machines = MachineStore(store);

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  SystemChrome.setSystemUIOverlayStyle(cxOverlayStyle);
  runApp(const CodexRemoteApp());
}

class CodexRemoteApp extends StatelessWidget {
  const CodexRemoteApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Caret',
      debugShowCheckedModeBanner: false,
      theme: codexTheme(),
      home: const RootScreen(),
    );
  }
}

/// Loads saved machines on launch, auto-connects the active one, then shows the
/// home (Codex) screen — or the connect screen if no machine has been added yet.
class RootScreen extends StatefulWidget {
  const RootScreen({super.key});

  @override
  State<RootScreen> createState() => _RootScreenState();
}

class _RootScreenState extends State<RootScreen> with WidgetsBindingObserver {
  // Optional launch-time overrides for testing:
  //   --dart-define=WS_URL=wss://...   full URL, added + auto-connected
  //   --dart-define=TOKEN=...          token only, added + auto-connected
  static const _envUrl = String.fromEnvironment('WS_URL');
  static const _envToken = String.fromEnvironment('TOKEN');
  static const _envName = String.fromEnvironment('MACHINE_NAME');

  bool _booted = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // lanInfo frames refresh the machine's direct-connect cache the next
    // (re)connect races against.
    bridge.onLanInfo = (id, cands, pub) => machines.updateLanInfo(id, cands, pub);
    _boot();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state != AppLifecycleState.resumed || bridge.suppressAutoConnect) {
      return;
    }
    final a = machines.active;
    if (a == null) return;
    if (bridge.state == ConnState.connected) {
      bridge.syncNow();
      return;
    }
    if (bridge.state != ConnState.connecting) {
      bridge.connectMachine(a);
    }
  }

  Future<void> _boot() async {
    await machines.load();

    // A build may bake in a connection string via --dart-define. Use it only to
    // seed the FIRST machine on a fresh install — never re-add it or override the
    // active machine a returning user has already chosen.
    if (machines.machines.isEmpty) {
      String? envTok;
      String envHost = '';
      if (_envUrl.isNotEmpty) {
        envTok = tokenFromWsUrl(_envUrl);
        envHost = hostFromWsUrl(_envUrl) ?? '';
      }
      if ((envTok == null || envTok.isEmpty) && _envToken.isNotEmpty) {
        envTok = _envToken;
      }
      if (envTok != null && envTok.isNotEmpty) {
        await machines.add(
            label: _envName.isNotEmpty ? _envName : '我的电脑',
            token: envTok,
            host: envHost);
      }
    }

    final a = machines.active;
    if (a != null && !bridge.suppressAutoConnect) {
      await bridge.connectMachine(a);
    }
    if (mounted) setState(() => _booted = true);
  }

  @override
  Widget build(BuildContext context) {
    final Widget child = !_booted
        ? const _Splash()
        : ListenableBuilder(
            listenable: Listenable.merge([bridge, machines]),
            builder: (context, _) => machines.machines.isEmpty
                ? const ConnectScreen()
                : const SessionsScreen(),
          );
    // Enforce the light, transparent status bar everywhere — including the
    // AppBar-less home — so the system bar never falls back to a gray scrim.
    return AnnotatedRegion<SystemUiOverlayStyle>(
        value: cxOverlayStyle, child: child);
  }
}

class _Splash extends StatelessWidget {
  const _Splash();
  @override
  Widget build(BuildContext context) {
    return const Scaffold(
      backgroundColor: Cx.bg,
      body: Center(
        child: SizedBox(
          width: 22,
          height: 22,
          child: CircularProgressIndicator(strokeWidth: 2, color: Cx.textFaint),
        ),
      ),
    );
  }
}
