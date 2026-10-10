import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import 'platform.dart';
import 'state.dart';

class HomeScreen extends StatelessWidget {
  final CitizenState state;
  const HomeScreen({super.key, required this.state});

  @override
  Widget build(BuildContext context) => ListenableBuilder(
        listenable: state,
        builder: (context, _) {
          final pending = state.queue.pending.length;
          return Scaffold(
            appBar: AppBar(title: const Text('گزارش و هشدار بحران'), actions: [
              IconButton(tooltip: 'به‌روزرسانی', icon: const Icon(Icons.sync), onPressed: state.sync),
            ]),
            body: RefreshIndicator(
              onRefresh: state.sync,
              child: ListView(padding: const EdgeInsets.all(16), children: [
                const EmergencyNumbers(),
                if (state.needsAccessCode) AccessCodeCard(state: state),
                if (state.offline)
                  const Card(
                    color: Color(0xFFFFF4DC),
                    child: ListTile(
                      leading: Icon(Icons.wifi_off),
                      title: Text('اتصال برقرار نیست'),
                      subtitle: Text('گزارش‌ها روی دستگاه (رمزنگاری‌شده) نگه داشته و پس از وصل شدن ارسال می‌شوند.'),
                    ),
                  ),
                if (pending > 0)
                  Card(child: ListTile(leading: const Icon(Icons.schedule_send), title: Text('${faDigits(pending)} گزارش در صف ارسال'))),
                const SizedBox(height: 8),
                Text('هشدارهای رسمی محدوده شما', style: Theme.of(context).textTheme.titleMedium),
                if (state.alertsFetchedAt != null)
                  Text('آخرین به‌روزرسانی: ${agoFa(state.alertsFetchedAt!)}', style: Theme.of(context).textTheme.bodySmall),
                if (state.lastLocation == null)
                  TextButton.icon(
                    icon: const Icon(Icons.my_location),
                    label: const Text('دریافت هشدارهای محل من'),
                    onPressed: () async {
                      state.lastLocation = await currentLocation();
                      await state.refreshAlerts();
                    },
                  ),
                if (state.activeAlerts.isEmpty) const Padding(padding: EdgeInsets.all(8), child: Text('هشدار فعالی برای این محدوده ثبت نشده است.')),
                for (final a in state.activeAlerts) AlertCard(alert: a),
                const SizedBox(height: 16),
                FilledButton.icon(
                  style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(56)),
                  icon: const Icon(Icons.campaign),
                  label: const Text('ثبت گزارش'),
                  onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => ReportScreen(state: state))),
                ),
                const SizedBox(height: 8),
                OutlinedButton(
                  onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => MyReportsScreen(state: state))),
                  child: const Text('گزارش‌های من'),
                ),
                const SizedBox(height: 8),
                OutlinedButton.icon(
                  icon: const Icon(Icons.night_shelter),
                  label: const Text('محل‌های اسکان اضطراری نزدیک'),
                  onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => SheltersScreen(state: state))),
                ),
              ]),
            ),
          );
        },
      );
}

/// Shown when the test server asks for an access code (given by its administrator).
class AccessCodeCard extends StatefulWidget {
  final CitizenState state;
  const AccessCodeCard({super.key, required this.state});

  @override
  State<AccessCodeCard> createState() => _AccessCodeCardState();
}

class _AccessCodeCardState extends State<AccessCodeCard> {
  final _code = TextEditingController();
  bool _busy = false;

  @override
  void dispose() {
    _code.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (_code.text.trim().isEmpty) return;
    setState(() => _busy = true);
    await widget.state.submitAccessCode(_code.text);
    if (mounted) setState(() => _busy = false);
  }

  @override
  Widget build(BuildContext context) => Card(
        color: const Color(0xFFE8F0FE),
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            const Text('کد دسترسی', style: TextStyle(fontWeight: FontWeight.bold)),
            const Text('این سرور آزمایشی است. کدی را که مدیر سامانه داده وارد کنید.'),
            const SizedBox(height: 8),
            TextField(
              controller: _code,
              textDirection: TextDirection.ltr,
              autocorrect: false,
              enableSuggestions: false,
              decoration: InputDecoration(border: const OutlineInputBorder(), errorText: widget.state.accessCodeError),
              onSubmitted: (_) => _submit(),
            ),
            const SizedBox(height: 8),
            FilledButton(onPressed: _busy ? null : _submit, child: Text(_busy ? 'در حال بررسی…' : 'تأیید')),
          ]),
        ),
      );
}

class EmergencyNumbers extends StatelessWidget {
  const EmergencyNumbers({super.key});
  @override
  Widget build(BuildContext context) => const Card(
        child: Padding(
          padding: EdgeInsets.all(12),
          child: Text('در خطر جانی فوری ابتدا با اورژانس ۱۱۵، آتش‌نشانی ۱۲۵ یا پلیس ۱۱۰ تماس بگیرید. '
              'این برنامه جایگزین تماس اضطراری نیست.'),
        ),
      );
}

class AlertCard extends StatelessWidget {
  final PublicAlert alert;
  const AlertCard({super.key, required this.alert});

  @override
  Widget build(BuildContext context) {
    final severe = alert.severity == 'emergency' || alert.severity == 'warning';
    return Card(
      color: severe ? const Color(0xFFFDE8E8) : null,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(alert.text, style: Theme.of(context).textTheme.bodyLarge),
          const SizedBox(height: 6),
          Text('صادرکننده: ${alert.issuer} · محدوده: ${alert.regionLabel}', style: Theme.of(context).textTheme.bodySmall),
        ]),
      ),
    );
  }
}

class ReportScreen extends StatefulWidget {
  final CitizenState state;
  const ReportScreen({super.key, required this.state});
  @override
  State<ReportScreen> createState() => _ReportScreenState();
}

class _ReportScreenState extends State<ReportScreen> {
  final _form = GlobalKey<FormState>();
  final _desc = TextEditingController();
  String? _type;
  GeoLocation? _loc;
  bool _locating = false, _sending = false;
  String? _locError;

  @override
  void initState() {
    super.initState();
    _locate();
  }

  Future<void> _locate() async {
    setState(() {
      _locating = true;
      _locError = null;
    });
    try {
      _loc = await currentLocation();
      if (_loc == null) _locError = 'دسترسی به موقعیت داده نشد یا GPS خاموش است.';
    } catch (_) {
      _locError = 'دریافت موقعیت ناموفق بود؛ دوباره تلاش کنید.';
    }
    if (mounted) setState(() => _locating = false);
  }

  Future<void> _submit() async {
    if (!_form.currentState!.validate() || _loc == null) return;
    setState(() => _sending = true);
    widget.state.lastLocation = _loc;
    final op = await widget.state.submit(ReportSubmission(type: _type!, description: _desc.text.trim(), location: _loc!));
    if (!mounted) return;
    final msg = switch (op.state) {
      OpState.done => 'گزارش دریافت شد. این به معنی تأیید یا اعزام نیست؛ کارشناسان آن را بررسی می‌کنند.',
      OpState.rejected => 'گزارش پذیرفته نشد. ${op.lastError}',
      _ => 'گزارش روی دستگاه ذخیره شد و پس از برقراری ارتباط ارسال می‌شود.',
    };
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(msg), duration: const Duration(seconds: 6)));
    if (op.state != OpState.rejected) Navigator.pop(context);
    setState(() => _sending = false);
  }

  @override
  Widget build(BuildContext context) => Scaffold(
        appBar: AppBar(title: const Text('ثبت گزارش')),
        body: Form(
          key: _form,
          child: ListView(padding: const EdgeInsets.all(16), children: [
            DropdownButtonFormField<String>(
              value: _type,
              decoration: const InputDecoration(labelText: 'نوع رخداد', border: OutlineInputBorder()),
              items: [for (final e in reportTypes.entries) DropdownMenuItem(value: e.key, child: Text(e.value))],
              onChanged: (v) => setState(() => _type = v),
              validator: (v) => v == null ? 'نوع رخداد را انتخاب کنید' : null,
            ),
            const SizedBox(height: 12),
            TextFormField(
              controller: _desc,
              maxLines: 4,
              maxLength: 2000,
              decoration: const InputDecoration(labelText: 'شرح کوتاه (چه دیدید؟ چند نفر؟)', border: OutlineInputBorder()),
            ),
            const SizedBox(height: 12),
            Card(
              child: ListTile(
                leading: _locating ? const CircularProgressIndicator() : Icon(_loc == null ? Icons.location_off : Icons.location_on),
                title: Text(_loc == null ? (_locError ?? 'در حال دریافت موقعیت…') : 'موقعیت دریافت شد'),
                subtitle: _loc == null ? null : Text('دقت تقریبی ${faDigits(_loc!.accuracyM.round())} متر'),
                trailing: IconButton(icon: const Icon(Icons.refresh), onPressed: _locating ? null : _locate),
              ),
            ),
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 8),
              child: Text('موقعیت دقیق شما فقط برای نقش‌های مجاز امدادی قابل مشاهده است.', style: TextStyle(fontSize: 12)),
            ),
            FilledButton(
              style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52)),
              onPressed: _sending || _loc == null ? null : _submit,
              child: Text(_sending ? 'در حال ثبت…' : 'ثبت گزارش'),
            ),
          ]),
        ),
      );
}

const reportStatusLabels = {
  'received': 'دریافت‌شده', 'triage': 'در صف بررسی', 'under_review': 'در حال بررسی', 'accepted': 'تأییدشده',
  'rejected': 'ردشده', 'duplicate': 'تکراری (قبلاً گزارش شده)', 'linked_to_incident': 'در حال پیگیری',
};

class MyReportsScreen extends StatelessWidget {
  final CitizenState state;
  const MyReportsScreen({super.key, required this.state});

  @override
  Widget build(BuildContext context) => ListenableBuilder(
        listenable: state,
        builder: (context, _) {
          final local = state.queue.all.where((o) => o.state != OpState.done).toList();
          return Scaffold(
            appBar: AppBar(title: const Text('گزارش‌های من')),
            body: ListView(children: [
              for (final o in local)
                ListTile(
                  leading: Icon(o.state == OpState.pending ? Icons.schedule : Icons.error_outline),
                  title: Text(reportTypes[o.body['type']] ?? '${o.body['type']}'),
                  subtitle: Text(o.state == OpState.pending ? 'در انتظار ارسال · ${agoFa(o.occurredAt)}' : 'ارسال ناموفق: ${o.lastError}'),
                  trailing: o.state == OpState.pending ? null : IconButton(icon: const Icon(Icons.close), onPressed: () => state.queue.dismiss(o.id)),
                ),
              for (final r in state.myReports)
                ListTile(
                  leading: const Icon(Icons.check_circle_outline),
                  title: Text(reportTypes[r.type] ?? r.type),
                  subtitle: Text('${reportStatusLabels[r.status] ?? r.status} · ${agoFa(r.receivedAt)}'),
                ),
              if (local.isEmpty && state.myReports.isEmpty) const Padding(padding: EdgeInsets.all(24), child: Text('گزارشی ثبت نکرده‌اید.')),
            ]),
          );
        },
      );
}

/// Nearest shelters that can take people now. Uses the current position once; shows the cached list offline.
class SheltersScreen extends StatefulWidget {
  final CitizenState state;
  const SheltersScreen({super.key, required this.state});

  @override
  State<SheltersScreen> createState() => _SheltersScreenState();
}

class _SheltersScreenState extends State<SheltersScreen> {
  bool _locating = false;
  String? _locationError;

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  Future<void> _refresh() async {
    setState(() {
      _locating = true;
      _locationError = null;
    });
    final loc = await currentLocation();
    if (!mounted) return;
    if (loc == null) {
      setState(() {
        _locating = false;
        _locationError = 'موقعیت شما در دسترس نیست. مکان‌یابی گوشی را روشن کنید و اجازه دسترسی بدهید.';
      });
      return;
    }
    widget.state.lastLocation = loc;
    await widget.state.refreshShelters(loc);
    if (mounted) setState(() => _locating = false);
  }

  /// Opens the phone's map app (Google Maps, Neshan, Balad, …) at the shelter via a geo: link; falls back to
  /// OpenStreetMap in the browser, then to copying the coordinates.
  Future<void> _navigate(BuildContext context, PublicShelter sh) async {
    final lat = sh.lat.toStringAsFixed(6), lng = sh.lng.toStringAsFixed(6);
    final geo = Uri.parse('geo:$lat,$lng?q=$lat,$lng(${Uri.encodeComponent(sh.name)})');
    final web = Uri.parse('https://www.openstreetmap.org/?mlat=$lat&mlon=$lng#map=17/$lat/$lng');
    for (final uri in [geo, web]) {
      try {
        if (await launchUrl(uri, mode: LaunchMode.externalApplication)) return;
      } catch (_) {
        // no app for this link; try the next one
      }
    }
    if (context.mounted) await _copy(context, sh, 'برنامه نقشه پیدا نشد؛ مختصات کپی شد.');
  }

  Future<void> _copy(BuildContext context, PublicShelter sh, String message) async {
    await Clipboard.setData(ClipboardData(text: '${sh.lat.toStringAsFixed(6)},${sh.lng.toStringAsFixed(6)}'));
    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(message)));
  }

  String _distance(double m) => m < 1000 ? '${faDigits(m.round())} متر' : '${faDigits((m / 1000).toStringAsFixed(1))} کیلومتر';

  @override
  Widget build(BuildContext context) => ListenableBuilder(
        listenable: widget.state,
        builder: (context, _) {
          final s = widget.state;
          return Scaffold(
            appBar: AppBar(title: const Text('محل‌های اسکان نزدیک'), actions: [
              IconButton(tooltip: 'به‌روزرسانی', icon: const Icon(Icons.my_location), onPressed: _locating ? null : _refresh),
            ]),
            body: RefreshIndicator(
              onRefresh: _refresh,
              child: ListView(padding: const EdgeInsets.all(16), children: [
                if (_locating) const LinearProgressIndicator(),
                if (_locationError != null) Card(color: const Color(0xFFFFF4DC), child: ListTile(title: Text(_locationError!))),
                if (s.shelterError != null) Card(color: const Color(0xFFFFF4DC), child: ListTile(title: Text(s.shelterError!))),
                if (s.sheltersFetchedAt != null)
                  Text('آخرین به‌روزرسانی: ${agoFa(s.sheltersFetchedAt!)}', style: Theme.of(context).textTheme.bodySmall),
                const Padding(
                  padding: EdgeInsets.symmetric(vertical: 8),
                  child: Text('فقط محل‌هایی که الان پذیرش دارند نمایش داده می‌شوند. جای خالی لحظه‌ای است و تضمین نمی‌شود؛ '
                      'در صورت امکان پیش از حرکت با ۱۱۲ هماهنگ کنید.'),
                ),
                if (!_locating && s.sheltersFetchedAt != null && s.shelters.isEmpty)
                  const Card(child: ListTile(title: Text('در شعاع ۵۰ کیلومتری محل اسکانِ دارای جای خالی ثبت نشده است.'),
                      subtitle: Text('با ۱۱۲ تماس بگیرید.'))),
                for (final sh in s.shelters)
                  Card(
                    child: Padding(
                      padding: const EdgeInsets.fromLTRB(8, 4, 8, 8),
                      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                        ListTile(
                          contentPadding: EdgeInsets.zero,
                          leading: const Icon(Icons.night_shelter, color: Color(0xFF15803D)),
                          title: Text(sh.name),
                          subtitle: Text('${sh.organization}\nفاصله: ${_distance(sh.distanceM)} · جای خالی: ${faDigits(sh.available)} نفر'
                              '${sh.updatedAt == null ? '' : '\nآمار: ${agoFa(sh.updatedAt!)}'}'),
                          isThreeLine: true,
                        ),
                        Row(children: [
                          Expanded(
                            child: FilledButton.icon(
                              icon: const Icon(Icons.directions),
                              label: const Text('مسیریابی'),
                              onPressed: () => _navigate(context, sh),
                            ),
                          ),
                          IconButton(
                            tooltip: 'کپی مختصات',
                            icon: const Icon(Icons.copy),
                            onPressed: () => _copy(context, sh, 'مختصات کپی شد؛ در برنامه نقشه جست‌وجو کنید.'),
                          ),
                        ]),
                      ]),
                    ),
                  ),
              ]),
            ),
          );
        },
      );
}
