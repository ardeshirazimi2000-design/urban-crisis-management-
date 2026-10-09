import 'dart:async';

import 'package:uuid/uuid.dart';

import 'api_client.dart';
import 'error_text.dart';
import 'secure_store.dart';

const _uuid = Uuid();

enum OpState { pending, done, rejected, conflict }

/// One operation captured while possibly offline. Its [id] doubles as the Idempotency-Key, so every retry
/// of the same operation has exactly one effect on the server (AT-12).
class QueuedOp {
  final String id;
  final String kind; // report.create | assignment.status | resource.location
  final String method;
  final String path;
  final Map<String, dynamic> body;
  final bool idempotent;

  /// Device time when the user acted (kept separate from the server's receipt time).
  final DateTime occurredAt;
  int attempts;
  OpState state;
  String? lastError;
  String? correlationId;
  Map<String, dynamic>? result;
  DateTime? syncedAt;

  QueuedOp({
    required this.id, required this.kind, required this.method, required this.path, required this.body,
    required this.idempotent, required this.occurredAt, this.attempts = 0, this.state = OpState.pending,
    this.lastError, this.correlationId, this.result, this.syncedAt,
  });

  Map<String, dynamic> toJson() => {
        'id': id, 'kind': kind, 'method': method, 'path': path, 'body': body, 'idempotent': idempotent,
        'occurred_at': occurredAt.toUtc().toIso8601String(), 'attempts': attempts, 'state': state.name,
        'last_error': lastError, 'correlation_id': correlationId, 'result': result, 'synced_at': syncedAt?.toIso8601String(),
      };

  factory QueuedOp.fromJson(Map<String, dynamic> j) => QueuedOp(
        id: j['id'] as String, kind: j['kind'] as String, method: j['method'] as String, path: j['path'] as String,
        body: (j['body'] as Map).cast<String, dynamic>(), idempotent: j['idempotent'] as bool,
        occurredAt: DateTime.parse(j['occurred_at'] as String), attempts: j['attempts'] as int,
        state: OpState.values.byName(j['state'] as String), lastError: j['last_error'] as String?,
        correlationId: j['correlation_id'] as String?,
        result: (j['result'] as Map?)?.cast<String, dynamic>(),
        syncedAt: j['synced_at'] == null ? null : DateTime.parse(j['synced_at'] as String),
      );
}

/// Decides whether a 409 conflict means the server already reflects the operation (e.g. the response of a
/// successful request was lost). Returning true marks the op done; false records a conflict for the user.
typedef ConflictResolver = Future<bool> Function(QueuedOp op);

class SyncReport {
  int sent = 0, rejected = 0, conflicts = 0;
  bool stoppedOffline = false;
  @override
  String toString() => 'sent=$sent rejected=$rejected conflicts=$conflicts offline=$stoppedOffline';
}

/// Durable, encrypted FIFO of outgoing operations with idempotent sync.
///
/// Policy (doc §9): operations are persisted before any network attempt; sync preserves order and stops at
/// the first transport failure; definitive 4xx rejections and conflicts are kept for the user to see —
/// the server stays authoritative for incident/assignment state.
class OfflineQueue {
  final EncryptedJsonStore store;
  final ApiClient api;
  final ConflictResolver? resolveConflict;
  final List<QueuedOp> _ops = [];
  final _changes = StreamController<List<QueuedOp>>.broadcast();
  Future<void>? _flushing;

  OfflineQueue(this.store, this.api, {this.resolveConflict});

  Stream<List<QueuedOp>> get changes => _changes.stream;
  List<QueuedOp> get all => List.unmodifiable(_ops);
  List<QueuedOp> get pending => _ops.where((o) => o.state == OpState.pending).toList();

  Future<void> load() async {
    final data = await store.read();
    _ops
      ..clear()
      ..addAll(((data as List?) ?? []).map((e) => QueuedOp.fromJson((e as Map).cast<String, dynamic>())));
    _emit();
  }

  Future<void> _persist() async {
    // Completed items are kept briefly for display, then trimmed.
    final done = _ops.where((o) => o.state == OpState.done).toList();
    if (done.length > 50) _ops.removeWhere((o) => done.take(done.length - 50).contains(o));
    await store.write(_ops.map((o) => o.toJson()).toList());
    _emit();
  }

  void _emit() => _changes.add(all);

  Future<QueuedOp> enqueue({required String kind, required String method, required String path,
      required Map<String, dynamic> body, bool idempotent = true, DateTime? occurredAt}) async {
    final op = QueuedOp(id: 'mob-${_uuid.v4()}', kind: kind, method: method, path: path, body: body,
        idempotent: idempotent, occurredAt: occurredAt ?? DateTime.now().toUtc());
    _ops.add(op);
    await _persist();
    return op;
  }

  /// Sends pending operations in order. Concurrent calls share one run.
  Future<SyncReport> flush() async {
    final report = SyncReport();
    while (_flushing != null) {
      await _flushing;
    }
    final c = Completer<void>();
    _flushing = c.future;
    try {
      for (final op in pending) {
        op.attempts++;
        try {
          final res = await api.request(op.method, op.path, body: op.body, idempotencyKey: op.idempotent ? op.id : null);
          op
            ..state = OpState.done
            ..result = res is Map<String, dynamic> ? res : null
            ..syncedAt = DateTime.now().toUtc()
            ..lastError = null;
          report.sent++;
        } on ApiException catch (e) {
          op
            ..lastError = describeApiError(e)
            ..correlationId = e.correlationId;
          if (e.isRetryable) {
            report.stoppedOffline = e.isNetwork;
            await _persist();
            break; // keep order; retry later
          }
          if (e.status == 409) {
            final already = resolveConflict != null && await resolveConflict!(op);
            op.state = already ? OpState.done : OpState.conflict;
            if (already) {
              op.syncedAt = DateTime.now().toUtc();
              report.sent++;
            } else {
              report.conflicts++;
            }
          } else {
            op.state = OpState.rejected;
            report.rejected++;
          }
        }
        await _persist();
      }
    } finally {
      _flushing = null;
      c.complete();
    }
    return report;
  }

  /// Removes a rejected/conflicted item after the user has seen it.
  Future<void> dismiss(String id) async {
    _ops.removeWhere((o) => o.id == id && o.state != OpState.pending);
    await _persist();
  }

  Future<void> dispose() => _changes.close();
}
