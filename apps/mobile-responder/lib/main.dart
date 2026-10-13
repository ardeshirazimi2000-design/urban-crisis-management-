import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter_localizations/flutter_localizations.dart';

import 'platform.dart';
import 'screens.dart';
import 'sign_in.dart';
import 'state.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  late final SessionAuth? auth;
  final api = ApiClient(Uri.parse(apiBaseUrl), () => auth?.accessToken() ?? SecureTokens.read());
  auth = devAuth ? null : SessionAuth(api, SecureCredentialStore());
  final state = ResponderState(
    auth: auth,
    api: api,
    queueStore: await openStore('responder_queue'),
    cache: await openStore('assignment_cache'),
    readToken: SecureTokens.read,
    writeToken: SecureTokens.write,
    readPref: SecureTokens.readPref,
    writePref: SecureTokens.writePref,
  );
  runApp(ResponderApp(state: state));
  await state.start();
}

class ResponderApp extends StatelessWidget {
  final ResponderState state;
  const ResponderApp({super.key, required this.state});

  @override
  Widget build(BuildContext context) => MaterialApp(
        title: 'امدادگر',
        debugShowCheckedModeBanner: false,
        theme: crisisTheme(const Color(0xFF1D4ED8)),
        locale: const Locale('fa'),
        supportedLocales: const [Locale('fa')],
        localizationsDelegates: GlobalMaterialLocalizations.delegates,
        home: ListenableBuilder(
          listenable: state,
          builder: (_, __) => state.signedIn
              ? MissionsScreen(state: state)
              : state.auth != null
                  ? LocalSignInScreen(state: state)
                  : SignInScreen(state: state),
        ),
      );
}
