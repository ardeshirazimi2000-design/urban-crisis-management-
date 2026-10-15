import 'package:crisis_core/crisis_core.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

void main() {
  test('a proxy error page becomes an ApiException instead of an unhandled FormatException', () async {
    final api = ApiClient(Uri.parse('https://x/api/v1'), () async => null,
        client: MockClient((_) async => http.Response('<html>502 Bad Gateway</html>', 502)));
    await expectLater(
      api.request('POST', '/auth/app/start', body: {}),
      throwsA(isA<ApiException>().having((e) => e.code, 'code', 'BAD_RESPONSE').having((e) => e.status, 'status', 502)),
    );
  });
}
