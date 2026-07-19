import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/config.dart';

void main() {
  const qr =
      'ws://1.2.3.4:39001/ws?token=abc&lan=192.168.1.5%3A8767%2C10.0.0.3%3A8767&pub=0';

  test('lanFromConnectString 解析逗号列表', () {
    expect(lanFromConnectString(qr), ['192.168.1.5:8767', '10.0.0.3:8767']);
    expect(lanFromConnectString('ws://h/ws?token=t'), isEmpty); // 老 QR
    expect(lanFromConnectString('not a url %%%'), isEmpty);
  });

  test('pubFromConnectString 三态', () {
    expect(pubFromConnectString(qr), false);
    expect(pubFromConnectString('ws://h/ws?token=t&pub=1'), true);
    expect(pubFromConnectString('ws://h/ws?token=t'), isNull); // 老 QR
  });

  test('lanWsUrl 构造直连地址', () {
    expect(lanWsUrl(' tok ', '192.168.1.5:8767'),
        'ws://192.168.1.5:8767/ws?token=tok');
  });
}
