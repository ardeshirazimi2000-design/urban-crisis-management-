# Runbook: قطع Kafka (طرح تفصیلی §۱۷.۱)

**نشانه:** `crisis_outbox_oldest_pending_seconds` رو به افزایش، لاگ relay با `outbox relay iteration failed`.
**اثر:** ثبت گزارش و تصمیم‌ها ادامه دارد (outbox)؛ AI و مصرف‌کننده‌ها داده جدید نمی‌گیرند.

1. سلامت broker، فضای دیسک و ISR را بررسی کنید (`kafka-topics.sh --describe`).
2. ظرفیت DB را پایش کنید: `SELECT count(*) FROM outbox_events WHERE published_at IS NULL;` — رشد خطی انتظار می‌رود.
3. retry storm نیست: relay با backoff نمایی کار می‌کند؛ relay را restart نکنید مگر hang شده باشد.
4. در صورت فشار، پردازش غیرحیاتی (ai-worker) را موقتاً scale-down کنید؛ ثبت گزارش را متوقف نکنید.
5. پس از بازیابی: انتشار خودکار و به‌ترتیب per-aggregate انجام می‌شود؛ lag مصرف‌کننده‌ها و DLQ را پایش کنید.
6. رویدادهای `dead`: `GET /api/v1/admin/outbox?state=dead` → علت را رفع → `POST /api/v1/admin/outbox/replay` با دلیل.
7. تمرین: `tests/disaster-drills/kafka-outage.sh` (زمان تخلیه backlog را به‌عنوان زمان بازیابی ثبت کنید).
