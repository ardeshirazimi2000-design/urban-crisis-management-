import 'dart:convert';

import 'package:crisis_core/crisis_core.dart';
import 'package:crisis_responder/screens.dart';
import 'package:crisis_responder/state.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

Map<String, dynamic> assignment(String status, int version) => {
      'id': 'a1', 'incident_id': 'i1', 'incident_code': 'INC-1', 'resource_id': 'r1', 'resource_name': 'آمبولانس ۱',
      'status': status, 'reason': 'زیر آوار', 'assigned_at': '2026-10-08T20:00:00Z', 'version': version,
    };

const _utf8 = {'content-type': 'application/json; charset=utf-8'};

void main() {
  late bool online;
  late String status;
  late int version;
  late ResponderState state;
  late Map<String, String> casualties;
  late List<String> assessments;
  var loseResponse = false;

  setUp(() async {
    online = true;
    casualties = {};
    assessments = [];
    loseResponse = false;
    status = 'assigned';
    version = 1;
    final client = MockClient((req) async {
      if (!online) throw http.ClientException('offline');
      if (req.url.path.endsWith('/assignments/mine')) {
        return http.Response(jsonEncode({'items': [assignment(status, version)]}), 200, headers: _utf8);
      }
      if (req.url.path.endsWith('/damage-assessments')) {
        assessments.add(req.headers['Idempotency-Key']!);
        return http.Response(jsonEncode({'id': 'd1', 'tag': 'red'}), 201, headers: _utf8);
      }
      if (req.url.path.endsWith('/casualties')) {
        final key = req.headers['Idempotency-Key']!;
        casualties.putIfAbsent(key, () => 'T-${(casualties.length + 1).toString().padLeft(5, '0')}');
        if (loseResponse) {
          loseResponse = false;
          throw http.ClientException('connection reset');
        }
        return http.Response(jsonEncode({'id': 'c-$key', 'tag_no': casualties[key], 'triage': 'immediate', 'status': 'on_scene'}), 201, headers: _utf8);
      }
      if (req.url.path.endsWith('/status')) {
        final b = jsonDecode(req.body) as Map<String, dynamic>;
        if (b['version'] != version) return http.Response(jsonEncode({'error': {'code': 'VERSION_CONFLICT', 'message': 'x'}}), 409, headers: _utf8);
        status = b['status'] as String;
        version++;
        return http.Response(jsonEncode(assignment(status, version)), 200, headers: _utf8);
      }
      return http.Response('{}', 404);
    });
    final api = ApiClient(Uri.parse('http://x/api/v1'), () async => 't', client: client);
    final key = StaticKeyProvider(List.filled(32, 3));
    state = ResponderState(api: api, queueStore: EncryptedJsonStore(MemoryByteStore(), key),
        cache: EncryptedJsonStore(MemoryByteStore(), key), readToken: () async => 't', writeToken: (_) async {});
    await state.queue.load();
    await state.sync();
  });

  test('offline status changes are projected locally and synced in order', () async {
    online = false;
    await state.setStatus(state.assignments.single, 'en_route');
    await state.setStatus(state.assignments.single, 'on_scene');
    expect(state.assignments.single.status, 'on_scene', reason: 'local projection while offline');
    expect(status, 'assigned', reason: 'server unchanged while offline');
    online = true;
    await state.sync();
    expect(status, 'on_scene');
    expect(state.queue.pending, isEmpty);
    expect(state.problems, isEmpty);
  });

  test('server-side cancellation wins over a stale offline update', () async {
    online = false;
    await state.setStatus(state.assignments.single, 'en_route');
    status = 'cancelled'; // commander cancelled meanwhile
    version = 5;
    online = true;
    await state.sync();
    expect(state.problems, hasLength(1));
    expect(state.assignments.single.status, 'cancelled');
  });

  testWidgets('mission card offers next lifecycle steps', (tester) async {
    await tester.pumpWidget(MaterialApp(home: Directionality(textDirection: TextDirection.rtl, child: MissionsScreen(state: state))));
    expect(find.text('INC-1 — آمبولانس ۱'), findsOneWidget);
    expect(find.text('در مسیر'), findsOneWidget);
    expect(find.text('دریافت شد'), findsOneWidget);
  });

  test('expired session renews itself with the saved sign-in; queued status change is not lost', () async {
    String? token;
    final prefs = <String, String?>{};
    final codes = <String?>[];
    var st = 'assigned', ver = 1;
    final client = MockClient((req) async {
      if (req.url.path.endsWith('/dev/token')) {
        final b = jsonDecode(req.body) as Map<String, dynamic>;
        codes.add(b['access_code'] as String?);
        if (b['access_code'] != 'staff-code') {
          return http.Response(jsonEncode({'error': {'code': 'ACCESS_CODE_INVALID', 'message': 'bad'}}), 401, headers: _utf8);
        }
        return http.Response(jsonEncode({'access_token': 'tok-${codes.length}'}), 200, headers: _utf8);
      }
      if (req.headers['Authorization'] != 'Bearer $token' || token == 'tok-1') {
        return http.Response(jsonEncode({'error': {'code': 'UNAUTHENTICATED', 'message': 'expired'}}), 401, headers: _utf8);
      }
      if (req.url.path.endsWith('/assignments/mine')) {
        return http.Response(jsonEncode({'items': [assignment(st, ver)]}), 200, headers: _utf8);
      }
      final b = jsonDecode(req.body) as Map<String, dynamic>;
      st = b['status'] as String;
      ver++;
      return http.Response(jsonEncode(assignment(st, ver)), 200, headers: _utf8);
    });
    final api = ApiClient(Uri.parse('http://x/api/v1'), () async => token, client: client);
    final key = StaticKeyProvider(List.filled(32, 3));
    final s = ResponderState(api: api, queueStore: EncryptedJsonStore(MemoryByteStore(), key),
        cache: EncryptedJsonStore(MemoryByteStore(), key), readToken: () async => token, writeToken: (t) async => token = t,
        readPref: (k) async => prefs[k], writePref: (k, v) async => prefs[k] = v);
    await s.queue.load();

    await expectLater(s.devSignIn('responder1', 'امدادگر', accessCode: 'wrong'), throwsA(isA<ApiException>()));
    expect(s.signedIn, isFalse);
    await s.devSignIn('responder1', 'امدادگر', accessCode: 'staff-code');
    expect(s.signedIn, isTrue);
    expect(s.assignments.single.status, 'assigned');

    token = 'tok-1'; // session expired
    await s.setStatus(s.assignments.single, 'en_route');
    expect(s.signedIn, isTrue);
    expect(st, 'en_route');
    expect(s.queue.pending, isEmpty);
    expect(codes.last, 'staff-code');
  });

  test('field triage recorded offline is sent once, even when a response is lost', () async {
    online = false;
    await state.recordCasualty(state.assignments.single, triage: 'immediate', ageGroup: 'elderly');
    expect(state.casualtiesFor('i1').single.state, OpState.pending);
    expect(casualtyLine(state.casualtiesFor('i1').single), contains('در صف'));

    online = true;
    loseResponse = true; // server creates it but the reply never arrives
    await state.sync();
    await state.sync();
    expect(casualties, hasLength(1), reason: 'exactly one casualty on the server');
    expect(casualtyLine(state.casualtiesFor('i1').single), startsWith('T-00001'));
  });

  test('building assessment is queued offline and sent with an idempotency key', () async {
    online = false;
    await state.assessBuilding(at: const GeoLocation(lat: 35.3, lng: 47.0, accuracyM: 10), tag: 'red', buildingUse: 'school',
        observations: ['collapse_partial'], peopleTrapped: true, incidentId: 'i1');
    expect(assessmentLine(state.assessments.single), contains('در صف'));
    online = true;
    await state.sync();
    expect(assessments, hasLength(1));
    expect(assessmentLine(state.assessments.single), contains('ثبت شد'));
  });
}
