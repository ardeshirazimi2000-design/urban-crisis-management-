# ردیابی آزمون‌های پذیرش (طرح تفصیلی §۱۴)

| شناسه | سناریو | آزمون خودکار | وضعیت |
|---|---|---|---|
| AT-01 | گزارش معتبر، پاسخ پس از commit، انتشار نهایی | `services/core/internal/itest` → `TestAT01_ReportAcceptedAndEventPublished` | ✅ خودکار |
| AT-02 | درخواست تکراری با همان key (ترتیبی و هم‌زمان) | `TestAT02_IdempotentReportCreation` | ✅ خودکار |
| AT-03 | Kafka قطع هنگام ثبت، ترتیب per-aggregate، DLQ و replay | `TestAT03_BrokerOutageAndRecovery`، `TestKafkaRoundTrip` (broker واقعی)، `tests/disaster-drills/kafka-outage.sh` | ✅ خودکار + تمرین |
| AT-04 | AI خاموش؛ بررسی دستی ادامه دارد؛ امتیاز دیرهنگام یک‌بار | `TestAT04_AIOutageDoesNotBlockReview` | ✅ خودکار |
| AT-05 | کاربر خارج از سازمان؛ ممنوع و ممیزی‌شده | `TestAT05_CrossOrganizationAccessDenied` | ✅ خودکار |
| AT-06 | تخصیص هم‌زمان منبع | `TestAT06_ConcurrentAllocation` (۲۰ درخواست موازی → دقیقاً ۱ موفق) | ✅ خودکار |
| AT-07 | هشدار بدون تأیید؛ چهارچشمی؛ dispatch idempotent | `TestAT07_AlertWorkflow` | ✅ خودکار |
| AT-08 | timeout ارائه‌دهنده → unknown/reconcile بدون ارسال کور | `TestAT08_ProviderTimeoutReconciliation` | ✅ خودکار |
| AT-09 | restore backup و اندازه‌گیری RTO/RPO | `tests/disaster-drills/backup-restore.sh` | ✅ اسکریپت (در محیط محلی اجرا و PASS شد؛ در staging دوره‌ای) |
| AT-10 | مکان نامعتبر → خطای ساخت‌یافته | `TestAT10_InvalidLocation` | ✅ خودکار |
| AT-11 | بار اوج توافق‌شده | `tests/load/report-intake.js` (k6) | ⏳ آماده؛ آستانه پس از تصویب D-08 |
| AT-12 | قطع شبکه اپ امدادگر؛ ثبت محلی رمزنگاری‌شده و sync idempotent | `apps/mobile-shared/test/offline_queue_test.dart`، `apps/mobile-responder/test/missions_test.dart` | ✅ خودکار (منطق)؛ آزمون میدانی روی دستگاه لازم است |

آزمون‌های تکمیلی: حریم خصوصی موقعیت، زنجیره ممیزی، چرخه حادثه، رسانه، GIS، مدیریت نقش، سلامت، قرارداد OpenAPI
(`TestOpenAPICoversRoutes`)، وجود schema برای هر رویداد (`TestEventSchemasExist`) و اعتبار رویداد AI در برابر schema
(`services/ai-assist/tests/test_contracts.py`).
