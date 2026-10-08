# Runbook: بازیابی پایگاه داده (AT-09)

تولید: backup فیزیکی رمزگذاری‌شده + آرشیو WAL (PITR) در storage غیرقابل‌حذف (object lock). تمرین ماهانه:

1. `tests/disaster-drills/backup-restore.sh` (یا بازیابی PITR در staging) را اجرا کنید.
2. مدت بازیابی (RTO) و فاصله آخرین تراکنش بازیابی‌شده تا لحظه خرابی (RPO) را ثبت و با هدف ۱۵ دقیقه / ۱ دقیقه مقایسه کنید.
3. پس از بازیابی: `GET /api/v1/admin/audit/verify` باید `valid=true` بدهد؛ شمار outbox معلق را بررسی کنید.
4. رسانه‌ها (object storage) و اسرار جداگانه بازیابی و تطبیق داده شوند.
