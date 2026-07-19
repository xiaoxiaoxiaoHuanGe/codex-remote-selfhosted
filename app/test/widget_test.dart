import 'package:flutter_test/flutter_test.dart';

import 'package:codex_remote_app/main.dart';

void main() {
  testWidgets('app boots', (tester) async {
    await tester.pumpWidget(const CodexRemoteApp());
    expect(find.byType(CodexRemoteApp), findsOneWidget);
  });
}
