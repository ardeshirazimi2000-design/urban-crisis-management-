import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';

import 'platform.dart';
import 'state.dart';

/// Shown on the home screen while there is no session.
class SignInCard extends StatelessWidget {
  final CitizenState state;
  const SignInCard({super.key, required this.state});

  @override
  Widget build(BuildContext context) => Card(
        color: const Color(0xFFE8F0FE),
        child: ListTile(
          leading: const Icon(Icons.verified_user),
          title: const Text('ورود به سامانه'),
          subtitle: const Text('برای ثبت گزارش و دریافت هشدار، با کد ملی و شماره موبایل به نام خودتان وارد شوید.'),
          trailing: FilledButton(
            onPressed: () => Navigator.push(context, MaterialPageRoute(builder: (_) => SignInScreen(state: state))),
            child: const Text('ورود'),
          ),
        ),
      );
}

/// Built-in sign-in: national code + mobile (verified with Shahkar on the first sign-in and on a new
/// device), then the SMS code. A first registration also asks for the name.
class SignInScreen extends StatefulWidget {
  final CitizenState state;
  const SignInScreen({super.key, required this.state});

  @override
  State<SignInScreen> createState() => _SignInScreenState();
}

class _SignInScreenState extends State<SignInScreen> {
  final _nc = TextEditingController();
  final _mobile = TextEditingController();
  final _code = TextEditingController();
  final _first = TextEditingController();
  final _last = TextEditingController();
  AuthChallenge? _challenge;
  String? _error;
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    widget.state.auth?.savedIdentity().then((v) {
      if (!mounted) return;
      _nc.text = v.$1 ?? '';
      _mobile.text = v.$2 ?? '';
    });
  }

  @override
  void dispose() {
    for (final c in [_nc, _mobile, _code, _first, _last]) {
      c.dispose();
    }
    super.dispose();
  }

  Future<void> _run(Future<void> Function() fn) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await fn();
    } on ApiException catch (e) {
      _error = describeApiError(e);
    }
    if (mounted) setState(() => _busy = false);
  }

  @override
  Widget build(BuildContext context) {
    final c = _challenge;
    return Scaffold(
      appBar: AppBar(title: const Text('ورود')),
      body: ListView(padding: const EdgeInsets.all(16), children: [
        if (c == null) ...[
          const Text('در اولین ورود، مالکیت شماره موبایل به نام کد ملی شما از سامانه شاهکار استعلام می‌شود؛ سپس کد تأیید پیامک می‌شود.'),
          const SizedBox(height: 12),
          TextField(controller: _nc, keyboardType: TextInputType.number, textDirection: TextDirection.ltr,
              decoration: const InputDecoration(labelText: 'کد ملی')),
          TextField(controller: _mobile, keyboardType: TextInputType.phone, textDirection: TextDirection.ltr,
              decoration: const InputDecoration(labelText: 'شماره موبایل', hintText: '09121234567')),
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _busy ? null : () => _run(() async {
              final ch = await widget.state.startSignIn(_nc.text, _mobile.text);
              setState(() => _challenge = ch);
            }),
            child: Text(_busy ? 'در حال استعلام…' : 'دریافت کد تأیید'),
          ),
        ] else ...[
          Text('کد تأیید به شماره ${c.mobileMasked} پیامک شد (${faDigits(c.expiresIn)} ثانیه اعتبار).'),
          if (c.shahkarVerified) const Text('مالکیت شماره موبایل با شاهکار تأیید شد.', style: TextStyle(color: Color(0xFF047857))),
          TextField(controller: _code, keyboardType: TextInputType.number, textDirection: TextDirection.ltr, autofocus: true,
              autofillHints: const [AutofillHints.oneTimeCode], decoration: const InputDecoration(labelText: 'کد تأیید')),
          if (c.needsProfile) ...[
            TextField(controller: _first, decoration: const InputDecoration(labelText: 'نام')),
            TextField(controller: _last, decoration: const InputDecoration(labelText: 'نام خانوادگی')),
          ],
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _busy ? null : () => _run(() async {
              await widget.state.completeSignIn(c, _code.text, firstName: _first.text, lastName: _last.text);
              if (context.mounted) Navigator.pop(context);
            }),
            child: Text(_busy ? 'در حال ورود…' : 'ورود'),
          ),
          TextButton(onPressed: _busy ? null : () => setState(() => _challenge = null), child: const Text('تغییر شماره / ارسال دوباره')),
        ],
        if (_error != null) Padding(padding: const EdgeInsets.only(top: 12), child: Text(_error!, style: const TextStyle(color: Colors.red))),
      ]),
    );
  }
}

/// One-line access history after signing in (last sign-in and failed attempts since).
class AccessHistoryCard extends StatelessWidget {
  final CitizenState state;
  const AccessHistoryCard({super.key, required this.state});

  @override
  Widget build(BuildContext context) {
    final h = state.lastAccess;
    if (h == null) return const SizedBox.shrink();
    final failed = h.failedSinceLastLogin;
    return Card(
      color: failed > 0 ? const Color(0xFFFFF4DC) : null,
      child: ListTile(
        leading: Icon(failed > 0 ? Icons.warning_amber : Icons.history),
        title: Text(h.lastLoginAt == null ? 'اولین ورود شما' : 'آخرین ورود: ${agoFa(h.lastLoginAt!)}'),
        subtitle: failed > 0 ? Text('${faDigits(failed)} تلاش ناموفق برای ورود به حساب شما ثبت شده است.') : null,
        trailing: IconButton(icon: const Icon(Icons.close), onPressed: state.dismissAccessHistory),
      ),
    );
  }
}
