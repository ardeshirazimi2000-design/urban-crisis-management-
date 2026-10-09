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
| `REGISTRY_MIRRORS` | mirrorهای Docker Hub (فهرست با فاصله) |
| `GOPROXY_CANDIDATES`, `NPM_CANDIDATES`, `PIP_CANDIDATES` | منابع جایگزین بسته‌ها |

## عیب‌یابی
```bash
cd /opt/urban-crisis
sudo docker compose ps
sudo docker compose logs --tail=100 api migrate relay
```

## محدودیت‌ها
این استقرار **آزمایشی** است: ورود توسعه‌ای (هر کاربر شبکه با هر نقشی)، بدون HTTPS، بدون داده واقعی، و نباید روی اینترنت باز شود.
برای استقرار عملیاتی: [infra/k8s](../infra/k8s) و دروازه‌های [open-decisions.md](open-decisions.md).
