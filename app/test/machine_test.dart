import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/models/machine.dart';

void main() {
  test('新字段序列化往返', () {
    const m = Machine(
      id: '1',
      label: 'Mac',
      token: 'tok',
      lanCandidates: ['192.168.1.5:8767'],
      pubEnabled: false,
      linkMode: 'lanOnly',
    );
    final back = Machine.fromJson(m.toJson());
    expect(back.lanCandidates, ['192.168.1.5:8767']);
    expect(back.pubEnabled, false);
    expect(back.linkMode, 'lanOnly');
  });

  test('旧 JSON 兼容：缺字段取默认', () {
    final m = Machine.fromJson({'id': '1', 'label': 'x', 'token': 't'});
    expect(m.lanCandidates, isEmpty);
    expect(m.pubEnabled, true); // 旧存档来自中继配对 → 公网可用
    expect(m.linkMode, 'auto');
  });

  test('copyWith 更新 lan 字段', () {
    const m = Machine(id: '1', label: 'x', token: 't');
    final n = m.copyWith(
        lanCandidates: ['10.0.0.3:8767'],
        pubEnabled: false,
        linkMode: 'relayOnly');
    expect(n.lanCandidates, ['10.0.0.3:8767']);
    expect(n.pubEnabled, false);
    expect(n.linkMode, 'relayOnly');
    expect(n.token, 't'); // 不变量保留
  });
}
