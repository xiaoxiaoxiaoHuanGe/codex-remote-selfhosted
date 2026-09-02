import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/models/codex_model.dart';

void main() {
  test('parses app-server model id and reasoning capabilities', () {
    final model = CodexModel.fromJson({
      'id': 'gpt-5.6-sol',
      'label': 'GPT-5.6-Sol',
      'efforts': ['low', 'max', 'ultra'],
      'defaultEffort': 'low',
      'default': true,
    });

    expect(model.id, 'gpt-5.6-sol');
    expect(model.label, 'GPT-5.6-Sol');
    expect(model.reasoningEfforts, ['low', 'max', 'ultra']);
    expect(model.defaultReasoningEffort, 'low');
    expect(model.isDefault, isTrue);
  });

  test('fallback is explicit and retains legacy model ids', () {
    final ids = CodexModel.fallback().map((m) => m.id).toList();
    expect(ids, containsAll(['gpt-5.5', 'gpt-5', 'gpt-5-mini']));
  });
}
