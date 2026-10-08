# پلتفرم مدیریت بحران شهری (MVP)

پیاده‌سازی مرجع بر اساس [سند معماری و طراحی فنی](docs/design/urban_crisis_platform_technical_design_fa.docx) و
[طرح تفصیلی فنی و اجرایی](docs/design/urban_crisis_platform_detailed_technical_design_fa.docx) — سامانه چندسازمانی
دریافت گزارش، مدیریت حادثه، منابع، GIS و هشدار عمومی با سناریوی زلزله تهران.

> **اصل حیاتی:** هوش مصنوعی و امتیازها فقط کمک‌تصمیم‌اند. هیچ هشدار عمومی بدون تأیید انسانی مجاز (چهارچشمی)،
> قالب نسخه‌دار، محدوده، انقضا و ثبت ممیزی ارسال نمی‌شود. این سامانه جایگزین دستورالعمل رسمی مدیریت بحران،
> مجوزهای مخابراتی یا تماس اضطراری (۱۱۵/۱۲۵/۱۱۰) نیست.

## اجزا

| مسیر | فناوری | نقش |
|---|---|---|
| [`services/core`](services/core) | Go 1.24، PostgreSQL 16 + PostGIS، Kafka | API ماژولار (گزارش، رسانه، حادثه، منابع، هشدار، GIS، هویت/ممیزی) + کارگرهای `relay`، `notifier`، `consumer` |
| [`services/ai-assist`](services/ai-assist) | Python، FastAPI، aiokafka | طبقه‌بندی/فوریت کمکی فارسی — غیرمرجع (ADR-004) |
| [`apps/web-console`](apps/web-console) | React + TypeScript، Leaflet | کنسول فارسی/RTL برای اپراتور، فرمانده، مدیر منابع، GIS و امنیت |
| [`apps/mobile-citizen`](apps/mobile-citizen) | Flutter | ثبت گزارش آفلاین، هشدارهای رسمی محدوده |
| [`apps/mobile-responder`](apps/mobile-responder) | Flutter | مأموریت‌ها offline-first، ارسال وضعیت و موقعیت |
| [`apps/mobile-shared`](apps/mobile-shared) | Dart | کلاینت API و صف آفلاین رمزنگاری‌شده با همگام‌سازی idempotent |
| [`contracts`](contracts) | OpenAPI 3.1، JSON Schema | قرارداد REST و رویدادهای نسخه‌دار (کنترل در CI) |
| [`infra`](infra) | Kubernetes، Prometheus | manifestهای سخت‌سازی‌شده، قواعد هشدار پایش |
| [`tests`](tests) | k6، bash | آزمون بار و تمرین‌های بازیابی |
| [`docs`](docs) | — | [معماری](docs/architecture.md)، [ADRها](docs/adr/README.md)، [Runbookها](docs/runbooks/README.md)، [مدل تهدید](docs/threat-model.md)، [آزمون‌های پذیرش](docs/acceptance-tests.md)، [تصمیم‌های باز](docs/open-decisions.md) |

## اجرای سریع (Docker)

```bash
cp .env.example .env        # فقط مقادیر محلی؛ هرگز commit نشود
docker compose up --build
```

- کنسول: http://localhost:8081 — ورود توسعه با نقش‌های نمونه (اپراتور، فرمانده، مدیر منابع، GIS، امنیت)
- API: http://localhost:8080/api/v1 — سلامت: `/health/live`، `/health/ready`، متریک: `/metrics`
- داده نمونه (سازمان‌ها، مراکز درمانی، منابع) **ساختگی** است و در `services/core/seeds/dev.sql` قرار دارد.

## اجرای بدون Docker

```bash
# پیش‌نیاز: PostgreSQL + PostGIS
export APP_ENV=local DATABASE_URL=postgres://crisis:local-only@localhost:5432/crisis?sslmode=disable \
       DEV_JWT_SECRET=change-me-local-dev-secret-at-least-32-chars \
       MEDIA_URL_SECRET=change-me-local-media-secret-at-least-32-chars MEDIA_ALLOW_UNSCANNED=true
make seed       # migration + داده نمونه
make run-api    # API + relay/notifier درون‌فرایندی (بدون Kafka، رویدادها لاگ می‌شوند)
cd apps/web-console && npm ci && npm run dev    # http://localhost:5173
```

## آزمون‌ها

```bash
make test       # Go (واحد + پذیرش AT-01..AT-10 + قرارداد)، Python، وب، Flutter
```

آزمون‌های یکپارچه Go با `TEST_DATABASE_URL` (پایگاه داده‌ای که **پاک‌سازی می‌شود**) و در صورت وجود
`TEST_KAFKA_BROKERS` با Kafka واقعی اجرا می‌شوند. نگاشت کامل در [docs/acceptance-tests.md](docs/acceptance-tests.md).

## گردش کار اصلی

1. **شهروند** گزارش را با موقعیت و دقت ثبت می‌کند (آفلاین: صف رمزنگاری‌شده روی دستگاه). پاسخ `202` یعنی ثبت پایدار، نه تأیید.
2. رویداد `report.created` از outbox به Kafka می‌رود؛ **AI** سیگنال کمکی `report.scored` تولید می‌کند.
3. **اپراتور** در صف بررسی گزارش‌های نزدیک/تکراری را می‌بیند و با دلیل تأیید، رد یا تکراری اعلام می‌کند.
4. گزارش تأییدشده به **حادثه** تبدیل یا پیوند می‌شود؛ فعال‌سازی، تشدید، بستن و بازگشایی فقط با تأیید **فرمانده**.
5. **منابع** به‌صورت اتمیک تخصیص می‌یابند؛ **امدادگر** وضعیت مأموریت را (حتی آفلاین) ثبت می‌کند.
6. **هشدار**: پیش‌نویس با قالب نسخه‌دار و محدوده → پیش‌نمایش دقیق → تأیید فردی دیگر → ارسال idempotent → وضعیت هر کانال
   (پذیرش ارائه‌دهنده ≠ تحویل). کانال‌های عمومی در MVP sandbox هستند و Cell Broadcast بدون قرارداد رد می‌شود.
7. همه تصمیم‌ها با actor، دلیل و correlation ID در **لاگ ممیزی زنجیره‌ای** ثبت می‌شوند.

## پیش از پایلوت (دروازه‌های §۱۸)

موارد زیر خارج از کد و نیازمند تصمیم/اقدام سازمانی است: مرجع رسمی صدور هشدار (D-01)، اتصال IdP سازمانی با MFA،
اسکنر بدافزار رسانه، قرارداد و آزمون ارائه‌دهندگان پیام (D-05)، سیاست نگهداری داده (D-04)، بار مبنا (D-08)، مجوز
نقشه پایه و کاشی آفلاین (D-09)، مدل DR (D-07)، و تمرین‌های tabletop/میدانی. جزئیات: [docs/open-decisions.md](docs/open-decisions.md).
