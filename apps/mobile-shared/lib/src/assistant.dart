/// Crisis assistant: matches a citizen's question against the approved guidance cards ON THE DEVICE.
/// Nothing is generated — every answer is an approved card — and the question never leaves the phone,
/// so the assistant works offline and keeps questions private.
library;

class GuidanceCard {
  final String slug;
  final int version;
  final String title;
  final String category;
  final List<String> keywords;
  final String body;
  final List<String> actions; // call:115, report, shelters, alerts
  final bool emergency;

  const GuidanceCard({required this.slug, required this.version, required this.title, required this.category,
      required this.keywords, required this.body, required this.actions, required this.emergency});

  factory GuidanceCard.fromJson(Map<String, dynamic> j) => GuidanceCard(
        slug: j['slug'] as String,
        version: (j['version'] as num?)?.toInt() ?? 1,
        title: j['title'] as String,
        category: j['category'] as String,
        keywords: (j['keywords'] as List).cast<String>(),
        body: j['body'] as String,
        actions: ((j['actions'] as List?) ?? const []).cast<String>(),
        emergency: j['emergency'] as bool? ?? false,
      );

  Map<String, dynamic> toJson() => {
        'slug': slug, 'version': version, 'title': title, 'category': category, 'keywords': keywords, 'body': body,
        'actions': actions, 'emergency': emergency,
      };
}

class KnowledgeBase {
  final int version;
  final List<GuidanceCard> cards;
  const KnowledgeBase(this.version, this.cards);

  factory KnowledgeBase.fromJson(Map<String, dynamic> j) => KnowledgeBase(
        (j['kb_version'] as num?)?.toInt() ?? 0,
        (j['cards'] as List).map((e) => GuidanceCard.fromJson((e as Map).cast<String, dynamic>())).toList(),
      );

  Map<String, dynamic> toJson() => {'kb_version': version, 'cards': cards.map((c) => c.toJson()).toList()};
}

class AssistantAnswer {
  final List<GuidanceCard> cards; // best first; empty when nothing matched
  const AssistantAnswer(this.cards);
  bool get emergency => cards.isNotEmpty && cards.first.emergency;
  bool get matched => cards.isNotEmpty;
}

const _charMap = {
  'ي': 'ی', 'ى': 'ی', 'ئ': 'ی', 'ك': 'ک', 'ة': 'ه', 'ۀ': 'ه', 'أ': 'ا', 'إ': 'ا', 'آ': 'ا', 'ٱ': 'ا', 'ؤ': 'و',
  '‌': ' ', '‏': '', '‎': '', 'ـ': '',
};

/// Everyday spoken forms → written forms used in the cards.
const _colloquial = {
  'خونه': 'خانه', 'میاد': 'می اید', 'میاد؟': 'می اید', 'چیکار': 'چه کار', 'چکار': 'چه کار', 'برم': 'بروم', 'بچم': 'بچه',
  'نمیتونم': 'نمی توانم', 'نمیتونه': 'نمی تواند', 'داره': 'دارد', 'گیرکردم': 'گیر کردم', 'بیرون': 'بیرون',
};

const _stop = {
  'و', 'در', 'به', 'از', 'که', 'را', 'رو', 'با', 'این', 'ان', 'است', 'هست', 'چه', 'چی', 'کنم', 'کنیم', 'باید', 'من', 'ما',
  'یک', 'یه', 'برای', 'تا', 'هم', 'اگر', 'چطور', 'چگونه', 'الان', 'دارد', 'شده', 'شد', 'بکنم', 'کار', 'لطفا', 'سلام', 'کمک',
  'می', 'نمی', 'ای', 'های', 'ها', 'خیلی', 'کجا', 'کی',
};

/// Normalises Persian text: Arabic letter forms, diacritics, ZWNJ, digits, punctuation, spoken forms.
String normalizeFa(String input) {
  final b = StringBuffer();
  for (final r in input.toLowerCase().runes) {
    final ch = String.fromCharCode(r);
    if (r >= 0x064B && r <= 0x065F || r == 0x0670) continue; // harakat
    if (r >= 0x06F0 && r <= 0x06F9) {
      b.writeCharCode(0x30 + r - 0x06F0);
      continue;
    }
    if (r >= 0x0660 && r <= 0x0669) {
      b.writeCharCode(0x30 + r - 0x0660);
      continue;
    }
    final mapped = _charMap[ch];
    if (mapped != null) {
      b.write(mapped);
    } else if (RegExp(r'[\p{L}\p{N}]', unicode: true).hasMatch(ch)) {
      b.write(ch);
    } else {
      b.write(' ');
    }
  }
  final words = b.toString().split(RegExp(r'\s+')).where((w) => w.isNotEmpty).map((w) => _colloquial[w] ?? w);
  return words.join(' ');
}

const _suffixes = ['هایی', 'های', 'ها', 'ترین', 'تر', 'ام', 'ات', 'اش', 'مان', 'تان', 'شان', 'یم', 'ید', 'ند', 'ی', 'م'];

/// Light stemming: drops the verb prefix «می» and common suffixes, keeping at least three letters.
String stemFa(String w) {
  var s = w;
  if (s.startsWith('می') && s.length > 4) s = s.substring(2);
  if (s.startsWith('نمی') && s.length > 5) s = s.substring(3);
  for (final suf in _suffixes) {
    if (s.endsWith(suf) && s.length - suf.length >= 3) {
      s = s.substring(0, s.length - suf.length);
      break;
    }
  }
  return s;
}

List<String> _tokens(String normalized) => normalized.split(' ').where((t) => t.isNotEmpty && !_stop.contains(t)).toList();

class Assistant {
  final KnowledgeBase kb;
  Assistant(this.kb);

  /// Scores each card: a whole keyword phrase in the question counts most, then stem matches, then
  /// prefix matches; title words help a little. Only reasonably confident matches are returned.
  AssistantAnswer answer(String question, {int max = 3}) {
    final q = ' ${normalizeFa(question)} ';
    final qStems = _tokens(q.trim()).map(stemFa).toList();
    if (qStems.isEmpty) return const AssistantAnswer([]);
    final scored = <(GuidanceCard, int)>[];
    for (final card in kb.cards) {
      var score = 0;
      for (final k in card.keywords) {
        final kn = normalizeFa(k);
        if (kn.isEmpty) continue;
        if (kn.contains(' ')) {
          if (q.contains(' $kn ')) score += 4;
          continue;
        }
        final ks = stemFa(kn);
        if (qStems.contains(ks)) {
          score += 3;
        } else if (ks.length >= 3 && qStems.any((t) => t.length >= 3 && (t.startsWith(ks) || ks.startsWith(t)))) {
          score += 1;
        }
      }
      for (final t in _tokens(normalizeFa(card.title)).map(stemFa)) {
        if (t.length >= 3 && qStems.contains(t)) score += 1;
      }
      if (score >= 3) scored.add((card, score + (card.emergency ? 1 : 0)));
    }
    scored.sort((a, b) => b.$2.compareTo(a.$2));
    return AssistantAnswer(scored.take(max).map((e) => e.$1).toList());
  }

  /// Suggested questions shown before the first question and when nothing matched.
  List<GuidanceCard> get suggestions {
    final emergency = kb.cards.where((c) => c.emergency).take(4);
    final other = kb.cards.where((c) => !c.emergency).take(4);
    return [...emergency, ...other];
  }
}
