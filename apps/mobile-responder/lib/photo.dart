import 'dart:io';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:geolocator/geolocator.dart';
import 'package:image/image.dart' as img;
import 'package:image_picker/image_picker.dart';
import 'package:path_provider/path_provider.dart';

/// Longest side of a field photo after processing (Full HD).
const maxPhotoSide = 1920;

/// JPEG quality; with Full HD this keeps a photo around 150–300 KB.
const photoQuality = 75;

/// Photos per building assessment (the server allows up to 10).
const maxPhotosPerAssessment = 5;

/// Processed photos are kept this long after the server confirmed them, then deleted from the phone.
const keepSentPhotos = Duration(days: 7);

/// A geotagged photo taken on site, kept in the app's private storage (not the gallery) until it is sent.
class FieldPhoto {
  final String path;

  /// Sent with the upload so a retried upload from the offline queue never creates a second copy on the server.
  final String clientRef;
  final double lat, lng, accuracyM;
  final double? altitudeM;
  final DateTime capturedAt;

  const FieldPhoto({required this.path, required this.clientRef, required this.lat, required this.lng,
      required this.accuracyM, this.altitudeM, required this.capturedAt});

  Map<String, dynamic> toJson() => {
        'path': path, 'client_ref': clientRef, 'lat': lat, 'lng': lng, 'accuracy_m': accuracyM,
        if (altitudeM != null) 'altitude_m': altitudeM, 'captured_at': capturedAt.toUtc().toIso8601String(),
      };

  /// Metadata sent as query parameters of POST /media.
  static Map<String, String> uploadQuery(Map p, {String? incidentId}) => {
        'client_ref': p['client_ref'] as String,
        'lat': (p['lat'] as num).toStringAsFixed(6),
        'lng': (p['lng'] as num).toStringAsFixed(6),
        'acc': (p['accuracy_m'] as num).toStringAsFixed(1),
        if (p['altitude_m'] != null) 'alt': (p['altitude_m'] as num).toStringAsFixed(1),
        'captured_at': (p['captured_at'] as String).replaceFirst(RegExp(r'\.\d+'), ''),
        if (incidentId != null) 'incident_id': incidentId,
      };
}

class PhotoException implements Exception {
  final String message;
  PhotoException(this.message);
  @override
  String toString() => message;
}

String _two(int v) => v.toString().padLeft(2, '0');

/// Watermark lines burnt into the bottom of the photo (ASCII only: the bitmap font has no Persian glyphs).
List<String> watermarkLines({required String label, required double lat, required double lng, required double accuracyM,
    double? altitudeM, required DateTime at}) {
  final l = at.toLocal();
  final off = l.timeZoneOffset;
  final sign = off.isNegative ? '-' : '+';
  final tz = '$sign${_two(off.inHours.abs())}:${_two(off.inMinutes.abs() % 60)}';
  final safe = label.replaceAll(RegExp(r'[^A-Za-z0-9 _\-]'), '').trim();
  return [
    '${safe.isEmpty ? 'FIELD' : safe} | ${l.year}/${_two(l.month)}/${_two(l.day)} ${_two(l.hour)}:${_two(l.minute)}:${_two(l.second)} UTC$tz',
    'Lat ${lat.toStringAsFixed(5)}  Lon ${lng.toStringAsFixed(5)}'
        '${altitudeM == null ? '' : '  Alt ${altitudeM.round()}m'}  Acc ${accuracyM.round()}m',
  ];
}

class _Job {
  final Uint8List bytes;
  final List<String> lines;
  const _Job(this.bytes, this.lines);
}

/// Resizes to at most Full HD, applies the camera's orientation, burns in the watermark and encodes JPEG.
/// Pure Dart; run it off the UI thread with [compute].
Uint8List processFieldPhoto(Uint8List input, List<String> lines) {
  final decoded = img.decodeImage(input);
  if (decoded == null) throw PhotoException('تصویر قابل خواندن نیست');
  var im = img.bakeOrientation(decoded);
  final side = max(im.width, im.height);
  if (side > maxPhotoSide) {
    im = im.width >= im.height ? img.copyResize(im, width: maxPhotoSide) : img.copyResize(im, height: maxPhotoSide);
  }
  final font = im.width >= 1500 ? img.arial48 : (im.width >= 640 ? img.arial24 : img.arial14);
  final pad = (font.lineHeight * 0.4).round();
  final barH = font.lineHeight * lines.length + pad * 2;
  img.fillRect(im, x1: 0, y1: im.height - barH, x2: im.width, y2: im.height, color: img.ColorRgba8(0, 0, 0, 170));
  for (var i = 0; i < lines.length; i++) {
    img.drawString(im, lines[i], font: font, x: pad * 2, y: im.height - barH + pad + i * font.lineHeight,
        color: img.ColorRgba8(255, 255, 255, 255));
  }
  var out = img.encodeJpg(im, quality: photoQuality);
  if (out.length > 400 * 1024) out = img.encodeJpg(im, quality: 60);
  return out;
}

Uint8List _run(_Job j) => processFieldPhoto(j.bytes, j.lines);

Future<Directory> _photoDir() async {
  final d = Directory('${(await getApplicationSupportDirectory()).path}/field_photos');
  if (!await d.exists()) await d.create(recursive: true);
  return d;
}

String _ref() {
  final r = Random.secure();
  return 'ph-${List.generate(16, (_) => r.nextInt(256).toRadixString(16).padLeft(2, '0')).join()}';
}

/// Location permission and service, asked before the camera opens so the two dialogs never overlap.
Future<void> _ensureLocation() async {
  if (!await Geolocator.isLocationServiceEnabled()) {
    throw PhotoException('مکان‌یاب (GPS) خاموش است؛ برای عکس محل حادثه آن را روشن کنید.');
  }
  var perm = await Geolocator.checkPermission();
  if (perm == LocationPermission.denied) perm = await Geolocator.requestPermission();
  if (perm == LocationPermission.denied || perm == LocationPermission.deniedForever) {
    throw PhotoException('اجازه دسترسی به موقعیت داده نشده است.');
  }
}

/// Opens the camera, fixes the position at the moment of the shot, then compresses and watermarks the photo.
/// Returns null when the user closes the camera without taking a photo.
Future<FieldPhoto?> takeFieldPhoto({required String label}) async {
  await _ensureLocation();
  // GPS warms up while the camera is open.
  final position = Geolocator.getCurrentPosition(
          locationSettings: const LocationSettings(accuracy: LocationAccuracy.best, timeLimit: Duration(seconds: 45)))
      .then<Position?>((p) => p, onError: (_) => null);
  final shot = await ImagePicker().pickImage(
      source: ImageSource.camera, preferredCameraDevice: CameraDevice.rear, maxWidth: 2560, maxHeight: 2560, imageQuality: 95);
  if (shot == null) return null;
  final takenAt = DateTime.now();
  final pos = await position;
  final raw = File(shot.path);
  try {
    if (pos == null) throw PhotoException('موقعیت GPS به دست نیامد؛ در فضای باز دوباره عکس بگیرید.');
    final alt = pos.altitude == 0 ? null : pos.altitude;
    final lines = watermarkLines(label: label, lat: pos.latitude, lng: pos.longitude, accuracyM: pos.accuracy,
        altitudeM: alt, at: takenAt);
    final bytes = await compute(_run, _Job(await raw.readAsBytes(), lines));
    final ref = _ref();
    final out = File('${(await _photoDir()).path}/$ref.jpg');
    await out.writeAsBytes(bytes, flush: true);
    return FieldPhoto(path: out.path, clientRef: ref, lat: pos.latitude, lng: pos.longitude,
        accuracyM: pos.accuracy, altitudeM: alt, capturedAt: takenAt.toUtc());
  } finally {
    // The full-size original from the camera is never kept.
    if (await raw.exists()) await raw.delete();
  }
}

Future<void> deletePhotoFile(String path) async {
  final f = File(path);
  if (await f.exists()) await f.delete();
}

/// Deletes processed photos no longer needed: sent and confirmed more than [keepSentPhotos] ago, or no longer
/// referenced by any queued item and older than that. Photos still waiting to be sent are always kept.
Future<void> cleanupPhotos({required Set<String> waiting, required Map<String, DateTime> sentAt}) async {
  final dir = await _photoDir();
  final now = DateTime.now();
  await for (final e in dir.list()) {
    if (e is! File || waiting.contains(e.path)) continue;
    final since = sentAt[e.path] ?? (await e.lastModified());
    if (now.difference(since) > keepSentPhotos) await e.delete();
  }
}
