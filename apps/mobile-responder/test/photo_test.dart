import 'dart:convert';
import 'dart:io';

import 'package:crisis_core/crisis_core.dart';
import 'package:crisis_responder/photo.dart';
import 'package:crisis_responder/state.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:image/image.dart' as img;

const _utf8 = {'content-type': 'application/json; charset=utf-8'};

void main() {
  test('photo is reduced to Full HD, watermarked and compressed', () {
    final camera = img.Image(width: 4000, height: 3000);
    img.fill(camera, color: img.ColorRgb8(200, 210, 220));
    final lines = watermarkLines(label: 'INC-20261109-0007', lat: 35.312345678, lng: 46.998771, accuracyM: 6.4, altitudeM: 1480.2,
        at: DateTime.utc(2026, 11, 9, 10, 30, 5));
    final out = processFieldPhoto(img.encodeJpg(camera, quality: 95), lines);
    final back = img.decodeJpg(out)!;
    expect([back.width, back.height], [1920, 1440]);
    expect(out.length, lessThan(400 * 1024));
    // The bottom band is darkened for the text; the top of the photo is untouched.
    expect(back.getPixel(5, back.height - 3).r, lessThan(120));
    expect(back.getPixel(5, 5).r, greaterThan(180));
    expect(lines[0], startsWith('INC-20261109-0007 | 2026/'));
    expect(lines[1], contains('Lat 35.31235  Lon 46.99877'));
    expect(lines[1], contains('Alt 1480m'));
    expect(lines.join(), matches(RegExp(r'^[\x20-\x7E]+$')));
  });

  test('assessment photos are uploaded once, resumed after a dropped connection, then the assessment carries their ids', () async {
    final dir = await Directory.systemTemp.createTemp('photos');
    final photos = [
      for (var i = 0; i < 3; i++)
        FieldPhoto(path: '${dir.path}/p$i.jpg', clientRef: 'ph-000000000000000$i', lat: 35.3, lng: 47.0, accuracyM: 5,
            altitudeM: 1400, capturedAt: DateTime.utc(2026, 11, 9, 10, i)),
    ];
    for (final p in photos) {
      await File(p.path).writeAsBytes(img.encodeJpg(img.Image(width: 8, height: 8)));
    }
    var online = false, dropSecond = true;
    final uploads = <String>[];
    Map<String, dynamic>? assessment;
    final client = MockClient((req) async {
      if (!online) throw http.ClientException('offline');
      if (req.url.path.endsWith('/media')) {
        final ref = req.url.queryParameters['client_ref']!;
        expect(req.headers['Content-Type'], 'image/jpeg');
        expect(req.url.queryParameters['captured_at'], isNot(contains('.')));
        if (ref.endsWith('1') && dropSecond) {
          dropSecond = false;
          throw http.ClientException('connection reset');
        }
        uploads.add(ref);
        if (ref.endsWith('2')) {
          return http.Response(jsonEncode({'error': {'code': 'VALIDATION_ERROR', 'message': 'x',
              'details': [{'field': 'body', 'reason': 'unreadable_image'}]}}), 422, headers: _utf8);
        }
        return http.Response(jsonEncode({'media_id': 'm-$ref'}), 201, headers: _utf8);
      }
      if (req.url.path.endsWith('/damage-assessments')) {
        assessment = jsonDecode(req.body) as Map<String, dynamic>;
        return http.Response(jsonEncode({'id': 'd1'}), 201, headers: _utf8);
      }
      return http.Response(jsonEncode({'items': []}), 200, headers: _utf8);
    });
    final key = StaticKeyProvider(List.filled(32, 7));
    final state = ResponderState(api: ApiClient(Uri.parse('http://x/api/v1'), () async => 't', client: client),
        queueStore: EncryptedJsonStore(MemoryByteStore(), key), cache: EncryptedJsonStore(MemoryByteStore(), key),
        readToken: () async => 't', writeToken: (_) async {});
    await state.queue.load();

    await state.assessBuilding(at: const GeoLocation(lat: 35.3, lng: 47.0, accuracyM: 5), tag: 'yellow', buildingUse: 'residential',
        observations: ['major_cracks'], incidentId: 'i1', photos: photos);
    expect(uploads, isEmpty, reason: 'offline: everything stays on the phone');

    online = true;
    await state.sync(); // first photo goes, the connection drops on the second
    expect(uploads, ['ph-0000000000000000']);
    expect(assessment, isNull);
    expect(ResponderState.photoProgress(state.queue.pending.single).$1, 1);

    await state.sync();
    expect(uploads, ['ph-0000000000000000', 'ph-0000000000000001', 'ph-0000000000000002'], reason: 'no photo sent twice');
    expect(assessment!['media_ids'], ['m-ph-0000000000000000', 'm-ph-0000000000000001'],
        reason: 'a photo the server refused does not block the assessment');
    expect(assessment!.containsKey('_photos'), isFalse);
    expect(state.queue.pending, isEmpty);
    await dir.delete(recursive: true);
  });
}
