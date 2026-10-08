# Runbookها

| Runbook | هشدار مرتبط (infra/observability/alerts.yml) |
|---|---|
| [قطع Kafka](kafka-outage.md) | OutboxBacklogGrowing, OutboxDeadLetters |
| [خرابی منطقه اصلی](region-failure.md) | synthetic probe / ApiHighErrorRate |
| [اختلال اعلان](notification-disruption.md) | NotificationUnknownStates, NotificationFailures |
| [بازیابی پایگاه داده](database-restore.md) | — (تمرین دوره‌ای AT-09) |
| [سیل گزارش جعلی](report-flood.md) | ReportIntakeRejectedSpike, ReviewQueueBacklog |

در هر رخداد: زمان شروع/پایان، اثر بر کاربر، داده از دست‌رفته، correlation IDهای کلیدی و اقدام اصلاحی ثبت شود.
