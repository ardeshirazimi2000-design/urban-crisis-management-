import 'api_client.dart';

/// Field-level reasons the API returns, in plain Persian for citizens and responders.
const _reasons = <String, String>{
  'location:outside_service_area': 'موقعیت شما خارج از محدوده تحت پوشش این سامانه است.',
  'location.lat:out_of_range': 'موقعیت دریافت‌شده نامعتبر است؛ دوباره موقعیت را بگیرید.',
  'location.lng:out_of_range': 'موقعیت دریافت‌شده نامعتبر است؛ دوباره موقعیت را بگیرید.',
  'location.accuracy_m:must_be_between_0_and_5000': 'دقت موقعیت کافی نیست؛ در فضای باز دوباره موقعیت را بگیرید.',
  'occurred_at:in_future': 'ساعت گوشی با ساعت سرور هماهنگ نیست؛ تاریخ و ساعت گوشی را روی «خودکار» بگذارید.',
  'occurred_at:too_old': 'زمان رخداد بیش از حد قدیمی است.',
  'description:too_long': 'شرح گزارش بیش از حد طولانی است.',
  'type:not_allowed': 'نوع رخداد معتبر نیست؛ نسخه جدید برنامه را نصب کنید.',
};

const _codes = <String, String>{
  'RATE_LIMITED': 'تعداد گزارش‌ها بیش از حد مجاز است؛ چند دقیقه بعد دوباره تلاش کنید.',
  'UNAUTHENTICATED': 'نشست شما منقضی شده است؛ برنامه را دوباره باز کنید.',
  'FORBIDDEN': 'اجازه انجام این کار را ندارید.',
  'NETWORK': 'ارتباط با سرور برقرار نیست.',
  'VERSION_CONFLICT': 'این مورد در این فاصله توسط مرکز تغییر کرده است.',
};

/// Human-readable explanation of an API error, with a short tracking id for support.
String describeApiError(ApiException e) {
  final parts = <String>[];
  for (final d in e.details) {
    final key = '${d['field']}:${d['reason']}';
    parts.add(_reasons[key] ?? 'مقدار «${d['field']}» معتبر نیست (${d['reason']}).');
  }
  if (parts.isEmpty) parts.add(_codes[e.code] ?? e.message);
  final ref = e.correlationId.length >= 8 ? ' (کد پیگیری: ${e.correlationId.substring(0, 8)})' : '';
  return parts.toSet().join(' ') + ref;
}
