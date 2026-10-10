import 'dart:convert';
import 'dart:io';

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

  test('nearby shelters are fetched, cached encrypted, and kept visible when offline', () async {
    var online = true;
    final client = MockClient((req) async {
      if (!online) throw http.ClientException('offline');
      if (req.url.path.endsWith('/shelters/public')) {
        expect(req.url.queryParameters['lat'], '35.7');
        return http.Response(jsonEncode({'items': [
          {'id': 's1', 'name': 'سالن ورزشی', 'organization': 'شهرداری', 'location': {'lat': 35.71, 'lng': 51.41},
            'distance_m': 1500.0, 'available': 550, 'capacity': 900, 'updated_at': '2026-10-10T10:00:00Z'},
        ]}), 200, headers: {'content-type': 'application/json; charset=utf-8'});
      }
      return http.Response('{}', 404);
    });
    final api = ApiClient(Uri.parse('http://x/api/v1'), () async => 't', client: client);
    final key = StaticKeyProvider(List.filled(32, 7));
    final bytes = MemoryByteStore();
    CitizenState mk() => CitizenState(api: api, queue: OfflineQueue(EncryptedJsonStore(MemoryByteStore(), key), api),
        alertCache: EncryptedJsonStore(MemoryByteStore(), key), shelterCache: EncryptedJsonStore(bytes, key),
        readToken: () async => 't', writeToken: (_) async {});
    final s = mk();
    const here = GeoLocation(lat: 35.7, lng: 51.4, accuracyM: 20);
    await s.refreshShelters(here);
    expect(s.shelters.single.available, 550);
    expect(s.shelterError, isNull);

    online = false;
    await s.refreshShelters(here);
    expect(s.shelters, hasLength(1), reason: 'last list stays visible offline');
    expect(s.shelterError, contains('اتصال'));

    final restarted = mk(); // app restart while offline: list comes from the encrypted cache
    await restarted.queue.load();
    await restarted.start();
    expect(restarted.shelters.single.name, 'سالن ورزشی');
    expect(restarted.sheltersFetchedAt, isNotNull);
    restarted.dispose();
  });

  testWidgets('assistant answers from approved guidance on the device, emergencies with call buttons', (tester) async {
    final requests = <Uri>[];
    final client = MockClient((req) async {
      requests.add(req.url);
      return http.Response('{}', 404);
    });
    final api = ApiClient(Uri.parse('http://x/api/v1'), () async => 't', client: client);
    final key = StaticKeyProvider(List.filled(32, 7));
    final s = CitizenState(api: api, queue: OfflineQueue(EncryptedJsonStore(MemoryByteStore(), key), api),
        alertCache: EncryptedJsonStore(MemoryByteStore(), key), guidanceCache: EncryptedJsonStore(MemoryByteStore(), key),
        bundledGuidance: () => File('assets/guidance.fa.json').readAsString(), readToken: () async => 't', writeToken: (_) async {});
    await tester.runAsync(() => s.loadGuidance());
    await tester.pumpWidget(MaterialApp(home: Directionality(textDirection: TextDirection.rtl, child: AssistantScreen(state: s))));
    await tester.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await tester.pump();
    expect(find.text('بوی گاز می‌آید'), findsOneWidget); // offered as a suggestion

    await tester.enterText(find.byType(TextField), 'بوی گاز میاد چیکار کنم');
    await tester.testTextInput.receiveAction(TextInputAction.send);
    await tester.pumpAndSettle();
    expect(find.textContaining('شیر اصلی گاز'), findsOneWidget);
    expect(find.text('تماس با ۱۹۴'), findsOneWidget);
    expect(requests.where((u) => u.queryParameters.values.any((v) => v.contains('گاز'))), isEmpty,
        reason: 'the question must never leave the device');

    await tester.enterText(find.byType(TextField), 'قیمت دلار امروز');
    await tester.testTextInput.receiveAction(TextInputAction.send);
    await tester.pumpAndSettle();
    expect(find.textContaining('راهنمای تأییدشده‌ای پیدا نشد'), findsOneWidget);
  });

  test('bundled guidance is identical to the server seed', () {
    expect(File('assets/guidance.fa.json').readAsStringSync(),
        File('../../services/core/seeds/guidance.fa.json').readAsStringSync());
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
