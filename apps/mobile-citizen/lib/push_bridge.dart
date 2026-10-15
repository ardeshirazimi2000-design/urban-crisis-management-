import 'package:flutter/services.dart';

/// Starts and stops the Android background service that shows notifications while the app is closed
/// (android/.../PushService.kt). Elsewhere (tests, other platforms) the calls do nothing.
class PushBridge {
  static const _channel = MethodChannel('crisis/push');

  static Future<bool> start({required String url, required String topic, required String ongoing}) async {
    try {
      return await _channel.invokeMethod<bool>('start', {'url': url, 'topic': topic, 'ongoing': ongoing}) ?? false;
    } on MissingPluginException {
      return false;
    } on PlatformException {
      return false;
    }
  }

  static Future<void> stop() async {
    try {
      await _channel.invokeMethod<bool>('stop');
    } on MissingPluginException {
      // not on Android
    } on PlatformException {
      // nothing to stop
    }
  }
}
