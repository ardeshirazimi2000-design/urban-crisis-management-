import 'dart:convert';
import 'dart:typed_data';

import 'package:crisis_core/crisis_core.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

/// Fake server: records requests, dedupes by Idempotency-Key and can simulate outages.
class FakeServer {
  bool online = true;
  bool loseResponses = false; // apply the effect but drop the response (ambiguous failure)
  final effects = <String, Map<String, dynamic>>{};
  final requests = <http.Request>[];
  String assignmentStatus = 'assigned';
  int assignmentVersion = 1;

  MockClient get client => MockClient((req) async {
        requests.add(req);
        if (!online) throw http.ClientException('offline');
        final key = req.headers['Idempotency-Key'];
        if (req.url.path.endsWith('/reports')) {
          final body = jsonDecode(req.body) as Map<String, dynamic>;
          if (body['type'] == 'alien') {
            return http.Response(jsonEncode({'error': {'code': 'VALIDATION_ERROR', 'message': 'bad', 'correlation_id': 'c1'}}), 422);
          }
          effects.putIfAbsent(key!, () => {'report_id': 'r-${effects.length + 1}', 'status': 'received',
              'received_at': '2026-10-08T20:00:00Z', 'correlation_id': 'c'});
          if (loseResponses) throw http.ClientException('connection reset');
          return http.Response(jsonEncode(effects[key]), 202);
        }
        if (req.url.path.contains('/assignments/')) {
          final body = jsonDecode(req.body) as Map<String, dynamic>;
          if (body['version'] != assignmentVersion) {
            return http.Response(jsonEncode({'error': {'code': 'VERSION_CONFLICT', 'message': 'conflict'}}), 409);
          }
          assignmentStatus = body['status'] as String;
          assignmentVersion++;
          if (loseResponses) throw http.ClientException('connection reset');
          return http.Response(jsonEncode({'status': assignmentStatus, 'version': assignmentVersion}), 200);
        }
        return http.Response('{}', 404);
      });
}

void main() {
  late FakeServer server;
  late MemoryByteStore bytes;
  late OfflineQueue queue;
  final key = StaticKeyProvider(List<int>.generate(32, (i) => i));

  OfflineQueue newQueue({ConflictResolver? resolver}) => OfflineQueue(
      EncryptedJsonStore(bytes, key), ApiClient(Uri.parse('http://api/api/v1'), () async => 't', client: server.client),
      resolveConflict: resolver);

  setUp(() {
    server = FakeServer();
    bytes = MemoryByteStore();
    queue = newQueue();
  });

  Map<String, dynamic> report([String type = 'fire']) =>
      {'type': type, 'description': 'دود', 'location': {'lat': 35.7, 'lng': 51.4, 'accuracy_m': 10}};

  test('queue survives restart and is encrypted at rest', () async {
    server.online = false;
    await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report());
    final raw = utf8.decode(bytes.data!, allowMalformed: true);
    expect(raw.contains('دود'), isFalse, reason: 'plaintext must not be stored');
    expect(raw.contains('report.create'), isFalse);

    final restarted = newQueue();
    await restarted.load();
    expect(restarted.pending, hasLength(1));
  });

  test('tampered storage is rejected', () async {
    await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report());
    bytes.data = Uint8List.fromList(bytes.data!..[20] ^= 0xff);
    expect(() => newQueue().load(), throwsA(anything));
  });

  test('offline: nothing lost, order kept, sync resumes (AT-12)', () async {
    server.online = false;
    await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report());
    await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report('gas_leak'));
    var r = await queue.flush();
    expect(r.stoppedOffline, isTrue);
    expect(queue.pending, hasLength(2));
    expect(server.requests, hasLength(1), reason: 'stop at first transport failure');

    server.online = true;
    r = await queue.flush();
    expect(r.sent, 2);
    expect(queue.pending, isEmpty);
    expect(server.effects, hasLength(2));
  });

  test('lost responses + retry produce exactly one server effect per operation', () async {
    server.loseResponses = true;
    final op = await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report());
    await queue.flush();
    await queue.flush();
    expect(server.effects, hasLength(1));
    expect(queue.pending.single.id, op.id);
    server.loseResponses = false;
    await queue.flush();
    expect(server.effects, hasLength(1), reason: 'same Idempotency-Key on every retry');
    expect(server.requests.map((r) => r.headers['Idempotency-Key']).toSet(), {op.id});
    expect(queue.all.single.state, OpState.done);
  });

  test('validation rejection is kept for the user and does not block later items', () async {
    await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report('alien'));
    await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: report());
    final r = await queue.flush();
    expect(r.rejected, 1);
    expect(r.sent, 1);
    expect(queue.all.first.state, OpState.rejected);
    expect(queue.all.first.correlationId, 'c1');
  });

  test('version conflict: server-authoritative, resolver detects already-applied ops', () async {
    queue = newQueue(resolver: (op) async => server.assignmentStatus == op.body['status']);
    server.loseResponses = true;
    await queue.enqueue(kind: 'assignment.status', method: 'POST', path: '/assignments/a1/status',
        body: {'status': 'en_route', 'version': 1}, idempotent: false);
    await queue.flush(); // applied, response lost
    server.loseResponses = false;
    await queue.flush(); // replay -> 409, resolver sees en_route already applied
    expect(queue.all.single.state, OpState.done);

    // A genuinely stale update (someone else changed it) is recorded as a conflict.
    server.assignmentStatus = 'cancelled';
    server.assignmentVersion = 5;
    await queue.enqueue(kind: 'assignment.status', method: 'POST', path: '/assignments/a1/status',
        body: {'status': 'on_scene', 'version': 2}, idempotent: false);
    final r = await queue.flush();
    expect(r.conflicts, 1);
    expect(queue.all.last.state, OpState.conflict);
  });
}
