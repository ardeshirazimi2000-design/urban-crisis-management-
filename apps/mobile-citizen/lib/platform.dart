import 'dart:convert';
import 'dart:math';

import 'package:crisis_core/crisis_core.dart';
import 'package:flutter/material.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:geolocator/geolocator.dart';
import 'package:path_provider/path_provider.dart';

/// API base URL: pass with --dart-define=API_BASE_URL=https://.../api/v1 (Android emulator: 10.0.2.2).
const apiBaseUrl = String.fromEnvironment('API_BASE_URL', defaultValue: 'http://10.0.2.2:8080/api/v1');

/// Data key for the offline queue, generated on first run and kept in the OS keystore (Keychain / Keystore).
class SecureKeyProvider implements KeyProvider {
  static const _storage = FlutterSecureStorage(aOptions: AndroidOptions(encryptedSharedPreferences: true));
  List<int>? _cached;

  @override
  Future<List<int>> key() async {
    if (_cached != null) return _cached!;
    final existing = await _storage.read(key: 'queue_key_v1');
    if (existing != null) return _cached = base64Decode(existing);
    final rnd = Random.secure();
    final k = List<int>.generate(32, (_) => rnd.nextInt(256));
    await _storage.write(key: 'queue_key_v1', value: base64Encode(k));
    return _cached = k;
  }
}

class SecureTokens {
  static const _storage = FlutterSecureStorage(aOptions: AndroidOptions(encryptedSharedPreferences: true));
  static Future<String?> read() => _storage.read(key: 'access_token');
  static Future<void> write(String? t) =>
      t == null ? _storage.delete(key: 'access_token') : _storage.write(key: 'access_token', value: t);
  static Future<String?> readPref(String key) => _storage.read(key: 'pref.$key');
  static Future<void> writePref(String key, String? v) =>
      v == null ? _storage.delete(key: 'pref.$key') : _storage.write(key: 'pref.$key', value: v);
}

Future<EncryptedJsonStore> openStore(String name) async {
  final dir = await getApplicationSupportDirectory();
  return EncryptedJsonStore(FileByteStore('${dir.path}/$name.bin'), SecureKeyProvider());
}

/// Current position with its accuracy; returns null when permission is denied or GPS is unavailable.
Future<GeoLocation?> currentLocation() async {
  if (!await Geolocator.isLocationServiceEnabled()) return null;
  var perm = await Geolocator.checkPermission();
  if (perm == LocationPermission.denied) perm = await Geolocator.requestPermission();
  if (perm == LocationPermission.denied || perm == LocationPermission.deniedForever) return null;
  final p = await Geolocator.getCurrentPosition(
      locationSettings: const LocationSettings(accuracy: LocationAccuracy.high, timeLimit: Duration(seconds: 20)));
  return GeoLocation(lat: p.latitude, lng: p.longitude, accuracyM: p.accuracy.clamp(1, 5000).toDouble(), source: 'gps');
}

ThemeData crisisTheme(Color seed) => ThemeData(
      colorScheme: ColorScheme.fromSeed(seedColor: seed),
      useMaterial3: true,
      visualDensity: VisualDensity.standard,
    );

String faDigits(Object v) {
  const fa = '۰۱۲۳۴۵۶۷۸۹';
  return v.toString().replaceAllMapped(RegExp(r'\d'), (m) => fa[int.parse(m[0]!)]);
}

String agoFa(DateTime t) {
  final s = DateTime.now().difference(t).inSeconds;
  if (s < 60) return '${faDigits(s)} ثانیه پیش';
  if (s < 3600) return '${faDigits(s ~/ 60)} دقیقه پیش';
  if (s < 86400) return '${faDigits(s ~/ 3600)} ساعت پیش';
  return '${faDigits(s ~/ 86400)} روز پیش';
}
