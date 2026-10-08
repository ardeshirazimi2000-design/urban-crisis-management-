import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';

/// Raw byte persistence (a file on device, memory in tests).
abstract class ByteStore {
  Future<Uint8List?> read();
  Future<void> write(Uint8List data);
}

class MemoryByteStore implements ByteStore {
  Uint8List? data;
  @override
  Future<Uint8List?> read() async => data;
  @override
  Future<void> write(Uint8List d) async => data = d;
}

/// Atomic file store: writes a temp file then renames, so a crash never leaves a half-written queue.
class FileByteStore implements ByteStore {
  final File file;
  FileByteStore(String path) : file = File(path);

  @override
  Future<Uint8List?> read() async => await file.exists() ? await file.readAsBytes() : null;

  @override
  Future<void> write(Uint8List d) async {
    await file.parent.create(recursive: true);
    final tmp = File('${file.path}.tmp');
    await tmp.writeAsBytes(d, flush: true);
    await tmp.rename(file.path);
  }
}

/// Supplies the 256-bit data key. On devices it lives in the OS keystore (flutter_secure_storage).
abstract class KeyProvider {
  Future<List<int>> key();
}

class StaticKeyProvider implements KeyProvider {
  final List<int> bytes;
  StaticKeyProvider(this.bytes) : assert(bytes.length == 32);
  @override
  Future<List<int>> key() async => bytes;
}

/// JSON document store encrypted at rest with AES-256-GCM (authenticated: tampering is detected).
class EncryptedJsonStore {
  final ByteStore bytes;
  final KeyProvider keys;
  final _algo = AesGcm.with256bits();

  EncryptedJsonStore(this.bytes, this.keys);

  Future<Object?> read() async {
    final raw = await bytes.read();
    if (raw == null || raw.isEmpty) return null;
    final box = SecretBox.fromConcatenation(raw, nonceLength: 12, macLength: 16);
    final clear = await _algo.decrypt(box, secretKey: SecretKey(await keys.key()));
    return jsonDecode(utf8.decode(clear));
  }

  Future<void> write(Object? value) async {
    final box = await _algo.encrypt(utf8.encode(jsonEncode(value)), secretKey: SecretKey(await keys.key()));
    await bytes.write(box.concatenation());
  }
}
