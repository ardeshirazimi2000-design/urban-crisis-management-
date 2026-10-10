import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';

import 'platform.dart';
import 'state.dart';

class SignInScreen extends StatefulWidget {
  final ResponderState state;
  const SignInScreen({super.key, required this.state});
  @override
  State<SignInScreen> createState() => _SignInScreenState();
}

class _SignInScreenState extends State<SignInScreen> {
  final _id = TextEditingController(text: 'responder1');
  final _name = TextEditingController(text: 'امدادگر ۱');
  final _code = TextEditingController();
  String? _error;
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    widget.state.savedSignIn().then((s) {
      if (!mounted) return;
      if (s.$1 != null) _id.text = s.$1!.replaceFirst('dev:', '');
      if (s.$2 != null) _name.text = s.$2!;
    });
  }

  @override
  void dispose() {
    _id.dispose();
    _name.dispose();
    _code.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Scaffold(
        appBar: AppBar(title: const Text('ورود امدادگر')),
        body: ListView(padding: const EdgeInsets.all(16), children: [
          const Text('ورود توسعه محلی. در محیط عملیاتی ورود از طریق OIDC و احراز هویت چندعاملی است.'),
          TextField(controller: _id, decoration: const InputDecoration(labelText: 'شناسه'), textDirection: TextDirection.ltr),
          TextField(controller: _name, decoration: const InputDecoration(labelText: 'نام نمایشی')),
          TextField(
            controller: _code,
            decoration: const InputDecoration(labelText: 'کد دسترسی کارکنان', helperText: 'از مدیر سامانه بگیرید'),
            textDirection: TextDirection.ltr,
            autocorrect: false,
            enableSuggestions: false,
          ),
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _busy
                ? null
                : () async {
                    setState(() => _busy = true);
                    try {
                      await widget.state.devSignIn(_id.text.trim().toLowerCase(), _name.text.trim(), accessCode: _code.text.trim());
                      _error = null;
                    } on ApiException catch (e) {
                      _error = e.isNetwork ? 'ارتباط با سرور برقرار نیست.' : e.message;
                    }
                    if (mounted) setState(() => _busy = false);
                  },
            child: Text(_busy ? 'در حال ورود…' : 'ورود'),
          ),
          if (_error != null) Text(_error!, style: const TextStyle(color: Colors.red)),
        ]),
      );
}

class MissionsScreen extends StatelessWidget {
  final ResponderState state;
  const MissionsScreen({super.key, required this.state});

  @override
  Widget build(BuildContext context) {
    final active = state.assignments.where((a) => a.isActive).toList();
    final done = state.assignments.where((a) => !a.isActive).toList();
    return Scaffold(
      appBar: AppBar(title: const Text('مأموریت‌های من'), actions: [
        IconButton(icon: const Icon(Icons.sync), onPressed: state.sync, tooltip: 'همگام‌سازی'),
        IconButton(icon: const Icon(Icons.logout), onPressed: state.signOut, tooltip: 'خروج'),
      ]),
      body: RefreshIndicator(
        onRefresh: state.sync,
        child: ListView(padding: const EdgeInsets.all(12), children: [
          if (state.offline)
            const Card(
              color: Color(0xFFFFF4DC),
              child: ListTile(
                leading: Icon(Icons.wifi_off),
                title: Text('حالت آفلاین'),
                subtitle: Text('تغییرات روی دستگاه ذخیره و پس از اتصال ارسال می‌شوند.'),
              ),
            ),
          if (state.fetchedAt != null)
            Text('آخرین دریافت از سرور: ${agoFa(state.fetchedAt!)}', style: Theme.of(context).textTheme.bodySmall),
          if (state.queue.pending.isNotEmpty)
            Card(child: ListTile(leading: const Icon(Icons.schedule_send), title: Text('${faDigits(state.queue.pending.length)} تغییر در صف ارسال'))),
          for (final p in state.problems)
            Card(
              color: const Color(0xFFFDE8E8),
              child: ListTile(
                leading: const Icon(Icons.warning_amber),
                title: Text(p.state == OpState.conflict ? 'تعارض با وضعیت سرور' : 'تغییر پذیرفته نشد'),
                subtitle: Text('${p.lastError ?? ''}\nوضعیت معتبر همان است که از سرور نمایش داده می‌شود.'),
                trailing: IconButton(icon: const Icon(Icons.close), onPressed: () => state.queue.dismiss(p.id)),
              ),
            ),
          if (active.isEmpty) const Padding(padding: EdgeInsets.all(24), child: Text('مأموریت فعالی ندارید.')),
          for (final a in active) MissionCard(state: state, a: a),
          if (done.isNotEmpty) const Padding(padding: EdgeInsets.only(top: 16), child: Text('پایان‌یافته')),
          for (final a in done)
            ListTile(title: Text('${a.incidentCode} · ${a.resourceName}'), subtitle: Text(assignmentStatusLabels[a.status] ?? a.status)),
        ]),
      ),
    );
  }
}

class MissionCard extends StatelessWidget {
  final ResponderState state;
  final Assignment a;
  const MissionCard({super.key, required this.state, required this.a});

  @override
  Widget build(BuildContext context) {
    final pending = state.hasPendingFor(a.id);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text('${a.incidentCode} — ${a.resourceName}', style: Theme.of(context).textTheme.titleMedium),
          Text('وضعیت: ${assignmentStatusLabels[a.status] ?? a.status}${pending ? ' (در انتظار همگام‌سازی)' : ''}'),
          Text('شرح ارجاع: ${a.reason}'),
          Text('تخصیص: ${agoFa(a.assignedAt)}', style: Theme.of(context).textTheme.bodySmall),
          if (state.casualtiesFor(a.incidentId).isNotEmpty)
            Text('مصدومان ثبت‌شده از این گوشی: ${state.casualtiesFor(a.incidentId).map(casualtyLine).join('، ')}'),
          const SizedBox(height: 8),
          Wrap(spacing: 8, runSpacing: 8, children: [
            for (final next in a.nextStatuses)
              FilledButton(onPressed: () => state.setStatus(a, next), child: Text(assignmentStatusLabels[next] ?? next)),
            OutlinedButton.icon(
              icon: const Icon(Icons.personal_injury),
              label: const Text('ثبت مصدوم'),
              onPressed: () => showModalBottomSheet<void>(
                context: context,
                isScrollControlled: true,
                builder: (_) => CasualtySheet(state: state, assignment: a),
              ),
            ),
            OutlinedButton.icon(
              icon: const Icon(Icons.my_location),
              label: const Text('ارسال موقعیت'),
              onPressed: () async {
                final loc = await currentLocation();
                if (loc != null) await state.reportPosition(a.resourceId, loc);
              },
            ),
          ]),
        ]),
      ),
    );
  }
}

const triageLabels = {'immediate': 'فوری (قرمز)', 'delayed': 'تأخیری (زرد)', 'minor': 'سرپایی (سبز)', 'deceased': 'فوت‌شده (سیاه)'};
const triageColors = {
  'immediate': Color(0xFFDC2626),
  'delayed': Color(0xFFFACC15),
  'minor': Color(0xFF16A34A),
  'deceased': Color(0xFF111827),
};

/// "T-00012 قرمز" once sent; "در صف" while queued offline; the server's reason if it was refused.
String casualtyLine(QueuedOp op) {
  final triage = (triageLabels[op.body['triage']] ?? '').split(' ').first;
  switch (op.state) {
    case OpState.done:
      return '${op.result?['tag_no'] ?? ''} $triage';
    case OpState.pending:
      return '$triage (در صف ارسال)';
    default:
      return '$triage (ثبت نشد: ${op.lastError ?? ''})';
  }
}

/// Field triage form: one tap per colour, optional age group, sex, tag number and note.
class CasualtySheet extends StatefulWidget {
  final ResponderState state;
  final Assignment assignment;
  const CasualtySheet({super.key, required this.state, required this.assignment});

  @override
  State<CasualtySheet> createState() => _CasualtySheetState();
}

class _CasualtySheetState extends State<CasualtySheet> {
  String? _triage;
  String _age = 'unknown';
  String _sex = 'unknown';
  final _tag = TextEditingController();
  final _notes = TextEditingController();
  bool _busy = false;

  @override
  void dispose() {
    _tag.dispose();
    _notes.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    setState(() => _busy = true);
    final loc = await currentLocation();
    await widget.state.recordCasualty(widget.assignment,
        triage: _triage!, ageGroup: _age, sex: _sex, tagNo: _tag.text, notes: _notes.text, at: loc);
    if (!mounted) return;
    Navigator.pop(context);
    final op = widget.state.casualtiesFor(widget.assignment.incidentId).firstOrNull;
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(op == null ? 'ثبت شد' : 'مصدوم: ${casualtyLine(op)}')));
  }

  @override
  Widget build(BuildContext context) => Padding(
        padding: EdgeInsets.fromLTRB(16, 16, 16, 16 + MediaQuery.of(context).viewInsets.bottom),
        child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Text('ثبت مصدوم — ${widget.assignment.incidentCode}', style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 8),
          Wrap(spacing: 8, runSpacing: 8, children: [
            for (final t in triageLabels.keys)
              ChoiceChip(
                label: Text(triageLabels[t]!, style: TextStyle(color: t == 'delayed' ? Colors.black : Colors.white)),
                selected: _triage == t,
                selectedColor: triageColors[t],
                backgroundColor: triageColors[t]!.withOpacity(0.6),
                onSelected: (_) => setState(() => _triage = t),
              ),
          ]),
          const SizedBox(height: 8),
          SegmentedButton<String>(
            segments: const [
              ButtonSegment(value: 'child', label: Text('کودک')),
              ButtonSegment(value: 'adult', label: Text('بزرگسال')),
              ButtonSegment(value: 'elderly', label: Text('سالمند')),
              ButtonSegment(value: 'unknown', label: Text('نامشخص')),
            ],
            selected: {_age},
            onSelectionChanged: (v) => setState(() => _age = v.first),
          ),
          const SizedBox(height: 8),
          SegmentedButton<String>(
            segments: const [
              ButtonSegment(value: 'female', label: Text('زن')),
              ButtonSegment(value: 'male', label: Text('مرد')),
              ButtonSegment(value: 'unknown', label: Text('نامشخص')),
            ],
            selected: {_sex},
            onSelectionChanged: (v) => setState(() => _sex = v.first),
          ),
          TextField(controller: _tag, textDirection: TextDirection.ltr,
              decoration: const InputDecoration(labelText: 'شماره برچسب تریاژ (اختیاری)')),
          TextField(controller: _notes, decoration: const InputDecoration(labelText: 'یادداشت پزشکی (اختیاری)')),
          const SizedBox(height: 4),
          const Text('نام و کد ملی ثبت نمی‌شود.', style: TextStyle(fontSize: 12)),
          const SizedBox(height: 8),
          FilledButton(onPressed: _triage == null || _busy ? null : _save, child: Text(_busy ? 'در حال ثبت…' : 'ثبت مصدوم')),
        ]),
      );
}
