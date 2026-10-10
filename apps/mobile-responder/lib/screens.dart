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
          const SizedBox(height: 8),
          Wrap(spacing: 8, runSpacing: 8, children: [
            for (final next in a.nextStatuses)
              FilledButton(onPressed: () => state.setStatus(a, next), child: Text(assignmentStatusLabels[next] ?? next)),
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
