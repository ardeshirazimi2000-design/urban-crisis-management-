import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_localizations/flutter_localizations.dart';

import 'platform.dart';
import 'screens.dart';
import 'state.dart';

const devAuth = bool.fromEnvironment('DEV_AUTH', defaultValue: true);

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final api = ApiClient(Uri.parse(apiBaseUrl), SecureTokens.read);
  final state = CitizenState(
    api: api,
    queue: OfflineQueue(await openStore('report_queue'), api),
    alertCache: await openStore('alert_cache'),
    shelterCache: await openStore('shelter_cache'),
    guidanceCache: await openStore('guidance_cache'),
    bundledGuidance: () => rootBundle.loadString('assets/guidance.fa.json'),
    readToken: SecureTokens.read,
    writeToken: SecureTokens.write,
    readPref: SecureTokens.readPref,
    writePref: SecureTokens.writePref,
    devAuth: devAuth,
  );
  runApp(CitizenApp(state: state));
  await state.start();
}

class CitizenApp extends StatelessWidget {
  final CitizenState state;
  const CitizenApp({super.key, required this.state});

  @override
  Widget build(BuildContext context) => MaterialApp(
        title: 'گزارش بحران',
        debugShowCheckedModeBanner: false,
        theme: crisisTheme(const Color(0xFFB91C1C)),
        locale: const Locale('fa'),
        supportedLocales: const [Locale('fa')],
        localizationsDelegates: GlobalMaterialLocalizations.delegates,
        home: HomeScreen(state: state),
      );
}
