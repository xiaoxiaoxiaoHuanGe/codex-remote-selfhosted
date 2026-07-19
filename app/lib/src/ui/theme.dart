import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

/// Codex official-style palette — light & near-monochrome. Green is reserved for
/// the "online" status dot and the brand mark; everything else is ink/gray.
class Cx {
  static const bg = Color(0xFFFFFFFF);
  static const surface = Color(0xFFF2F2F3); // chips, fields, code blocks
  static const surfaceAlt = Color(0xFFE9E9EB); // pressed / alt fills
  static const card = Color(0xFFFFFFFF); // composer card
  static const border = Color(0xFFE6E6E8);
  static const hairline = Color(0xFFEFEFF1);

  static const ink = Color(0xFF0D0D0D); // dark pills / FAB / send (primary action)
  static const textPrimary = Color(0xFF0D0D0D);
  static const textSecondary = Color(0xFF6B6B70);
  static const textFaint = Color(0xFFA6A6AC);

  static const online = Color(0xFF34C759);
  static const offline = Color(0xFFFF3B30);
  static const danger = offline; // backward-compatible alias
  static const warn = Color(0xFFE0922F);
  static const accent = Color(0xFF6E56CF); // Caret brand indigo — accents only

  static const mono = 'monospace';

  static const cardShadow = <BoxShadow>[
    BoxShadow(color: Color(0x12000000), blurRadius: 22, offset: Offset(0, 8)),
  ];
}

/// One overlay style for the whole app: transparent status/nav bars with dark
/// icons (we're a light app). Applied globally in main(), on every AppBar via
/// [codexTheme], and on the AppBar-less home via an AnnotatedRegion — so the
/// system status bar never shows the default gray scrim.
const SystemUiOverlayStyle cxOverlayStyle = SystemUiOverlayStyle(
  statusBarColor: Colors.transparent,
  statusBarIconBrightness: Brightness.dark, // Android: dark icons
  statusBarBrightness: Brightness.light, // iOS: dark icons on light bg
  systemStatusBarContrastEnforced: false,
  systemNavigationBarColor: Cx.bg,
  systemNavigationBarIconBrightness: Brightness.dark,
  systemNavigationBarContrastEnforced: false,
);

ThemeData codexTheme() {
  final base = ThemeData.light(useMaterial3: true);
  return base.copyWith(
    scaffoldBackgroundColor: Cx.bg,
    colorScheme: base.colorScheme.copyWith(
      primary: Cx.ink,
      secondary: Cx.ink,
      surface: Cx.bg,
      onSurface: Cx.textPrimary,
      onPrimary: Colors.white,
    ),
    appBarTheme: const AppBarTheme(
      backgroundColor: Cx.bg,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      systemOverlayStyle: cxOverlayStyle,
      titleTextStyle: TextStyle(color: Cx.textPrimary, fontSize: 17, fontWeight: FontWeight.w600),
      iconTheme: IconThemeData(color: Cx.textPrimary),
    ),
    dividerTheme: const DividerThemeData(color: Cx.hairline, thickness: 1, space: 1),
    textSelectionTheme: const TextSelectionThemeData(cursorColor: Cx.ink),
    splashFactory: NoSplash.splashFactory,
    highlightColor: Colors.transparent,
  );
}

/// A circular icon button sitting on a light-gray disc — the Codex header buttons.
class CxCircleButton extends StatelessWidget {
  final IconData icon;
  final VoidCallback onTap;
  final String? tooltip;
  final double size;
  final Color? fg;
  const CxCircleButton(this.icon,
      {super.key, required this.onTap, this.tooltip, this.size = 40, this.fg});

  @override
  Widget build(BuildContext context) {
    final w = Material(
      color: Cx.surface,
      shape: const CircleBorder(),
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: onTap,
        child: SizedBox(
          width: size,
          height: size,
          child: Icon(icon, size: 20, color: fg ?? Cx.textPrimary),
        ),
      ),
    );
    return tooltip == null ? w : Tooltip(message: tooltip!, child: w);
  }
}

/// Small status dot (online green / offline red / neutral gray).
class CxDot extends StatelessWidget {
  final Color color;
  final double size;
  const CxDot(this.color, {super.key, this.size = 8});

  @override
  Widget build(BuildContext context) => Container(
        width: size,
        height: size,
        decoration: BoxDecoration(color: color, shape: BoxShape.circle),
      );
}

/// Small section label (e.g. "项目"). Sentence case — Codex doesn't uppercase.
class CxLabel extends StatelessWidget {
  final String text;
  final Color? color;
  const CxLabel(this.text, {super.key, this.color});

  @override
  Widget build(BuildContext context) => Text(
        text,
        style: TextStyle(
          color: color ?? Cx.textSecondary,
          fontSize: 15,
          fontWeight: FontWeight.w600,
        ),
      );
}
