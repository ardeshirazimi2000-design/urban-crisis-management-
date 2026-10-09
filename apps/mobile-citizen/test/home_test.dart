import 'dart:convert';

import 'package:crisis_citizen/screens.dart';
import 'package:crisis_citizen/state.dart';
import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  CitizenState build({required bool online}) {
    final client = MockClient((req) async {
      if (!online) throw http.ClientException('offline');
      if (req.url.path.endsWith('/reports/mine')) return http.Response(jsonEncode({'items': []}), 200);
      return http.Response('{}', 404);
    });
    final api = ApiClient(Uri.parse('http://x/api/v1'), () async => 't', client: client);
    final key = StaticKeyProvider(List.filled(32, 7));
    return CitizenState(
      api: api,
      queue: OfflineQueue(EncryptedJsonStore(MemoryByteStore(), key), api),
      alertCache: EncryptedJsonStore(MemoryByteStore(), key),
      readToken: () async => 't',
      writeToken: (_) async {},
    );
  }

  testWidgets('offline banner and pending queue are shown', (tester) async {
    final s = build(online: false);
    await s.queue.load();
    await s.queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports',
        body: {'type': 'fire', 'location': {'lat': 35.7, 'lng': 51.4, 'accuracy_m': 5}});
    await s.sync();
    await tester.pumpWidget(MaterialApp(home: Directionality(textDirection: TextDirection.rtl, child: HomeScreen(state: s))));
    expect(find.text('اتصال برقرار نیست'), findsOneWidget);
    expect(find.textContaining('در صف ارسال'), findsOneWidget);
    expect(find.text('ثبت گزارش'), findsOneWidget);
  });

  testWidgets('test server access code: asked once, then the citizen signs in with a stable identity', (tester) async {
    String? token;
    final prefs = <String, String?>{};
    final subjects = <String>[];
    final client = MockClient((req) async {
      if (req.url.path.endsWith('/dev/token')) {
        final body = jsonDecode(req.body) as Map<String, dynamic>;
        subjects.add(body['subject'] as String);
        if (body['access_code'] != 'right-code') {
          return http.Response(jsonEncode({'error': {'code': 'ACCESS_CODE_INVALID', 'message': 'bad code'}}), 401);
        }
        return http.Response(jsonEncode({'access_token': 'tok-${subjects.length}'}), 200);
      }
      if (req.headers['Authorization'] == null) return http.Response(jsonEncode({'error': {'code': 'UNAUTHENTICATED'}}), 401);
      if (req.url.path.endsWith('/reports/mine')) return http.Response(jsonEncode({'items': []}), 200);
      return http.Response('{}', 404);
    });
    final api = ApiClient(Uri.parse('http://x/api/v1'), () async => token, client: client);
    final key = StaticKeyProvider(List.filled(32, 7));
    final s = CitizenState(
      api: api,
      queue: OfflineQueue(EncryptedJsonStore(MemoryByteStore(), key), api),
      alertCache: EncryptedJsonStore(MemoryByteStore(), key),
      readToken: () async => token,
      writeToken: (t) async => token = t,
      readPref: (k) async => prefs[k],
      writePref: (k, v) async => prefs[k] = v,
    );
    await tester.runAsync(() => s.ensureIdentity());
    expect(s.needsAccessCode, isTrue);
    await tester.pumpWidget(MaterialApp(home: Directionality(textDirection: TextDirection.rtl, child: HomeScreen(state: s))));
    expect(find.text('کد دسترسی'), findsOneWidget);

    expect(await tester.runAsync(() => s.submitAccessCode('wrong')), isFalse);
    await tester.pump();
    expect(find.text('کد وارد‌شده درست نیست.'), findsOneWidget);

    expect(await tester.runAsync(() => s.submitAccessCode(' right-code ')), isTrue);
    await tester.pump();
    expect(find.text('کد دسترسی'), findsNothing);
    expect(token, isNotNull);
    expect(prefs['access_code'], 'right-code');
    expect(subjects.toSet(), hasLength(1), reason: 'same citizen identity across attempts');
  });

  test('expired cached alerts are not shown', () {
    final s = build(online: true);
    s.alerts = [
      PublicAlert('a', 'warning', 'old', 'x', null, DateTime.now().subtract(const Duration(minutes: 1)), 'r'),
      PublicAlert('b', 'warning', 'new', 'x', null, DateTime.now().add(const Duration(hours: 1)), 'r'),
    ];
    expect(s.activeAlerts.map((a) => a.id), ['b']);
  });
}
