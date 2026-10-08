import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:uuid/uuid.dart';

import 'models.dart';

const _uuid = Uuid();

/// Error in the platform's fixed envelope. [isRetryable] separates transport failures (keep queued)
/// from definitive rejections (surface to the user).
class ApiException implements Exception {
  final int status;
  final String code;
  final String message;
  final String correlationId;
  final List<Map<String, dynamic>> details;

  ApiException(this.status, this.code, this.message, {this.correlationId = '', this.details = const []});

  bool get isNetwork => status == 0;
  bool get isRetryable => status == 0 || status == 429 || status >= 500 || code == 'IDEMPOTENCY_IN_PROGRESS';

  @override
  String toString() => 'ApiException($status $code: $message, correlation=$correlationId)';
}

typedef TokenProvider = Future<String?> Function();

class ApiClient {
  final Uri baseUrl;
  final TokenProvider token;
  final http.Client _http;
  final Duration timeout;

  ApiClient(this.baseUrl, this.token, {http.Client? client, this.timeout = const Duration(seconds: 15)})
      : _http = client ?? http.Client();

  Future<dynamic> request(String method, String path,
      {Object? body, String? idempotencyKey, Map<String, String>? query}) async {
    final uri = baseUrl.replace(path: '${baseUrl.path}$path', queryParameters: query);
    final headers = <String, String>{'X-Correlation-ID': _uuid.v4(), 'Accept': 'application/json'};
    final t = await token();
    if (t != null) headers['Authorization'] = 'Bearer $t';
    if (body != null) headers['Content-Type'] = 'application/json';
    if (idempotencyKey != null) headers['Idempotency-Key'] = idempotencyKey;
    final req = http.Request(method, uri)..headers.addAll(headers);
    if (body != null) req.body = jsonEncode(body);
    http.Response res;
    try {
      res = await http.Response.fromStream(await _http.send(req).timeout(timeout));
    } catch (_) {
      throw ApiException(0, 'NETWORK', 'ارتباط با سرور برقرار نیست', correlationId: headers['X-Correlation-ID']!);
    }
    final text = utf8.decode(res.bodyBytes);
    final data = text.isEmpty ? null : jsonDecode(text);
    if (res.statusCode >= 400) {
      final e = (data is Map ? data['error'] : null) as Map<String, dynamic>? ?? {};
      throw ApiException(res.statusCode, (e['code'] as String?) ?? 'HTTP_${res.statusCode}',
          (e['message'] as String?) ?? 'خطا', correlationId: (e['correlation_id'] as String?) ?? '',
          details: ((e['details'] as List?) ?? []).cast<Map<String, dynamic>>());
    }
    return data;
  }

  Future<ReportReceipt> createReport(ReportSubmission r, String idempotencyKey) async =>
      ReportReceipt.fromJson(await request('POST', '/reports', body: r.toJson(), idempotencyKey: idempotencyKey) as Map<String, dynamic>);

  Future<List<MyReport>> myReports() async {
    final d = await request('GET', '/reports/mine') as Map<String, dynamic>;
    return (d['items'] as List).map((e) => MyReport.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<List<PublicAlert>> activeAlerts(double lat, double lng) async {
    final d = await request('GET', '/alerts/public', query: {'lat': '$lat', 'lng': '$lng'}) as Map<String, dynamic>;
    return (d['items'] as List).map((e) => PublicAlert.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<List<Assignment>> myAssignments() async {
    final d = await request('GET', '/assignments/mine') as Map<String, dynamic>;
    return (d['items'] as List).map((e) => Assignment.fromJson(e as Map<String, dynamic>)).toList();
  }

  /// Local development login (API must run with APP_ENV=local, AUTH_MODE=dev).
  Future<String> devToken(String subject, String name, List<Map<String, String>> grants) async {
    final d = await request('POST', '/dev/token', body: {'subject': subject, 'name': name, 'grants': grants}) as Map<String, dynamic>;
    return d['access_token'] as String;
  }
}
