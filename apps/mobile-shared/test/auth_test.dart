import 'dart:convert';

import 'package:crisis_core/crisis_core.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

http.Response json(Object body, [int status = 200]) =>
    http.Response.bytes(utf8.encode(jsonEncode(body)), status, headers: {'content-type': 'application/json'});

void main() {
  test('first sign-in sends no device token; later sign-ins present it; expired access tokens renew once', () async {
    final starts = <Map<String, dynamic>>[];
    var refreshes = 0;
    var access = 'a1';
    final client = MockClient((req) async {
      switch (req.url.path) {
        case '/api/v1/auth/app/start':
          starts.add(jsonDecode(req.body) as Map<String, dynamic>);
          return json({'challenge': {'challenge_id': 'c1', 'mobile_masked': '0912***4567', 'expires_in': 120},
            'shahkar_verified': starts.length == 1, 'needs_profile': starts.length == 1});
        case '/api/v1/auth/app/verify':
          return json({'tokens': {'access_token': access, 'refresh_token': 'r1'}, 'device_token': 'dt-1',
            'access_history': {'failed_since_last_login': 2}});
        case '/api/v1/auth/refresh':
          refreshes++;
          expect(req.headers['Authorization'], isNull);
          access = 'a2';
          return json({'access_token': 'a2', 'refresh_token': 'r2'});
        case '/api/v1/reports/mine':
          if (req.headers['Authorization'] != 'Bearer a2') return json({'error': {'code': 'UNAUTHENTICATED', 'message': 'x'}}, 401);
          return json({'items': []});
      }
      return json({}, 404);
    });
    final store = MemoryCredentialStore();
    late SessionAuth auth;
    final api = ApiClient(Uri.parse('http://x/api/v1'), () => auth.accessToken(), client: client);
    auth = SessionAuth(api, store);

    final c = await auth.start('0010000003', '09121234567');
    expect(c.shahkarVerified, isTrue);
    expect(c.needsProfile, isTrue);
    expect(starts.single['device_token'], '');
    final h = await auth.verify(c, '123456', firstName: 'مریم', lastName: 'احمدی');
    expect(h.failedSinceLastLogin, 2);
    expect(await auth.signedIn, isTrue);

    await auth.start('0010000003', '09121234567');
    expect(starts.last['device_token'], 'dt-1');
    expect(starts.last['device_id'], starts.first['device_id']);

    expect(await api.myReports(), isEmpty);
    expect(refreshes, 1);
    expect(await store.read('auth.refresh_token'), 'r2');
  });

  test('a rejected refresh token signs out; a network failure keeps the session', () async {
    var online = true;
    final client = MockClient((req) async {
      if (!online) throw http.ClientException('offline');
      if (req.url.path.endsWith('/auth/refresh')) return json({'error': {'code': 'SESSION_ENDED', 'message': 'x'}}, 401);
      return json({'error': {'code': 'UNAUTHENTICATED', 'message': 'x'}}, 401);
    });
    final store = MemoryCredentialStore()..values.addAll({'auth.access_token': 'a', 'auth.refresh_token': 'r'});
    final api = ApiClient(Uri.parse('http://x/api/v1'), () => store.read('auth.access_token'), client: client);
    final auth = SessionAuth(api, store);
    online = false;
    expect(await auth.refresh(), isFalse);
    expect(await auth.signedIn, isTrue);
    online = true;
    await expectLater(api.myReports(), throwsA(isA<ApiException>().having((e) => e.code, 'code', 'UNAUTHENTICATED')));
    expect(await auth.signedIn, isFalse);
  });
}
