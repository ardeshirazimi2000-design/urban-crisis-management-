import 'dart:async';

import 'package:uuid/uuid.dart';

import 'api_client.dart';

/// Small secrets kept in the OS keystore (Keychain / Android Keystore): tokens and the device identity.
abstract class CredentialStore {
  Future<String?> read(String key);
  Future<void> write(String key, String? value);
}

class MemoryCredentialStore implements CredentialStore {
  final Map<String, String> values = {};
  @override
  Future<String?> read(String key) async => values[key];
  @override
  Future<void> write(String key, String? value) async => value == null ? values.remove(key) : values[key] = value;
}

/// An SMS code was sent; the user must type it.
class AuthChallenge {
  final String id;
  final String mobileMasked;
  final int expiresIn;
  final int resendAfter;

  /// Shahkar confirmed national code ↔ mobile ownership for this sign-in (first sign-in or a new device).
  final bool shahkarVerified;

  /// No account exists yet: a citizen gives first and last name to register.
  final bool needsProfile;

  AuthChallenge({required this.id, required this.mobileMasked, required this.expiresIn, required this.resendAfter,
      required this.shahkarVerified, required this.needsProfile});

  factory AuthChallenge.fromJson(Map<String, dynamic> j) {
    final c = j['challenge'] as Map<String, dynamic>;
    return AuthChallenge(
      id: c['challenge_id'] as String,
      mobileMasked: c['mobile_masked'] as String? ?? '',
      expiresIn: (c['expires_in'] as num?)?.toInt() ?? 120,
      resendAfter: (c['resend_after'] as num?)?.toInt() ?? 60,
      shahkarVerified: j['shahkar_verified'] as bool? ?? false,
      needsProfile: j['needs_profile'] as bool? ?? false,
    );
  }
}

/// When the account was last used and how many attempts failed since (shown after sign-in).
class AccessHistory {
  final DateTime? lastLoginAt;
  final String? lastLoginIp;
  final int failedSinceLastLogin;

  AccessHistory({this.lastLoginAt, this.lastLoginIp, this.failedSinceLastLogin = 0});

  factory AccessHistory.fromJson(Map<String, dynamic>? j) => AccessHistory(
        lastLoginAt: DateTime.tryParse(j?['last_login_at'] as String? ?? ''),
        lastLoginIp: j?['last_login_ip'] as String?,
        failedSinceLastLogin: (j?['failed_since_last_login'] as num?)?.toInt() ?? 0,
      );
}

/// Built-in sign-in of the platform (AUTH_MODE=local):
/// the first sign-in, and the first one from a new device, verifies with Shahkar that the mobile belongs to
/// the national code's owner; every sign-in then needs the SMS code. A device token issued at sign-in lets
/// later sign-ins from this device skip Shahkar. Access tokens are short-lived and renewed with a rotating
/// refresh token; reusing an old refresh token ends the session on the server.
class SessionAuth {
  final ApiClient api;
  final CredentialStore store;
  Future<bool>? _refreshing;

  static const _access = 'auth.access_token';
  static const _refresh = 'auth.refresh_token';
  static const _deviceToken = 'auth.device_token';
  static const _deviceId = 'auth.device_id';
  static const _nationalCode = 'auth.national_code';
  static const _mobile = 'auth.mobile';

  SessionAuth(this.api, this.store) {
    api.refresh = refresh;
  }

  Future<String?> accessToken() => store.read(_access);
  Future<bool> get signedIn async => await store.read(_refresh) != null;

  /// Identity used at the last sign-in, to pre-fill the form.
  Future<(String?, String?)> savedIdentity() async => (await store.read(_nationalCode), await store.read(_mobile));

  /// Stable random id of this installation (never a hardware identifier).
  Future<String> deviceId() async {
    var id = await store.read(_deviceId);
    if (id == null) {
      id = 'dev-${const Uuid().v4()}';
      await store.write(_deviceId, id);
    }
    return id;
  }

  Future<AuthChallenge> start(String nationalCode, String mobile) async {
    final d = await api.request('POST', '/auth/app/start', body: {
      'national_code': nationalCode.trim(),
      'mobile': mobile.trim(),
      'device_id': await deviceId(),
      'device_token': await store.read(_deviceToken) ?? '',
    }) as Map<String, dynamic>;
    await store.write(_nationalCode, nationalCode.trim());
    await store.write(_mobile, mobile.trim());
    return AuthChallenge.fromJson(d);
  }

  Future<AccessHistory> verify(AuthChallenge c, String code, {String firstName = '', String lastName = '', String deviceName = ''}) async {
    final d = await api.request('POST', '/auth/app/verify', body: {
      'challenge_id': c.id,
      'code': code.trim(),
      'device_id': await deviceId(),
      if (deviceName.isNotEmpty) 'device_name': deviceName,
      if (firstName.isNotEmpty) 'first_name': firstName.trim(),
      if (lastName.isNotEmpty) 'last_name': lastName.trim(),
    }) as Map<String, dynamic>;
    final t = d['tokens'] as Map<String, dynamic>;
    await store.write(_access, t['access_token'] as String);
    await store.write(_refresh, t['refresh_token'] as String);
    await store.write(_deviceToken, d['device_token'] as String);
    return AccessHistory.fromJson(d['access_history'] as Map<String, dynamic>?);
  }

  /// Renews the access token once even when several requests notice expiry together. A rejected refresh
  /// token (session ended, revoked or idle) signs the user out; a network failure keeps the session.
  Future<bool> refresh() {
    return _refreshing ??= () async {
      try {
        final r = await store.read(_refresh);
        if (r == null) return false;
        final d = await api.request('POST', '/auth/refresh', body: {'refresh_token': r}, auth: false) as Map<String, dynamic>;
        await store.write(_access, d['access_token'] as String);
        await store.write(_refresh, d['refresh_token'] as String);
        return true;
      } on ApiException catch (e) {
        if (!e.isNetwork) await _clearSession();
        return false;
      } finally {
        scheduleMicrotask(() => _refreshing = null);
      }
    }();
  }

  Future<void> signOut() async {
    try {
      if (await accessToken() != null) await api.request('POST', '/auth/logout');
    } on ApiException {
      // the session ends locally either way
    }
    await _clearSession();
  }

  Future<void> _clearSession() async {
    await store.write(_access, null);
    await store.write(_refresh, null);
  }
}
