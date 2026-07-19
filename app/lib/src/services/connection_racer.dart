// Happy-eyeballs candidate racing for the bridge link: LAN candidates dial
// immediately, the relay starts [relayDelay] later (giving same-WiFi direct a
// head start), and the FIRST completed dial wins. Losers are closed via
// [closeLoser] before they ever carry a frame, so the E2EE codec only arms on
// the winner. Generic over the channel type T so tests run without sockets.
import 'dart:async';

/// How the race was won.
class RaceOutcome<T> {
  final T channel;
  final String url;
  final bool direct; // true = a LAN candidate won
  RaceOutcome(this.channel, this.url, this.direct);
}

Future<RaceOutcome<T>> raceConnect<T>({
  required List<String> lanUrls,
  required String? relayUrl,
  required Future<T> Function(String url) dialer,
  required Future<void> Function(T channel) closeLoser,
  Duration relayDelay = const Duration(milliseconds: 300),
  Duration perTryTimeout = const Duration(seconds: 8),
}) {
  if (lanUrls.isEmpty && relayUrl == null) {
    return Future.error(StateError('no candidates'));
  }
  final done = Completer<RaceOutcome<T>>();
  final total = lanUrls.length + (relayUrl != null ? 1 : 0);
  var failures = 0;
  var relayStarted = false;
  Object lastErr = StateError('no candidates');
  Timer? relayTimer;

  Future<void> attempt(String url, bool direct) async {
    try {
      final ch = await dialer(url).timeout(perTryTimeout);
      if (done.isCompleted) {
        await closeLoser(ch); // lost — close before any frame travels
        return;
      }
      relayTimer?.cancel(); // a winner means the delayed relay never dials
      done.complete(RaceOutcome(ch, url, direct));
    } catch (e) {
      lastErr = e;
      failures++;
      if (failures == lanUrls.length && relayUrl != null && !relayStarted) {
        // Every LAN candidate already failed — don't sit out the grace delay.
        relayTimer?.cancel();
        relayStarted = true;
        // ignore: discarded_futures
        attempt(relayUrl, false);
      } else if (failures == total && !done.isCompleted) {
        done.completeError(lastErr);
      }
    }
  }

  for (final u in lanUrls) {
    // ignore: discarded_futures
    attempt(u, true);
  }
  if (relayUrl != null) {
    if (lanUrls.isEmpty) {
      relayStarted = true;
      // ignore: discarded_futures
      attempt(relayUrl, false);
    } else {
      relayTimer = Timer(relayDelay, () {
        if (relayStarted || done.isCompleted) return;
        relayStarted = true;
        // ignore: discarded_futures
        attempt(relayUrl, false);
      });
    }
  }
  return done.future;
}
