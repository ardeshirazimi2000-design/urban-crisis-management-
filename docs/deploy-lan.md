# راه‌اندازی سرور آزمایشی در شبکه دفتر (LAN)

پیش‌نیاز: Ubuntu Server 24.04، حداقل ۸ گیگابایت RAM و ۳۰ گیگابایت فضای خالی، دسترسی اینترنت (برای دریافت کد و بسته‌ها).

```bash
curl -fsSL https://raw.githubusercontent.com/ardeshirazimi2000-design/urban-crisis-management-/claude/urban-crisis-platform-mvp/deploy/install-lan.sh -o install-lan.sh
sudo bash install-lan.sh
```

اسکریپت [`deploy/install-lan.sh`](../deploy/install-lan.sh):

1. IP سرور و محدوده شبکه را تشخیص می‌دهد (یا `SERVER_IP=...` بدهید).
2. Docker را از مخزن خود Ubuntu نصب می‌کند (نیازی به download.docker.com نیست).
3. دسترسی به Docker Hub و mirrorهای داخلی، و منابع Go/npm/pip را آزمایش و اولین مورد در دسترس را انتخاب می‌کند.
4. کد را در `/opt/urban-crisis` می‌گیرد، `.env` با رمزهای تصادفی می‌سازد (در اجرای مجدد حفظ می‌شوند).
5. ایمیج‌ها را می‌سازد، سرویس‌ها را با سیاست restart بالا می‌آورد و سلامت را بررسی می‌کند.
6. فایروال: SSH و کنسول فقط از شبکه دفتر.
7. پشتیبان روزانه ساعت ۲:۳۰ در `/var/backups/urban-crisis` (نگهداری ۱۴ روز).

به‌روزرسانی: همان دستور `sudo bash /opt/urban-crisis/deploy/install-lan.sh` (داده و رمزها حفظ می‌شوند).

## متغیرهای اختیاری
| متغیر | کاربرد |
|---|---|
| `SERVER_IP`, `LAN_CIDR` | اگر تشخیص خودکار درست نبود |
| `WEB_PORT` | پورت کنسول (پیش‌فرض 80) |
| `SERVICE_AREA` | محدوده پذیرش گزارش: `iran` (پیش‌فرض)، `tehran` یا `minLat,maxLat,minLng,maxLng` |
| `PUBLIC_HOST` | IP یا نام عمومی که روی سرور فوروارد شده (فقط برای نمایش نشانی در پایان نصب) |
| `ROTATE_ACCESS_CODES=1` | صدور کدهای دسترسی جدید (APK آزمایشی باید دوباره ساخته شود) |
| `REGISTRY_MIRRORS` | mirrorهای Docker Hub (فهرست با فاصله) |
| `NPM_CANDIDATES`, `PIP_CANDIDATES` | منابع جایگزین بسته‌ها |

## کد دسترسی آزمایشی
نصب‌کننده دو کد می‌سازد و در پایان نصب نشان می‌دهد (دوباره: `sudo grep ACCESS_CODE /opt/urban-crisis/.env`):
- **کد کارکنان**: در صفحه ورود کنسول وارد می‌شود و هر نقشی را می‌دهد. فقط به آزمایش‌کنندگان بدهید.
- **کد شهروند**: اپ شهروند در اولین اجرا آن را می‌پرسد و ذخیره می‌کند؛ فقط حساب شهروند می‌سازد و نمی‌تواند نقش کارکنان بگیرد یا حساب آن‌ها را تغییر دهد.

تلاش‌های ورود برای هر IP محدود است (۱۰ در دقیقه) و کد نادرست در لاگ API ثبت می‌شود.

## دسترسی از بیرون (اینترنت) برای آزمایش
1. روی روتر **فقط** پورت وب (80) را به IP سرور فوروارد کنید؛ هرگز SSH (22) را.
2. `sudo PUBLIC_HOST=<IP عمومی> bash /opt/urban-crisis/deploy/install-lan.sh`
3. APK شهروند را با نشانی عمومی بسازید (کد شهروند را اپ در اولین اجرا می‌پرسد؛ یا با `--dart-define=ACCESS_CODE=...` داخل آن بگذارید):
   ```bash
   ALLOW_CLEARTEXT=true flutter build apk --release \
     --dart-define=API_BASE_URL=http://<IP عمومی>/api/v1 --dart-define=DEV_AUTH=true
   ```
   اپ امدادگر هم به همین شکل ساخته می‌شود (`apps/mobile-responder`)؛ کد **کارکنان** را در صفحه ورود آن وارد کنید.
4. وقتی آزمایش نمی‌کنید فوروارد را خاموش کنید. ترافیک رمزنگاری نشده (HTTP) است؛ داده واقعی وارد نکنید.

پورت‌هایی که Docker منتشر می‌کند از ufw عبور می‌کنند؛ بنابراین محافظ اصلی همین کدهای دسترسی است.

## عیب‌یابی
```bash
cd /opt/urban-crisis
sudo docker compose ps
sudo docker compose logs --tail=100 api migrate relay
```

## محدودیت‌ها
این استقرار **آزمایشی** است: ورود توسعه‌ای که فقط با کد دسترسی محافظت می‌شود، بدون HTTPS و بدون داده واقعی. دسترسی از اینترنت فقط برای آزمایش کوتاه‌مدت.
برای استقرار عملیاتی: [infra/k8s](../infra/k8s) و دروازه‌های [open-decisions.md](open-decisions.md).
