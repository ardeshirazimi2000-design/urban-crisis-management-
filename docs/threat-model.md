# مدل تهدید و کنترل‌ها (طرح تفصیلی §۸)

| تهدید | کنترل پیشگیرانه (پیاده‌شده) | کنترل تشخیصی/پاسخ | محل در کد |
|---|---|---|---|
| جعل گزارش / سیل درخواست | rate limit به‌ازای هویت + Ingress، اعتبارسنجی سخت (allowlist نوع، محدوده خدمت، طول، `DisallowUnknownFields`)، سقف حجم بدنه | متریک 429/422، هشدار `ReportIntakeRejectedSpike`، تعلیق کاربر با دلیل | `report/domain.go`, `platform/httpx` |
| سرقت حساب ممتاز | OIDC با MFA (در IdP)، توکن کوتاه‌عمر، بررسی iss/aud/exp، تعلیق کاربر | ممیزی `role.*`، `user.status`، رد دسترسی‌ها | `auth/token.go`, `admin` |
| دسترسی بین‌سازمانی | RBAC + دامنه سازمانی + deny-by-default؛ پاسخ 404 برای منابع پنهان | هر رد دسترسی با `outcome=denied` ممیزی می‌شود (AT-05) | `auth/permissions.go`, `Guard` |
| ارتقای اختیار | فرمانده بدون `user:manage`؛ عدم اعطای نقش به خود؛ break-glass ≤ ۸ ساعت با دلیل | رویداد `role.break_glass_grant` | `admin/handler.go` |
| مسموم‌سازی رسانه | تشخیص MIME واقعی و تطابق با نوع اعلامی، allowlist، سقف حجم، checksum، رابط اسکنر، storage خصوصی، URL امضاشده ۵ دقیقه‌ای | `scan_status`، ممیزی صدور URL | `media` |
| تزریق / داده مخرب | پرس‌وجوی پارامتری (pgx)، اعتبارسنجی GeoJSON با PostGIS، escaping خودکار React | SAST در CI | همه ماژول‌ها |
| افشای موقعیت | مختصات دقیق فقط با `report:read_precise`؛ تقریب ~۱.۱km؛ ماسک مختصات و متن در لاگ؛ payload رویداد بدون هویت گزارش‌دهنده | ممیزی دسترسی رسانه | `report/handler.go`, `logx` |
| دستکاری هشدار | گردش کار تأیید چهارچشمی، قالب نسخه‌دار، متن رندرشده ثابت، dispatch idempotent، انقضای الزامی | dispatch بدون تأیید = رویداد امنیتی (AT-07) | `alert` |
| دستکاری ممیزی | جدول فقط‌افزودنی (trigger) + زنجیره هش | `GET /admin/audit/verify` | `audit` |
| سرقت secret | اسرار فقط از env/Vault؛ `.env` در gitignore؛ dev auth در تولید رد می‌شود | gitleaks در CI | `config`, `.github/workflows` |
| خرابی/باج‌افزار | backup رمزگذاری‌شده و immutable، least privilege، NetworkPolicy default-deny | تمرین restore (AT-09) | `infra/k8s`, `tests/disaster-drills` |
| دستکاری داده آفلاین موبایل | AES-256-GCM با کلید در Keystore/Keychain؛ تشخیص دستکاری (MAC) | خطای بارگذاری صف | `apps/mobile-shared` |

## ریسک‌های باقی‌مانده (پیش از پایلوت)
- اسکنر بدافزار واقعی (ClamAV یا معادل) متصل نشده است.
- MFA و سیاست نشست در IdP سازمان پیکربندی شود؛ API فقط توکن معتبر را می‌پذیرد.
- mTLS بین سرویس‌ها و Kafka ACL به ازای topic در زیرساخت تعریف شود.
- لنگر خارجی (WORM) برای هش ممیزی جهت مقابله با بازنویسی کامل پایگاه داده توسط DBA.
