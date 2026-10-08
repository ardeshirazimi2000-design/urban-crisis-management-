import 'dart:async';

import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/foundation.dart';

/// Citizen app state: identity, encrypted offline report queue, cached official alerts.
class CitizenState extends ChangeNotifier {
  final ApiClient api;
  final OfflineQueue queue;
  final EncryptedJsonStore alertCache;
  final Future<String?> Function() readToken;
  final Future<void> Function(String?) writeToken;
  final bool devAuth;

  List<PublicAlert> alerts = [];
  DateTime? alertsFetchedAt;
  List<MyReport> myReports = [];
  DateTime? lastSyncAt;
  bool offline = false;
  String? lastError;
  GeoLocation? lastLocation;
  Timer? _timer;
  StreamSubscription<List<QueuedOp>>? _sub;

  CitizenState({required this.api, required this.queue, required this.alertCache, required this.readToken,
      required this.writeToken, this.devAuth = true});

  Future<void> start() async {
    await queue.load();
    _sub = queue.changes.listen((_) => notifyListeners());
    final cached = await alertCache.read() as Map<String, dynamic>?;
    if (cached != null) {
      alerts = (cached['items'] as List).map((e) => PublicAlert.fromJson((e as Map).cast())).toList();
      alertsFetchedAt = DateTime.tryParse(cached['fetched_at'] as String? ?? '');
    }
    await ensureIdentity();
    await sync();
    _timer = Timer.periodic(const Duration(seconds: 30), (_) => sync());
  }

  /// Development identity only. Production uses the national/municipal OIDC provider.
  Future<void> ensureIdentity() async {
    if (await readToken() != null || !devAuth) return;
    try {
      final id = DateTime.now().microsecondsSinceEpoch.toRadixString(36);
      await writeToken(await api.devToken('citizen-$id', 'شهروند', const []));
    } on ApiException catch (e) {
      offline = e.isNetwork;
    }
  }

  Future<void> sync() async {
    final r = await queue.flush();
    offline = r.stoppedOffline;
    if (!offline) lastSyncAt = DateTime.now();
    await refreshAlerts();
    try {
      myReports = await api.myReports();
    } on ApiException catch (e) {
      offline = offline || e.isNetwork;
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
