/// A Codex session (thread) as returned by the bridge's `sessions` message.
class Session {
  final String id;
  final String name; // AI-generated short title (may be empty)
  final String preview; // first user message
  final String cwd;
  final String source; // e.g. "vscode", "cli"
  final int updatedAt;

  const Session({
    required this.id,
    required this.name,
    required this.preview,
    required this.cwd,
    required this.source,
    required this.updatedAt,
  });

  /// Best human label for the session.
  String get title => name.isNotEmpty ? name : (preview.isNotEmpty ? preview : id);

  /// Immutable copy with selected fields replaced (used to patch a session's
  /// name in place when the desktop renames it — `thread/name/updated`).
  Session copyWith({String? name}) => Session(
        id: id,
        name: name ?? this.name,
        preview: preview,
        cwd: cwd,
        source: source,
        updatedAt: updatedAt,
      );

  factory Session.fromJson(Map<String, dynamic> j) => Session(
        id: (j['id'] as String?) ?? '',
        name: (j['name'] as String?) ?? '',
        preview: (j['preview'] as String?) ?? '',
        cwd: (j['cwd'] as String?) ?? '',
        source: (j['source'] as String?) ?? '',
        updatedAt: (j['updatedAt'] as num?)?.toInt() ?? 0,
      );
}
