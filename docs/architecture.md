# معماری پیاده‌سازی‌شده — پلتفرم مدیریت بحران شهری (MVP)

این سند توضیح می‌دهد طرح‌های [سند معماری](design/urban_crisis_platform_technical_design_fa.docx) و
[طرح تفصیلی](design/urban_crisis_platform_detailed_technical_design_fa.docx) چگونه در کد پیاده شده‌اند.

## ۱. نمای کلی

```mermaid
flowchart LR
  Citizen[اپ شهروند Flutter] --> GW
  Responder[اپ امدادگر Flutter] --> GW
  Console[کنسول وب React] --> GW
  GW[Ingress / WAF / Rate limit] --> API
  subgraph Core["services/core (Go، ماژولار)"]
    API[REST API /api/v1] --> Mods[Report · Media · Incident · Resource · Alert · GIS · Identity/Audit]
    Mods --> DB[(PostgreSQL + PostGIS)]
    Mods --> Outbox[(outbox_events)]
    Relay[relay] --> Outbox
    Notifier[notifier] --> DB
    Consumer[consumer + inbox] --> DB
  end
  Mods --> OS[(Object Storage خصوصی)]
  Relay --> Kafka[(Kafka)]
  Kafka --> AI[ai-assist worker (Python) — غیرمرجع]
  AI --> Kafka
  Kafka --> Consumer
  Notifier --> Adapters[آداپتور کانال: داخلی، Push/SMS sandbox، Cell Broadcast (غیرفعال)]
```

مطابق ADR-001 یک deployable اصلی با مرز ماژول مشخص ساخته شده و فقط کارگرها (relay، notifier، consumer) و
هوش مصنوعی جدا اجرا می‌شوند. هر ماژول مالک جداول خود است و از طریق تابع صریح (مثل `report.MarkLinked`) با ماژول
دیگر تعامل می‌کند؛ استخراج سرویس مستقل در آینده بدون تغییر قرارداد ممکن است.

| ماژول | مسیر کد | مالک داده | نباید انجام دهد |
|---|---|---|---|
| Identity/Auth | `internal/auth`, `internal/admin` | users, user_roles, organizations | تفسیر سیاست بحران |
| Report | `internal/report` | reports, report_events, report_ai_scores | صدور هشدار |
| Media | `internal/media` | report_media + object storage | URL عمومی دائمی |
| Incident | `internal/incident` | incidents, incident_reports, incident_events | تولید داده مکانی خام |
| Resource | `internal/resource` | resources, assignments | اعلان عمومی |
| Alert | `internal/alert` | alerts | تضمین تحویل اپراتور |
| Notification | `internal/notification` | notification_attempts | تصمیم محتوایی |
| GIS | `internal/gis` | gis_features, impact_areas | تصمیم فرماندهی |
| AI Assist | `services/ai-assist` | — (رویداد report.scored) | رد/تأیید خودکار گزارش |
| Audit | `internal/audit` | audit_log (زنجیره هش) | — |

## ۲. جریان گزارش (مطابق §۴.۲)

1. `POST /api/v1/reports` با `Idempotency-Key` → اعتبارسنجی (نوع مجاز، طول متن، مختصات در محدوده خدمت، دقت، زمان ادعایی).
2. در **یک تراکنش**: ثبت گزارش با شناسه سمت سرور، اتصال رسانه‌های متعلق به همان کاربر، رویداد تاریخچه، رکورد outbox و کلید idempotency.
3. پاسخ `202` فقط پس از commit (پذیرش پایدار؛ نه تأیید). رویداد بعداً توسط `relay` با کلید `aggregate_id` به Kafka می‌رود.
4. `ai-assist` رویداد را مصرف و `report.scored.v1` را با شناسه قطعی (uuid5) تولید می‌کند؛ `consumer` با inbox آن را دقیقاً یک‌بار اعمال می‌کند و فقط وضعیت `received→triage` را تغییر می‌دهد.
5. اپراتور گزارش را برمی‌دارد، تصمیم با دلیل ثبت می‌شود (قفل خوش‌بینانه `version`)، و گزارش تأییدشده به حادثه پیوند می‌خورد.

## ۳. الگوهای قابلیت اطمینان

| نیاز سند | پیاده‌سازی |
|---|---|
| Transactional Outbox | `internal/outbox`؛ ترتیب per-aggregate با `seq` و شرط «هیچ رویداد قبلی معلق»، `FOR UPDATE SKIP LOCKED` برای چند relay |
| retry + backoff + jitter | `outbox.Backoff` (نمایی با full jitter، سقف ۵ دقیقه)؛ برای relay، consumer و اعلان |
| DLQ و replay | outbox: `dead_lettered_at` + `POST /admin/outbox/replay` (مجوز، سقف ۱۰۰۰، rate limit، ممیزی)؛ Kafka: `<topic>.dlq` |
| مصرف idempotent | `internal/inbox.Process` (inbox + اثر در یک تراکنش) |
| Idempotency-Key | `internal/idempotency` — پاسخ ذخیره و بازپخش؛ کلید تکراری با بدنه متفاوت → 422 |
| تخصیص اتمیک | `SELECT … FOR UPDATE` + ایندکس یکتای جزئی `assignments_one_active_per_resource` |
| وضعیت نامشخص ارسال | `unknown` → reconcile با `Query` ارائه‌دهنده؛ فقط `not_found` مجوز ارسال مجدد است |
| live/ready جدا | `/health/live` فقط فرایند؛ `/health/ready` فقط DB و draining (Kafka/AI وابستگی readiness نیستند) |
| graceful shutdown | readiness ابتدا شکست می‌خورد، سپس drain و `Shutdown` |

## ۴. امنیت (خلاصه؛ جزئیات در [threat-model.md](threat-model.md))

- احراز هویت OIDC (JWKS، بررسی iss/aud/exp، چرخش کلید)؛ حالت `dev` فقط با `APP_ENV=local|test` قابل فعال‌شدن است.
- مجوزها از پایگاه داده (نه توکن)؛ RBAC صریح بدون `*`؛ دامنه سازمانی برای حادثه، منبع و هشدار؛ deny-by-default؛ هر رد دسترسی ممیزی می‌شود.
- تفکیک وظایف: فرمانده مدیریت کاربر/نقش ندارد؛ مدیر امنیت اختیار عملیاتی ندارد؛ هیچ‌کس به خودش نقش نمی‌دهد؛ تأیید هشدار چهارچشمی.
- break-glass: حداکثر ۸ ساعت، دلیل الزامی، رویداد ممیزی جداگانه.
- حریم خصوصی موقعیت: مختصات دقیق فقط با `report:read_precise` یا برای خود گزارش‌دهنده؛ بقیه ~۱.۱ کیلومتر.
- لاگ ساخت‌یافته با ماسک توکن، تلفن، مختصات و متن؛ لاگ دسترسی فقط الگوی مسیر (نه URL خام).
- لاگ ممیزی فقط‌افزودنی (trigger) و زنجیره هش قابل راستی‌آزمایی (`GET /admin/audit/verify`).

## ۵. حالت تنزل‌یافته (§۱۰)

| خرابی | رفتار پیاده‌شده |
|---|---|
| Kafka | ثبت گزارش ادامه دارد (outbox)؛ انتشار پس از بازیابی به‌ترتیب |
| AI | گزارش در `received` با `enrichment_status=pending` می‌ماند؛ بررسی دستی بدون مانع |
| ارائه‌دهنده اعلان | `unknown` + تطبیق؛ کانال‌ها مستقل؛ Cell Broadcast بدون قرارداد رد می‌شود |
| شبکه موبایل | صف رمزنگاری‌شده روی دستگاه، همگام‌سازی به‌ترتیب و idempotent، تعارض با منطق server-authoritative |
| نقشه آنلاین | آدرس کاشی قابل پیکربندی (`VITE_TILE_URL`) برای سرور کاشی آفلاین مجاز؛ داده کهنه با خط‌چین و برچسب |

## ۶. خارج از دامنه این پیاده‌سازی (مطابق §۲.۲)

ارسال واقعی SMS/Push/Cell Broadcast (فقط sandbox)، تصمیم خودکار AI، Digital Twin، active-active چندمنطقه‌ای،
اتصال به سامانه‌های اضطراری قدیمی، اسکنر بدافزار واقعی (رابط `media.Scanner` آماده است). تصمیم‌های باز در
[open-decisions.md](open-decisions.md).

## نیازهای باز و ظرفیت اسکان (ماژول `relief`)
- **نیاز باز**: آنچه یک حادثه کم دارد (تیم، خودرو، پتو، آب، جای اسکان، …) با مقدار، واحد و اولویت. ثبت و لغو با `need:create`
  (اپراتور/فرمانده سازمان مالک حادثه)، تأمین با `need:fulfill` (مدیر منابع/فرمانده). وضعیت از مقدار تأمین‌شده مشتق می‌شود:
  `open` → `partially_met` → `met`؛ لغو فقط با دلیل.
- هر تأمین یک رکورد append-only در `need_fulfillments` با «تأمین‌کننده» الزامی است. تأمین بیش از باقی‌مانده رد می‌شود و
  `version` نیاز مانع ثبت دوباره یک تحویل در تلاش مجدد می‌شود.
- **اسکان**: محل اسکان همان منبع نوع `shelter` است (ظرفیت در `resources.capacity`). اشغال فعلی در `shelter_occupancy` و هر
  پذیرش/خروج/تغییر تنظیمات در `shelter_occupancy_log` (append-only) ثبت می‌شود. پذیرش بیش از ظرفیت (`SHELTER_CAPACITY_EXCEEDED`)،
  پذیرش در محل بسته (`SHELTER_NOT_ACCEPTING`) و کاهش ظرفیت به زیر اشغال فعلی رد می‌شود. ثبت جابه‌جایی با `shelter:update`،
  تغییر ظرفیت و بستن/باز کردن پذیرش با `resource:manage` و دلیل.
- دامنه سازمانی: نیاز تابع سازمان مالک حادثه و محل اسکان تابع سازمان مالک منبع است (deny-by-default).
- رویدادها: `need.created`، `need.status_changed`، `shelter.occupancy_changed` (فقط شمارش، بدون داده شخصی).
- خارج از این نسخه: ثبت فردی اسکان‌یافتگان (داده شخصی؛ نیازمند سیاست نگهداشت D-04)، نمایش محل‌های اسکان باز به شهروند، موجودی انبار.
