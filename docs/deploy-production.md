# استقرار عملیاتی (پروفایل حفاظتی افتا)

در این حالت:
- ورود کنسول با نام کاربری، رمز عبور و کد پیامکی انجام می‌شود.
- ورود اپ‌ها با شاهکار و کد پیامکی است.
- همه ارتباط‌ها از HTTPS عبور می‌کنند.
- کدهای دسترسی آزمایشی وجود ندارد.

انطباق بند به بند: [afta-compliance.md](afta-compliance.md).

## پیش‌نیازها
- Ubuntu Server 24.04 با NTP فعال (`timedatectl set-ntp true`).
- دامنه با رکورد A به IP عمومی و گواهی TLS، مثلاً Let's Encrypt.
- nginx روی میزبان برای HTTPS.
- کلید api.ir با دسترسی به `Shahkar` (یا `ShahkarPro`)، `SmsOTP` و `SendSms`.

## نصب یا تبدیل نصب آزمایشی به عملیاتی

کلید را بدون ثبت در تاریخچه shell وارد کنید:

```bash
read -rs APIIR_TOKEN && export APIIR_TOKEN
curl -fsSL https://raw.githubusercontent.com/ardeshirazimi2000-design/urban-crisis-management-/claude/afta-security-profile/deploy/install-lan.sh -o /tmp/install.sh
sudo --preserve-env=APIIR_TOKEN MODE=production BRANCH=claude/afta-security-profile APIIR_SHAHKAR_ENDPOINT=Shahkar bash /tmp/install.sh
```

نصب‌کننده این کارها را انجام می‌دهد:
- کلیدهای `JWT_SECRET`، `OTP_PEPPER`، `AUDIT_KEY` و `INTEGRITY_KEY` را تصادفی می‌سازد. در اجرای بعدی همان‌ها حفظ می‌شوند.
- `.env` را با مجوز 600 می‌نویسد.
- `APP_ENV=production`، `AUTH_MODE=local`، `REQUIRE_HTTPS=true` و `TRUSTED_PROXY_HOPS=2` را تنظیم می‌کند.
- کنسول را فقط روی `127.0.0.1:8081` در دسترس می‌گذارد.
- حوادث و هشدارهای موجود را مهر صحت می‌زند (`-seal-existing`).
- داده‌های موجود و پشتیبان روزانه را حفظ می‌کند.

## HTTPS روی میزبان
```bash
sudo cp /opt/urban-crisis/deploy/nginx-crisis.conf /etc/nginx/sites-available/crisis
sudo nano /etc/nginx/sites-available/crisis      # server_name و مسیر گواهی
sudo ln -sf /etc/nginx/sites-available/crisis /etc/nginx/sites-enabled/crisis
sudo nginx -t && sudo systemctl reload nginx
```

فقط TLS 1.2 با مجموعه رمزهای پروفایل پذیرفته می‌شود. برای آزمون:

```bash
openssl s_client -connect <دامنه>:443 -tls1_1 </dev/null
```

این دستور باید رد شود.

## اولین مدیر امنیت
```bash
cd /opt/urban-crisis
sudo docker compose run --rm \
  -e BOOTSTRAP_ADMIN_NATIONAL_CODE=کد_ملی -e BOOTSTRAP_ADMIN_MOBILE=09xxxxxxxxx \
  -e BOOTSTRAP_ADMIN_USERNAME=secadmin -e BOOTSTRAP_ADMIN_FIRST_NAME=نام -e BOOTSTRAP_ADMIN_LAST_NAME=نام‌خانوادگی \
  migrate -bootstrap-admin
```

- موبایل باید به نام همان کد ملی باشد (استعلام شاهکار).
- رمز موقت فقط یک‌بار چاپ می‌شود و در اولین ورود باید تغییر کند.

پس از ورود:
1. مدیر امنیت یک **ممیز امنیتی** تعریف کند: کنسول ← کاربران و نقش‌ها ← تعریف کاربر ← نقش «ممیز امنیتی».
2. مدیر امنیت تنظیمات امنیتی را بازبینی کند: کنسول ← امنیت و ممیزی. مهم‌ترین موارد:
   - `access.consoleCidrs`، مثلاً `["192.168.1.0/24"]`؛
   - ساعت و روزهای مجاز؛
   - آستانه قفل.
3. ممیز تنظیمات `audit.*` را بازبینی کند و یک بار «بررسی صحت» را بزند.
4. سایر کاربران را تعریف کنید. امدادگران «دسترسی اپلیکیشن» و نقش «امدادگر» بگیرند.

## اپ‌ها
```bash
cd apps/mobile-citizen   && flutter build apk --release --dart-define=API_BASE_URL=https://<دامنه>/api/v1
cd apps/mobile-responder && flutter build apk --release --dart-define=API_BASE_URL=https://<دامنه>/api/v1
```

- `DEV_AUTH` و `ALLOW_CLEARTEXT` را در نسخه عملیاتی **به کار نبرید**.
- برای انتشار، APK را با کلید امضای سازمان امضا کنید.

## مهاجرت از سامانه `crisis-management` (مخزن kx21) روی همان سرور
1. پیش از هر کاری از هر دو پایگاه داده پشتیبان بگیرید:
   ```bash
   cd /opt/urban-crisis && sudo docker compose exec -T postgres pg_dump -U crisis -Fc crisis > ~/urban-before-afta.dump
   sudo -u postgres pg_dump -p 5433 -Fc crisis > ~/kx21-crisis.dump
   ```
2. نصب عملیاتی بالا را اجرا کنید. پس از آن، پورت 80 سرور دیگر در اختیار Docker نیست و کنسول روی `127.0.0.1:8081` است.
3. در فایل nginx دامنه، مقصد `proxy_pass` را از `http://127.0.0.1:4000` به `http://127.0.0.1:8081` تغییر دهید. این دو سرآیند را هم اضافه کنید:
   - `X-Forwarded-For $proxy_add_x_forwarded_for`
   - `X-Forwarded-Proto https`

   نمونه کامل در `deploy/nginx-crisis.conf` است. سپس:
   ```bash
   sudo nginx -t && sudo systemctl reload nginx
   ```
4. اولین مدیر امنیت را بسازید (بالا) و کاربران را دوباره تعریف کنید. کاربران سامانه قبلی منتقل نمی‌شوند؛ هر کاربر باید دوباره با شاهکار تعریف شود.
5. پس از اطمینان از کارکرد، سرویس قبلی را متوقف کنید:
   ```bash
   sudo systemctl disable --now crisis-management
   ```

## اعلان‌ها: Push داخلی و پیامک

**Push داخلی**
- به گوگل یا سرویس شخص ثالث وابسته نیست.
- هر کاربر اپ پس از ورود یک «موضوع» تصادفی و غیرقابل‌حدس می‌گیرد (`POST /me/push`).
- سرویس پس‌زمینه اپ اندروید همین نشانی را با long-poll می‌خواند: `GET /api/v1/push/poll` با سرآیند `X-Push-Topic`.
- این سرویس اعلان‌ها را حتی وقتی اپ بسته است نشان می‌دهد. یک اعلان کم‌اهمیت دائمی («آماده دریافت…») روی گوشی می‌ماند.
- پیام‌ها ۴۸ ساعت نگه داشته می‌شوند.

**هشدار عمومی**
- کانال‌های «Push» و «پیامک» به کاربرانی می‌رسند که در ۷۲ ساعت گذشته داخل محدوده هشدار بوده‌اند.
- موقعیت کاربر هنگام دریافت هشدارها در اپ، با دقت حدود ۱۰۰ متر، ثبت می‌شود و پس از ۷۲ ساعت پاک می‌شود.
- هیچ‌کس یک هشدار را دو بار دریافت نمی‌کند (`alert_recipients`).
- پیامک از api.ir و در دسته‌های ۱۰۰تایی ارسال می‌شود. سقف هر هشدار `ALERT_SMS_MAX` است (پیش‌فرض ۲۰۰۰).

**هشدار آزمایشی**
- فقط به شماره‌های `ALERT_TEST_MOBILES` می‌رسد، آن هم اگر صاحب شماره داخل محدوده باشد.
- مثال: `ALERT_TEST_MOBILES=09121234567,09351234567`

**مأموریت**
- با تخصیص یا لغو مأموریت، برای امدادگر Push و پیامک ارسال می‌شود.
- پیامک‌ها از صف `sms_outbox` و با تلاش مجدد ارسال می‌شوند.

**تنظیم‌ها در `/opt/urban-crisis/.env`**
- `ALERT_SMS_ENABLED`: ارسال پیامک هشدار (پیش‌فرض true)
- `ALERT_SMS_MAX`: سقف پیامک هر هشدار
- `ALERT_TEST_MOBILES`: شماره‌های دریافت‌کننده هشدار آزمایشی
- `MISSION_SMS_ENABLED`: پیامک مأموریت (پیش‌فرض true)

پس از تغییر این تنظیم‌ها، `sudo docker compose up -d` را اجرا کنید.

**گوشی‌ها**
- بعضی گوشی‌ها (مثلاً شیائومی و هواوی) سرویس پس‌زمینه را می‌بندند. در تنظیمات باتری گوشی برای «همراه پاد» گزینه «بدون محدودیت» را انتخاب کنید.
- در اندروید ۱۳ و بالاتر، اجازه نمایش اعلان را هنگام اولین ورود بدهید.

## به‌روزرسانی
```bash
sudo MODE=production BRANCH=claude/afta-security-profile bash /opt/urban-crisis/deploy/install-lan.sh
```
کلیدها، `APIIR_TOKEN` و داده‌ها حفظ می‌شوند.

## نگهداری کلیدها (FCS_CKM.4)
- فایل `/opt/urban-crisis/.env` فقط برای root خواندنی است. یک نسخه از آن را در محل امن بیرون از سرور نگه دارید.
- **چرخش کلید api.ir:**
  1. کلید جدید را در `.env` بگذارید.
  2. `sudo docker compose up -d` را اجرا کنید.
  3. کلید قدیمی را در پنل api.ir باطل کنید.
- **چرخش `JWT_SECRET` یا `OTP_PEPPER`:** همه نشست‌ها و توکن‌های دستگاه باطل می‌شوند و کاربران دوباره وارد می‌شوند.
- **چرخش `AUDIT_KEY`:** پیش از آن، ممیزی را خروجی بگیرید و بایگانی کنید. رکوردهای قبلی با کلید قدیمی قابل بررسی‌اند.
- **چرخش `INTEGRITY_KEY`:** پس از تغییر، `docker compose run --rm migrate -seal-existing` را اجرا کنید. این دستور فقط رکوردهای بدون مهر را مهر می‌زند. برای مهر مجدد همه رکوردها، ابتدا جدول `record_seals` را با تأیید ممیز خالی کنید.
- نسخه‌های قدیمی کلیدها را از همه پشتیبان‌ها با `shred` پاک کنید.

## محدودیت‌های شناخته‌شده
- **رسانه‌ها:** تا اتصال اسکنر بدافزار (از دروازه‌های پیش از پایلوت)، رسانه‌های بارگذاری‌شده در حالت عملیاتی در انتظار اسکن می‌مانند و نمایش داده نمی‌شوند.
- **کانال‌های پیامک و Push:** کانال‌های عمومی هشدار همچنان sandbox هستند و قرارداد ارائه‌دهنده لازم است (D-05). پیامک‌های امنیتی و OTP از api.ir واقعی ارسال می‌شوند.
