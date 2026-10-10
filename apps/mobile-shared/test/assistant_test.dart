import 'dart:convert';
import 'dart:io';

import 'package:crisis_core/crisis_core.dart';
import 'package:test/test.dart';

void main() {
  // The canonical guidance served by the API and bundled in the citizen app.
  final kb = KnowledgeBase.fromJson(
      jsonDecode(File('../../services/core/seeds/guidance.fa.json').readAsStringSync()) as Map<String, dynamic>);
  final a = Assistant(kb);
  String? top(String q) => a.answer(q).cards.firstOrNull?.slug;

  test('normalisation unifies Arabic letters, digits, ZWNJ and spoken forms', () {
    expect(normalizeFa('كمك ي٣۴ می‌لرزه!'), 'کمک ی34 می لرزه');
    expect(normalizeFa('خونه'), 'خانه');
    expect(stemFa('خانه‌ها'.replaceAll('‌', '')), 'خانه');
  });

  test('emergencies are recognised in everyday phrasing and come first', () {
    expect(top('زیر آوار گیر کردم کمک کنید'), 'trapped-under-rubble');
    expect(a.answer('زیر آوار موندم').emergency, isTrue);
    expect(top('بوی گاز میاد چیکار کنم'), 'gas-leak');
    expect(top('زمین داره میلرزه'), 'during-earthquake');
    expect(top('خونریزی شدید داره'), 'severe-bleeding');
    expect(top('بابام بیهوش شده نفس نمیکشه'), 'unconscious');
    expect(top('خونه آتیش گرفته دود همه جا رو گرفته'), 'fire');
  });

  test('everyday needs find their card', () {
    expect(top('کجا برم خونه ندارم'), 'find-shelter');
    expect(top('دستم شکسته'), 'fracture');
    expect(top('بچه ام خیلی ترسیده'), 'children-stress');
    expect(top('شماره اورژانس چنده'), 'emergency-numbers');
    expect(top('آب لوله کشی رو میشه خورد'), 'drinking-water');
  });

  test('unrelated questions do not get a made-up answer', () {
    expect(a.answer('سلام').matched, isFalse);
    expect(a.answer('قیمت دلار امروز').matched, isFalse);
    expect(a.answer('').matched, isFalse);
  });

  test('suggestions start with emergencies', () {
    expect(a.suggestions.first.emergency, isTrue);
  });
}
