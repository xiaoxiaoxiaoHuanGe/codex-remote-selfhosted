import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/services/connection_racer.dart';

void main() {
  // 测试用 T=String：dialer 返回 url 本身当作"连接"，closer 记录被关掉的败者。
  Future<String> ok(String url, [Duration d = Duration.zero]) =>
      Future.delayed(d, () => url);

  test('LAN 先胜：中继被 300ms 延迟，LAN 立即成功', () async {
    final closed = <String>[];
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan/ws'],
      relayUrl: 'wss://relay/ws',
      dialer: (u) => u.contains('lan')
          ? ok(u)
          : ok(u, const Duration(milliseconds: 50)),
      closeLoser: (c) async => closed.add(c),
    );
    expect(out.direct, true);
    expect(out.url, 'ws://lan/ws');
    await Future<void>.delayed(const Duration(milliseconds: 400));
    expect(closed, isEmpty); // 中继 timer 被取消，从未拨出
  });

  test('LAN 全失败：中继不等满 300ms 立即起拨并胜出', () async {
    final sw = Stopwatch()..start();
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan1/ws', 'ws://lan2/ws'],
      relayUrl: 'wss://relay/ws',
      dialer: (u) =>
          u.contains('lan') ? Future.error(StateError('refused')) : ok(u),
      closeLoser: (_) async {},
    );
    expect(out.direct, false);
    expect(sw.elapsedMilliseconds, lessThan(250)); // 没有干等 grace
  });

  test('败者被关闭：双方都成功，先到者胜', () async {
    final closed = <String>[];
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan/ws'],
      relayUrl: 'wss://relay/ws',
      relayDelay: Duration.zero,
      dialer: (u) => u.contains('lan')
          ? ok(u, const Duration(milliseconds: 30))
          : ok(u),
      closeLoser: (c) async => closed.add(c),
    );
    expect(out.direct, false); // relayDelay=0 且中继更快
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(closed, ['ws://lan/ws']); // 迟到的 LAN 连接被立即关闭
  });

  test('全部失败：抛出最后一个错误', () async {
    expect(
      raceConnect<String>(
        lanUrls: ['ws://lan/ws'],
        relayUrl: 'wss://relay/ws',
        dialer: (_) => Future.error(StateError('down')),
        closeLoser: (_) async {},
      ),
      throwsStateError,
    );
  });

  test('无任何候选：StateError', () {
    expect(
      raceConnect<String>(
          lanUrls: const [],
          relayUrl: null,
          dialer: (u) => ok(u),
          closeLoser: (_) async {}),
      throwsStateError,
    );
  });

  test('lanOnly（relayUrl=null）：只拨 LAN', () async {
    final dialed = <String>[];
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan/ws'],
      relayUrl: null,
      dialer: (u) {
        dialed.add(u);
        return ok(u);
      },
      closeLoser: (_) async {},
    );
    expect(out.direct, true);
    expect(dialed, ['ws://lan/ws']);
  });
}
