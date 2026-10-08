# قراردادهای رویداد (Event Contracts)

همه رویدادها پوشش مشترک [`envelope.v1.json`](envelope.v1.json) دارند. تحویل «حداقل یک‌بار» است؛ مصرف‌کننده باید با
`event_id` تکرار را حذف کند (inbox). ترتیب فقط در محدوده هر aggregate تضمین می‌شود (کلید Kafka = `aggregate.id`).
تغییر شکننده = نسخه جدید (`.v2`) و انتشار موازی؛ تست `TestEventSchemasExist` در CI وجود schema برای هر رویداد
تولیدشده را بررسی می‌کند.

| رویداد | تولیدکننده | Topic | توضیح |
|---|---|---|---|
| `report.created` | Report | `crisis.report.created.v1` | Report intake committed (payload minimised: no reporter identity, no media). |
| `report.reviewed` | Report | `crisis.report.reviewed.v1` | Human review decision. |
| `report.scored` | AI Assist | `crisis.report.scored.v1` | Non-authoritative AI signal. Never changes review decisions. |
| `incident.created` | Incident | `crisis.incident.created.v1` | Incident formed. |
| `incident.status_changed` | Incident | `crisis.incident.status_changed.v1` | Lifecycle transition with mandatory reason. |
| `incident.assessment_changed` | Incident | `crisis.incident.assessment_changed.v1` | Severity / response level changed. |
| `incident.report_linked` | Incident | `crisis.incident.report_linked.v1` | Evidence linked. |
| `resource.assigned` | Resource | `crisis.resource.assigned.v1` | Atomic allocation. |
| `resource.assignment_status_changed` | Resource | `crisis.resource.assignment_status_changed.v1` | Mission lifecycle change. |
| `alert.approved` | Alert | `crisis.alert.approved.v1` | Approved (four-eyes). Notification may now be dispatched. |
| `alert.dispatch_requested` | Alert | `crisis.alert.dispatch_requested.v1` | Immutable dispatch command created. |
| `alert.cancelled` | Alert | `crisis.alert.cancelled.v1` | Cancelled with reason. |
| `alert.expired` | Alert | `crisis.alert.expired.v1` | Expired before delivery. |
| `alert.status_changed` | Alert | `crisis.alert.status_changed.v1` | Aggregate delivery status changed. |
| `notification.status_changed` | Notification | `crisis.notification.status_changed.v1` | Per-channel attempt result. accepted = provider acceptance, not receipt. |

DLQ: پیام‌های غیرقابل‌پردازش به `<topic>.dlq` با سرآیندهای `dlq_error` و `dlq_consumer` منتقل می‌شوند.
بازپخش رویدادهای outbox از طریق `POST /api/v1/admin/outbox/replay` (مجوز `event:replay`، محدود، ممیزی‌شده).
