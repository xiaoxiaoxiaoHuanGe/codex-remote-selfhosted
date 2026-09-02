class CodexModel {
  final String id;
  final String label;
  final List<String> reasoningEfforts;
  final String defaultReasoningEffort;
  final bool isDefault;

  const CodexModel({
    required this.id,
    required this.label,
    required this.reasoningEfforts,
    required this.defaultReasoningEffort,
    this.isDefault = false,
  });

  factory CodexModel.fromJson(Map<String, dynamic> json) {
    final id = (json['id'] as String?)?.trim() ?? '';
    final label = (json['label'] as String?)?.trim();
    final efforts = (json['efforts'] as List?)
            ?.whereType<String>()
            .where((e) => e.isNotEmpty)
            .toList() ??
        const <String>[];
    final defaultEffort = (json['defaultEffort'] as String?)?.trim() ?? '';
    return CodexModel(
      id: id,
      label: label == null || label.isEmpty ? id : label,
      reasoningEfforts: efforts,
      defaultReasoningEffort: defaultEffort,
      isDefault: json['default'] == true,
    );
  }

  static List<CodexModel> fallback() => const [
        CodexModel(
          id: 'gpt-5.5',
          label: 'GPT-5.5',
          reasoningEfforts: ['low', 'medium', 'high', 'xhigh'],
          defaultReasoningEffort: 'medium',
        ),
        CodexModel(
          id: 'gpt-5',
          label: 'GPT-5',
          reasoningEfforts: ['low', 'medium', 'high', 'xhigh'],
          defaultReasoningEffort: 'medium',
        ),
        CodexModel(
          id: 'gpt-5-mini',
          label: 'GPT-5 mini',
          reasoningEfforts: ['low', 'medium', 'high', 'xhigh'],
          defaultReasoningEffort: 'medium',
        ),
      ];
}
