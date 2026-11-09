import 'dart:async';
import 'dart:io';

import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/foundation.dart';

import 'photo.dart';
import 'push_bridge.dart';

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

  /// Built-in sign-in (Shahkar on first sign-in / new device, SMS code every time); null with dev sign-in.
  final SessionAuth? auth;

  /// Shown once after signing in.
  AccessHistory? lastAccess;

  List<Assignment> _server = [];
  DateTime? fetchedAt;
  bool offline = false;
  bool signedIn = false;
  Timer? _timer;

  ResponderState({required this.api, required EncryptedJsonStore queueStore, required this.cache,
      required this.readToken, required this.writeToken, this.auth, Future<String?> Function(String)? readPref,
      Future<void> Function(String, String?)? writePref})
      : readPref = readPref ?? _noPref,
        writePref = writePref ?? _noWrite {
    queue = OfflineQueue(queueStore, api, resolveConflict: _alreadyApplied, prepare: _uploadPhotos);
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
    signedIn = auth != null ? await auth!.signedIn : await readToken() != null;
    notifyListeners();
    unawaited(_cleanupPhotos());
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

  /// Step 1 of the built-in sign-in. Responder accounts are created by the security administrator; an unknown
  /// national code is refused here instead of registering a citizen.
  Future<AuthChallenge> startSignIn(String nationalCode, String mobile) => auth!.start(nationalCode, mobile);

  Future<void> completeSignIn(AuthChallenge c, String code) async {
    lastAccess = await auth!.verify(c, code, deviceName: 'responder-app');
    signedIn = true;
    notifyListeners();
    await sync();
  }

  Future<bool> _reauthenticate() async {
    if (auth != null) return auth!.refresh();
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
    await _stopPush();
    await auth?.signOut();
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
      if (e.isUnauthenticated) signedIn = auth != null && await auth!.signedIn;
    }
    if (signedIn && !offline) await _startPush();
    notifyListeners();
  }

  bool _pushOn = false;

  /// Notifications while the app is closed (own push service); retried on each sync until it is running.
  Future<void> _startPush() async {
    if (_pushOn) return;
    try {
      final topic = await api.registerPush();
      _pushOn = await PushBridge.start(url: api.pushPollUrl, topic: topic, ongoing: 'آماده دریافت مأموریت‌ها');
    } on ApiException {
      // offline or an older server: tried again on the next sync
    }
  }

  Future<void> _stopPush() async {
    _pushOn = false;
    await PushBridge.stop();
    try {
      await api.unregisterPush();
    } on ApiException {
      // offline: the server drops the topic when the session ends anyway
    }
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

  /// Rapid building assessment from the field; offline-safe and created exactly once like casualties.
  Future<void> assessBuilding({required GeoLocation at, required String tag, required String buildingUse,
      List<String> observations = const [], bool peopleTrapped = false, int? floors, String address = '', String notes = '',
      String? incidentId, List<FieldPhoto> photos = const []}) async {
    await queue.enqueue(kind: 'damage.assess', method: 'POST', path: '/damage-assessments', body: {
      'location': {'lat': at.lat, 'lng': at.lng},
      'tag': tag,
      'building_use': buildingUse,
      'observations': observations,
      'people_trapped': peopleTrapped,
      if (floors != null) 'floors': floors,
      if (address.trim().isNotEmpty) 'address_text': address.trim(),
      if (notes.trim().isNotEmpty) 'notes': notes.trim(),
      if (incidentId != null) 'incident_id': incidentId,
      // Uploaded just before the assessment is sent (see _uploadPhotos); never sent to the server as such.
      if (photos.isNotEmpty) '_photos': [for (final p in photos) p.toJson()],
    });
    notifyListeners();
    await sync();
  }

  /// Uploads an assessment's photos first, then sends the assessment with their ids. Each uploaded id is saved
  /// at once, so after a lost connection only the remaining photos are sent; the server also recognises a
  /// repeated upload by its client reference. A photo the server refuses, or whose file is gone, is noted and
  /// skipped so the assessment itself is never lost.
  Future<Map<String, dynamic>> _uploadPhotos(QueuedOp op) async {
    final photos = (op.body['_photos'] as List?)?.cast<Map>();
    if (photos == null) return op.body;
    final ids = <String>[];
    for (final p in photos) {
      var id = p['media_id'] as String?;
      if (id == null && p['error'] == null) {
        final f = File(p['path'] as String);
        if (!await f.exists()) {
          p['error'] = 'فایل عکس روی گوشی پیدا نشد';
          await queue.save();
          continue;
        }
        try {
          final res = await api.upload('/media', await f.readAsBytes(), 'image/jpeg',
              query: FieldPhoto.uploadQuery(p, incidentId: op.body['incident_id'] as String?));
          id = res['media_id'] as String;
          p['media_id'] = id;
        } on ApiException catch (e) {
          if (e.isRetryable || e.isUnauthenticated) rethrow;
          p['error'] = describeApiError(e);
        }
        await queue.save();
      }
      if (id != null) ids.add(id);
    }
    final body = Map<String, dynamic>.of(op.body)..remove('_photos');
    if (ids.isNotEmpty) body['media_ids'] = ids;
    return body;
  }

  /// Photo files of an assessment op, with how many were sent.
  static (int sent, int total) photoProgress(QueuedOp op) {
    final photos = (op.body['_photos'] as List?) ?? const [];
    return (photos.where((p) => (p as Map)['media_id'] != null).length, photos.length);
  }

  Future<void> _cleanupPhotos() async {
    final waiting = <String>{}, sentAt = <String, DateTime>{};
    for (final op in queue.all) {
      for (final p in ((op.body['_photos'] as List?) ?? const []).cast<Map>()) {
        final path = p['path'] as String;
        if (op.state == OpState.done) {
          sentAt[path] = op.syncedAt ?? op.occurredAt;
        } else {
          waiting.add(path);
        }
      }
    }
    try {
      await cleanupPhotos(waiting: waiting, sentAt: sentAt);
    } on FileSystemException {
      // best effort; tried again on the next start
    }
  }

  List<QueuedOp> get assessments => queue.all.where((o) => o.kind == 'damage.assess').toList().reversed.toList();

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
