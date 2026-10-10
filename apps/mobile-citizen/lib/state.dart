import 'dart:async';

import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/foundation.dart';

/// Citizen access code of a test server reachable from outside the office (--dart-define=ACCESS_CODE=...).
const devAccessCode = String.fromEnvironment('ACCESS_CODE');

/// Citizen app state: identity, encrypted offline report queue, cached official alerts.
class CitizenState extends ChangeNotifier {
  final ApiClient api;
  final OfflineQueue queue;
  final EncryptedJsonStore alertCache;

  /// Last nearby-shelter list, kept encrypted so it can still be shown (with its age) when offline.
  final EncryptedJsonStore? shelterCache;
  final Future<String?> Function() readToken;
  final Future<void> Function(String?) writeToken;
  final bool devAuth;

  /// Small private values kept in the OS keystore (stable test identity, access code).
  final Future<String?> Function(String key) readPref;
  final Future<void> Function(String key, String? value) writePref;

  /// The test server requires an access code that this device does not have (or has a wrong one).
  bool needsAccessCode = false;
  String? accessCodeError;

  List<PublicAlert> alerts = [];
  DateTime? alertsFetchedAt;
  List<MyReport> myReports = [];
  DateTime? lastSyncAt;
  bool offline = false;
  String? lastError;
  GeoLocation? lastLocation;
  List<PublicShelter> shelters = [];
  DateTime? sheltersFetchedAt;
  String? shelterError;
  Timer? _timer;
  StreamSubscription<List<QueuedOp>>? _sub;

  CitizenState({required this.api, required this.queue, required this.alertCache, this.shelterCache, required this.readToken,
      required this.writeToken, this.devAuth = true, Future<String?> Function(String)? readPref,
      Future<void> Function(String, String?)? writePref})
      : readPref = readPref ?? _noPref,
        writePref = writePref ?? _noWrite;

  static Future<String?> _noPref(String _) async => null;
  static Future<void> _noWrite(String _, String? __) async {}

  Future<void> start() async {
    await queue.load();
    _sub = queue.changes.listen((_) => notifyListeners());
    final cached = await alertCache.read() as Map<String, dynamic>?;
    if (cached != null) {
      alerts = (cached['items'] as List).map((e) => PublicAlert.fromJson((e as Map).cast())).toList();
      alertsFetchedAt = DateTime.tryParse(cached['fetched_at'] as String? ?? '');
    }
    final sc = await shelterCache?.read() as Map<String, dynamic>?;
    if (sc != null) {
      shelters = (sc['items'] as List).map((e) => PublicShelter.fromJson((e as Map).cast())).toList();
      sheltersFetchedAt = DateTime.tryParse(sc['fetched_at'] as String? ?? '');
    }
    await ensureIdentity();
    await sync();
    _timer = Timer.periodic(const Duration(seconds: 30), (_) => sync());
  }

  /// Development identity only. Production uses the national/municipal OIDC provider.
  /// The identity is stable per installation, so "my reports" survive an expired session.
  Future<void> ensureIdentity({bool force = false}) async {
    if (!devAuth || (!force && await readToken() != null)) return;
    var subject = await readPref('citizen_subject');
    if (subject == null) {
      subject = 'citizen-${DateTime.now().microsecondsSinceEpoch.toRadixString(36)}';
      await writePref('citizen_subject', subject);
    }
    final code = await readPref('access_code') ?? devAccessCode;
    try {
      await writeToken(await api.devToken(subject, 'شهروند', const [], accessCode: code));
      needsAccessCode = false;
      accessCodeError = null;
    } on ApiException catch (e) {
      offline = e.isNetwork;
      if (e.code == 'ACCESS_CODE_INVALID') {
        needsAccessCode = true;
        accessCodeError = code.isEmpty ? null : 'کد وارد‌شده درست نیست.';
      } else if (e.status == 429) {
        accessCodeError = 'تلاش زیاد بود؛ یک دقیقه بعد دوباره امتحان کنید.';
      }
    }
    notifyListeners();
  }

  /// Saves the access code given by the test-server administrator and signs in with it.
  Future<bool> submitAccessCode(String code) async {
    await writePref('access_code', code.trim());
    await ensureIdentity(force: true);
    if (!needsAccessCode && accessCodeError == null) await sync();
    return !needsAccessCode;
  }

  Future<bool> _reauthenticate() async {
    await writeToken(null);
    await ensureIdentity(force: true);
    return await readToken() != null;
  }

  Future<void> sync() async {
    if (devAuth && !needsAccessCode && await readToken() == null) await ensureIdentity(); // first start was offline
    var r = await queue.flush();
    if (r.needsAuth && await _reauthenticate()) r = await queue.flush();
    offline = r.stoppedOffline;
    if (!offline) lastSyncAt = DateTime.now();
    await refreshAlerts();
    try {
      myReports = await api.myReports();
    } on ApiException catch (e) {
      offline = offline || e.isNetwork;
      if (e.isUnauthenticated && await _reauthenticate()) {
        try {
          myReports = await api.myReports();
        } on ApiException catch (_) {}
      }
    }
    notifyListeners();
  }

  Future<void> refreshAlerts([GeoLocation? at]) async {
    final loc = at ?? lastLocation;
    if (loc == null) return;
    try {
      alerts = await api.activeAlerts(loc.lat, loc.lng);
      alertsFetchedAt = DateTime.now();
      await alertCache.write({'items': alerts.map((a) => a.toJson()).toList(), 'fetched_at': alertsFetchedAt!.toIso8601String()});
      lastError = null;
    } on ApiException catch (e) {
      offline = e.isNetwork;
      lastError = e.message;
    }
    notifyListeners();
  }

  /// Nearest shelters accepting people. On failure the previous list stays visible with its fetch time.
  Future<void> refreshShelters(GeoLocation at) async {
    try {
      shelters = await api.nearbyShelters(at.lat, at.lng);
      sheltersFetchedAt = DateTime.now();
      shelterError = null;
      await shelterCache?.write({'items': shelters.map((s) => s.toJson()).toList(), 'fetched_at': sheltersFetchedAt!.toIso8601String()});
    } on ApiException catch (e) {
      offline = e.isNetwork;
      shelterError = e.isNetwork ? 'اتصال برقرار نیست؛ آخرین فهرست ذخیره‌شده نمایش داده می‌شود.' : describeApiError(e);
    }
    notifyListeners();
  }

  /// Saves the report durably on the device first, then tries to send it.
  Future<QueuedOp> submit(ReportSubmission r) async {
    final op = await queue.enqueue(kind: 'report.create', method: 'POST', path: '/reports', body: r.toJson(),
        occurredAt: r.occurredAt);
    await sync();
    return queue.all.firstWhere((o) => o.id == op.id, orElse: () => op);
  }

  /// Alerts past their expiry are hidden even when shown from the offline cache.
  List<PublicAlert> get activeAlerts => alerts.where((a) => a.expiresAt.isAfter(DateTime.now())).toList();

  @override
  void dispose() {
    _timer?.cancel();
    _sub?.cancel();
    super.dispose();
  }
}
