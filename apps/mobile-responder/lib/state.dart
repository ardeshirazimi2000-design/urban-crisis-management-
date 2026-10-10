import 'dart:async';

import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/foundation.dart';

const _lifecycle = ['proposed', 'assigned', 'acknowledged', 'en_route', 'on_scene', 'completed'];

/// Responder state. Missions are cached encrypted on the device; status changes and positions are queued
/// and synced idempotently. The server is authoritative: conflicting changes are surfaced, never forced.
class ResponderState extends ChangeNotifier {
  final ApiClient api;
  final EncryptedJsonStore cache;
  final Future<String?> Function() readToken;
  final Future<void> Function(String?) writeToken;

  /// Small private values in the OS keystore: the test sign-in (id, name, access code), so an expired
  /// session renews itself instead of stopping mission updates in the field.
  final Future<String?> Function(String key) readPref;
  final Future<void> Function(String key, String? value) writePref;
  late final OfflineQueue queue;

  List<Assignment> _server = [];
  DateTime? fetchedAt;
  bool offline = false;
  bool signedIn = false;
  Timer? _timer;

  ResponderState({required this.api, required EncryptedJsonStore queueStore, required this.cache,
      required this.readToken, required this.writeToken, Future<String?> Function(String)? readPref,
      Future<void> Function(String, String?)? writePref})
      : readPref = readPref ?? _noPref,
        writePref = writePref ?? _noWrite {
    queue = OfflineQueue(queueStore, api, resolveConflict: _alreadyApplied);
  }

  static Future<String?> _noPref(String _) async => null;
  static Future<void> _noWrite(String _, String? __) async {}

  Future<void> start() async {
    await queue.load();
    queue.changes.listen((_) => notifyListeners());
    final c = await cache.read() as Map<String, dynamic>?;
    if (c != null) {
      _server = (c['items'] as List).map((e) => Assignment.fromJson((e as Map).cast())).toList();
      fetchedAt = DateTime.tryParse(c['fetched_at'] as String? ?? '');
    }
    signedIn = await readToken() != null;
    notifyListeners();
    if (signedIn) await sync();
    _timer = Timer.periodic(const Duration(seconds: 20), (_) => signedIn ? sync() : null);
  }

  /// Local development sign-in. Production uses OIDC with MFA for responders.
  Future<void> devSignIn(String subject, String name, {String accessCode = ''}) async {
    await writeToken(await api.devToken(subject, name, const [{'role': 'RESPONDER'}], accessCode: accessCode));
    await writePref('subject', subject);
    await writePref('name', name);
    await writePref('access_code', accessCode);
    signedIn = true;
    await sync();
  }

  /// Saved sign-in for the form (pre-filled after sign-out).
  Future<(String?, String?, String?)> savedSignIn() async =>
      (await readPref('subject'), await readPref('name'), await readPref('access_code'));

  Future<bool> _reauthenticate() async {
    final subject = await readPref('subject');
    if (subject == null) return false;
    try {
      await writeToken(await api.devToken(subject, await readPref('name') ?? subject, const [{'role': 'RESPONDER'}],
          accessCode: await readPref('access_code') ?? ''));
      return true;
    } on ApiException {
      return false;
    }
  }

  Future<void> signOut() async {
    await writeToken(null);
    await writePref('access_code', null);
    signedIn = false;
    notifyListeners();
  }

  Future<void> sync() async {
    var r = await queue.flush();
    if (r.needsAuth && await _reauthenticate()) r = await queue.flush();
    offline = r.stoppedOffline;
    try {
      _server = await _fetchAssignments();
      fetchedAt = DateTime.now();
      await cache.write({'items': _server.map((a) => a.toJson()).toList(), 'fetched_at': fetchedAt!.toIso8601String()});
      offline = false;
    } on ApiException catch (e) {
      offline = e.isNetwork;
      if (e.isUnauthenticated) signedIn = false;
    }
    notifyListeners();
  }

  Future<List<Assignment>> _fetchAssignments() async {
    try {
      return await api.myAssignments();
    } on ApiException catch (e) {
      if (e.isUnauthenticated && await _reauthenticate()) return api.myAssignments();
      rethrow;
    }
  }

  /// Missions as the responder sees them: server state with not-yet-synced local status changes applied.
  List<Assignment> get assignments => _server.map(_project).toList();

  Assignment _project(Assignment a) {
    var cur = a;
    for (final op in queue.pending.where((o) => o.kind == 'assignment.status' && o.path.contains(a.id))) {
      cur = Assignment(id: cur.id, incidentId: cur.incidentId, incidentCode: cur.incidentCode, resourceId: cur.resourceId,
          resourceName: cur.resourceName, status: op.body['status'] as String, reason: cur.reason,
          assignedAt: cur.assignedAt, version: cur.version + 1);
    }
    return cur;
  }

  bool hasPendingFor(String assignmentId) => queue.pending.any((o) => o.path.contains(assignmentId));

  Future<void> setStatus(Assignment projected, String status, {String reason = ''}) async {
    await queue.enqueue(kind: 'assignment.status', method: 'POST', path: '/assignments/${projected.id}/status',
        body: {'status': status, 'version': projected.version, 'reason': reason}, idempotent: false);
    notifyListeners();
    await sync();
  }

  /// Field triage: saved on the device first and sent with an idempotency key, so a casualty recorded
  /// offline or retried after a lost response is created exactly once.
  Future<void> recordCasualty(Assignment a, {required String triage, String ageGroup = 'unknown', String sex = 'unknown',
      String tagNo = '', String notes = '', GeoLocation? at}) async {
    await queue.enqueue(kind: 'casualty.record', method: 'POST', path: '/incidents/${a.incidentId}/casualties', body: {
      'triage': triage,
      'age_group': ageGroup,
      'sex': sex,
      if (tagNo.trim().isNotEmpty) 'tag_no': tagNo.trim(),
      if (notes.trim().isNotEmpty) 'notes': notes.trim(),
      if (at != null) 'location': {'lat': at.lat, 'lng': at.lng},
    });
    notifyListeners();
    await sync();
  }

  /// Casualties this device recorded for an incident (sent or still queued), newest first.
  List<QueuedOp> casualtiesFor(String incidentId) => queue.all
      .where((o) => o.kind == 'casualty.record' && o.path == '/incidents/$incidentId/casualties')
      .toList()
      .reversed
      .toList();

  Future<void> reportPosition(String resourceId, GeoLocation loc) async {
    await queue.enqueue(kind: 'resource.location', method: 'POST', path: '/resources/$resourceId/location',
        body: {'lat': loc.lat, 'lng': loc.lng, 'observed_at': DateTime.now().toUtc().toIso8601String()}, idempotent: false);
    await sync();
  }

  /// A 409 on replay is benign when the server already reached (or passed) the requested status,
  /// e.g. the first attempt succeeded but its response was lost.
  Future<bool> _alreadyApplied(QueuedOp op) async {
    if (op.kind != 'assignment.status') return false;
    try {
      final server = await api.myAssignments();
      final id = op.path.split('/')[2];
      final a = server.where((x) => x.id == id).firstOrNull;
      if (a == null) return false;
      final want = op.body['status'] as String;
      if (a.status == want) return true;
      final wi = _lifecycle.indexOf(want), si = _lifecycle.indexOf(a.status);
      return wi >= 0 && si >= wi;
    } on ApiException {
      return false;
    }
  }

  List<QueuedOp> get problems => queue.all.where((o) => o.state == OpState.conflict || o.state == OpState.rejected).toList();

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }
}
