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
                const SizedBox(height: 8),
                FilledButton.icon(
                  style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52), backgroundColor: const Color(0xFF0F766E)),
                  icon: const Icon(Icons.support_agent),
                  label: const Text('دستیار بحران: چه کار کنم؟'),
                  onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => AssistantScreen(state: state))),
                ),
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

/// Crisis assistant: answers only with approved guidance cards, matched on the phone (offline, private).
class AssistantScreen extends StatefulWidget {
  final CitizenState state;
  const AssistantScreen({super.key, required this.state});

  @override
  State<AssistantScreen> createState() => _AssistantScreenState();
}

class _Turn {
  final String question;
  final AssistantAnswer answer;
  _Turn(this.question, this.answer);
}

class _AssistantScreenState extends State<AssistantScreen> {
  final _q = TextEditingController();
  final _scroll = ScrollController();
  final _turns = <_Turn>[];

  @override
  void initState() {
    super.initState();
    widget.state.loadGuidance().then((_) => widget.state.refreshGuidance());
  }

  @override
  void dispose() {
    _q.dispose();
    _scroll.dispose();
    super.dispose();
  }

  void _ask(String question) {
    final kb = widget.state.guidance;
    if (kb == null || question.trim().isEmpty) return;
    setState(() => _turns.add(_Turn(question.trim(), Assistant(kb).answer(question))));
    _q.clear();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_scroll.hasClients) _scroll.animateTo(_scroll.position.maxScrollExtent, duration: const Duration(milliseconds: 250), curve: Curves.easeOut);
    });
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
        listenable: widget.state,
        builder: (context, _) {
          final kb = widget.state.guidance;
          return Scaffold(
            appBar: AppBar(title: const Text('دستیار بحران')),
            body: Column(children: [
              Container(
                width: double.infinity,
                color: const Color(0xFFFFF4DC),
                padding: const EdgeInsets.all(8),
                child: const Text('فقط راهنمای تأییدشده نمایش داده می‌شود و جایگزین تماس با ۱۱۲ و ۱۱۵ نیست. '
                    'سؤال شما از گوشی خارج نمی‌شود و بدون اینترنت هم کار می‌کند.', style: TextStyle(fontSize: 12)),
              ),
              Expanded(
                child: kb == null
                    ? const Center(child: CircularProgressIndicator())
                    : ListView(controller: _scroll, padding: const EdgeInsets.all(12), children: [
                        if (_turns.isEmpty) ...[
                          const Text('سؤال خود را بنویسید یا یکی از موارد زیر را انتخاب کنید:'),
                          const SizedBox(height: 8),
                          Wrap(spacing: 6, runSpacing: 6, children: [
                            for (final c in Assistant(kb).suggestions)
                              ActionChip(
                                avatar: c.emergency ? const Icon(Icons.warning_amber, color: Color(0xFFDC2626), size: 18) : null,
                                label: Text(c.title),
                                onPressed: () => _ask(c.title),
                              ),
                          ]),
                        ],
                        for (final t in _turns) ...[
                          Align(
                            alignment: AlignmentDirectional.centerEnd,
                            child: Container(
                              margin: const EdgeInsets.symmetric(vertical: 6),
                              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                              decoration: BoxDecoration(color: const Color(0xFFE0E7FF), borderRadius: BorderRadius.circular(12)),
                              child: Text(t.question),
                            ),
                          ),
                          if (!t.answer.matched) _NoAnswer(onSuggest: _ask, suggestions: Assistant(kb).suggestions),
                          for (final (i, c) in t.answer.cards.indexed) GuidanceCardView(card: c, state: widget.state, primary: i == 0, kbVersion: kb.version),
                        ],
                      ]),
              ),
              SafeArea(
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(8, 4, 8, 8),
                  child: Row(children: [
                    Expanded(
                      child: TextField(
                        controller: _q,
                        textInputAction: TextInputAction.send,
                        onSubmitted: _ask,
                        decoration: const InputDecoration(hintText: 'مثلاً: بوی گاز می‌آید چه کنم؟', border: OutlineInputBorder(), isDense: true),
                      ),
                    ),
                    IconButton.filled(icon: const Icon(Icons.send), tooltip: 'پرسیدن', onPressed: () => _ask(_q.text)),
                  ]),
                ),
              ),
            ]),
          );
        },
      );
}

class _NoAnswer extends StatelessWidget {
  final void Function(String) onSuggest;
  final List<GuidanceCard> suggestions;
  const _NoAnswer({required this.onSuggest, required this.suggestions});

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            const Text('برای این سؤال راهنمای تأییدشده‌ای پیدا نشد. اگر در خطر هستید همین حالا تماس بگیرید.'),
            const SizedBox(height: 8),
            Wrap(spacing: 8, children: [for (final n in ['112', '115', '125']) CallButton(number: n)]),
            const SizedBox(height: 8),
            const Text('یا یکی از این موارد:'),
            Wrap(spacing: 6, runSpacing: 6, children: [
              for (final c in suggestions.take(6)) ActionChip(label: Text(c.title), onPressed: () => onSuggest(c.title)),
            ]),
          ]),
        ),
      );
}

/// Opens the dialer with the number filled in; the citizen presses call (nothing is dialled automatically).
class CallButton extends StatelessWidget {
  final String number;
  const CallButton({super.key, required this.number});

  @override
  Widget build(BuildContext context) => FilledButton.icon(
        style: FilledButton.styleFrom(backgroundColor: const Color(0xFFDC2626)),
        icon: const Icon(Icons.call),
        label: Text('تماس با ${faDigits(number)}'),
        onPressed: () async {
          final ok = await launchUrl(Uri.parse('tel:$number'));
          if (!ok && context.mounted) {
            ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('شماره ${faDigits(number)} را با تلفن بگیرید.')));
          }
        },
      );
}

class GuidanceCardView extends StatelessWidget {
  final GuidanceCard card;
  final CitizenState state;
  final bool primary;
  final int kbVersion;
  const GuidanceCardView({super.key, required this.card, required this.state, required this.primary, required this.kbVersion});

  @override
  Widget build(BuildContext context) {
    final emergency = card.emergency && primary;
    return Card(
      shape: RoundedRectangleBorder(
          side: BorderSide(color: emergency ? const Color(0xFFDC2626) : Colors.transparent, width: 2),
          borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Row(children: [
            if (emergency) const Padding(padding: EdgeInsetsDirectional.only(end: 6), child: Icon(Icons.warning_amber, color: Color(0xFFDC2626))),
            Expanded(child: Text(card.title, style: Theme.of(context).textTheme.titleMedium)),
          ]),
          const SizedBox(height: 6),
          if (primary) Text(card.body) else Text(card.body.split('\n').first, maxLines: 2, overflow: TextOverflow.ellipsis),
          const SizedBox(height: 8),
          Wrap(spacing: 8, runSpacing: 8, children: [
            for (final a in card.actions)
              if (a.startsWith('call:'))
                CallButton(number: a.substring(5))
              else if (a == 'report')
                OutlinedButton.icon(
                  icon: const Icon(Icons.campaign),
                  label: const Text('ثبت گزارش'),
                  onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => ReportScreen(state: state))),
                )
              else if (a == 'shelters')
                OutlinedButton.icon(
                  icon: const Icon(Icons.night_shelter),
                  label: const Text('محل‌های اسکان نزدیک'),
                  onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => SheltersScreen(state: state))),
                )
              else if (a == 'alerts')
                OutlinedButton.icon(
                  icon: const Icon(Icons.notifications_active),
                  label: const Text('هشدارهای رسمی'),
                  onPressed: () => Navigator.popUntil(context, (r) => r.isFirst),
                ),
          ]),
          const SizedBox(height: 4),
          Text('راهنمای تأییدشده · نسخه ${faDigits(card.version)}', style: Theme.of(context).textTheme.bodySmall),
        ]),
      ),
    );
  }
}
