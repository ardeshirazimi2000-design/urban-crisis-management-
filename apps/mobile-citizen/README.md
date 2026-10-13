# اپ شهروند (Flutter)

ثبت گزارش با موقعیت و دقت مکان، صف آفلاین رمزنگاری‌شده (AES-256-GCM، کلید در Keystore/Keychain)، ارسال idempotent
پس از برقراری ارتباط، و نمایش هشدارهای رسمی فعال محدوده (با صادرکننده و انقضا؛ هشدار منقضی حتی از کش نمایش داده نمی‌شود).

```bash
flutter pub get
flutter test
flutter run --dart-define=API_BASE_URL=http://10.0.2.2:8080/api/v1   # شبیه‌ساز اندروید
```

ساخت APK برای سرور آزمایشی شبکه داخلی (HTTP بدون TLS):

```bash
ALLOW_CLEARTEXT=true flutter build apk --release \
  --dart-define=API_BASE_URL=http://192.168.1.16/api/v1 --dart-define=DEV_AUTH=true
```

`ALLOW_CLEARTEXT` فقط برای سرور آزمایشی بدون HTTPS است؛ نسخه عملیاتی باید بدون آن و با آدرس `https://` ساخته شود.
APK فعلی با کلید آزمایشی (debug) امضا شده است؛ برای انتشار عمومی کلید امضای سازمانی لازم است.

**ورود:** به‌طور پیش‌فرض (`DEV_AUTH=false`) ورود سامانه با کد ملی و موبایل انجام می‌شود: در اولین ورود و ورود از
گوشی جدید، مالکیت شماره از شاهکار استعلام و سپس کد پیامکی (OTP) ارسال می‌شود؛ ورودهای بعدی از همان گوشی فقط با
کد پیامکی است (توکن دستگاه در Keystore). نشست با توکن کوتاه‌مدت و توکن تمدید چرخشی نگهداری می‌شود. سرور باید با
`AUTH_MODE=local` اجرا شود.

نسخه عملیاتی:

```bash
flutter build apk --release --dart-define=API_BASE_URL=https://crisis.example.ir/api/v1
```

`--dart-define=DEV_AUTH=true` فقط برای سرور آزمایشی (`APP_ENV=local`) است و با endpoint `/dev/token` هویت آزمایشی می‌سازد.
