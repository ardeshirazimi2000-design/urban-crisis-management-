# اپ شهروند (Flutter)

ثبت گزارش با موقعیت و دقت مکان، صف آفلاین رمزنگاری‌شده (AES-256-GCM، کلید در Keystore/Keychain)، ارسال idempotent
پس از برقراری ارتباط، و نمایش هشدارهای رسمی فعال محدوده (با صادرکننده و انقضا؛ هشدار منقضی حتی از کش نمایش داده نمی‌شود).

```bash
flutter pub get
flutter test
flutter run --dart-define=API_BASE_URL=http://10.0.2.2:8080/api/v1   # شبیه‌ساز اندروید
```

`DEV_AUTH=true` (پیش‌فرض) با endpoint محلی `/dev/token` هویت آزمایشی می‌سازد. در محیط عملیاتی با
`--dart-define=DEV_AUTH=false` ساخته و به ارائه‌دهنده OIDC متصل شود.
