import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';

import 'platform.dart';
import 'state.dart';

/// Built-in sign-in for responders: national code + mobile (Shahkar on the first sign-in and on a new device),
/// then the SMS code. Accounts are defined by the security administrator in the console.
class LocalSignInScreen extends StatefulWidget {
  final ResponderState state;
  const LocalSignInScreen({super.key, required this.state});

  @override
  State<LocalSignInScreen> createState() => _LocalSignInScreenState();
}

class _LocalSignInScreenState extends State<LocalSignInScreen> {
  final _nc = TextEditingController();
  final _mobile = TextEditingController();
  final _code = TextEditingController();
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
    _nc.dispose();
    _mobile.dispose();
    _code.dispose();
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
      appBar: AppBar(title: const Text('ورود امدادگر')),
      body: ListView(padding: const EdgeInsets.all(16), children: [
        if (c == null) ...[
          const Text('با کد ملی و شماره موبایل ثبت‌شده توسط مدیر سامانه وارد شوید. در اولین ورود از هر گوشی، مالکیت شماره از شاهکار استعلام می‌شود.'),
          TextField(controller: _nc, keyboardType: TextInputType.number, textDirection: TextDirection.ltr,
              decoration: const InputDecoration(labelText: 'کد ملی')),
          TextField(controller: _mobile, keyboardType: TextInputType.phone, textDirection: TextDirection.ltr,
              decoration: const InputDecoration(labelText: 'شماره موبایل')),
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _busy ? null : () => _run(() async {
              final ch = await widget.state.startSignIn(_nc.text, _mobile.text);
              if (ch.needsProfile) {
                _error = 'حسابی با این کد ملی برای امدادگران تعریف نشده است؛ با مدیر سامانه تماس بگیرید.';
                return;
              }
              setState(() => _challenge = ch);
            }),
            child: Text(_busy ? 'در حال بررسی…' : 'دریافت کد تأیید'),
          ),
        ] else ...[
          Text('کد تأیید به شماره ${c.mobileMasked} پیامک شد (${faDigits(c.expiresIn)} ثانیه اعتبار).'),
          TextField(controller: _code, keyboardType: TextInputType.number, textDirection: TextDirection.ltr, autofocus: true,
              autofillHints: const [AutofillHints.oneTimeCode], decoration: const InputDecoration(labelText: 'کد تأیید')),
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _busy ? null : () => _run(() => widget.state.completeSignIn(c, _code.text)),
            child: Text(_busy ? 'در حال ورود…' : 'ورود'),
          ),
          TextButton(onPressed: _busy ? null : () => setState(() => _challenge = null), child: const Text('تغییر شماره / ارسال دوباره')),
        ],
        if (_error != null) Padding(padding: const EdgeInsets.only(top: 12), child: Text(_error!, style: const TextStyle(color: Colors.red))),
      ]),
    );
  }
}
