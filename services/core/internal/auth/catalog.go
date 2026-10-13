package auth

// PermissionInfo describes a permission for the console's roles and access matrix.
type PermissionInfo struct {
	Code  Permission `json:"code"`
	Group string     `json:"group"`
	Title string     `json:"title"`
}

// Catalog lists every permission with its Persian title, grouped as shown in the console.
var Catalog = []PermissionInfo{
	{ReportCreate, "گزارش‌ها", "ثبت گزارش (شهروند / امدادگر)"},
	{ReportIntake, "گزارش‌ها", "ثبت گزارش تلفنی از طرف تماس‌گیرنده"},
	{ReportReadOwn, "گزارش‌ها", "مشاهده گزارش‌های خود"},
	{ReportRead, "گزارش‌ها", "مشاهده همه گزارش‌ها"},
	{ReportReadPrecise, "گزارش‌ها", "مشاهده مختصات دقیق گزارش‌دهنده"},
	{ReportReview, "گزارش‌ها", "بررسی، تأیید، رد یا ادغام گزارش"},
	{MediaRead, "گزارش‌ها", "مشاهده عکس و رسانه گزارش"},

	{IncidentCreate, "حوادث", "ایجاد حادثه"},
	{IncidentRead, "حوادث", "مشاهده حوادث سازمان"},
	{IncidentReadAssign, "حوادث", "مشاهده حوادث مأموریت‌های خود"},
	{IncidentTransition, "حوادث", "تغییر وضعیت حادثه"},
	{IncidentApprove, "حوادث", "تأیید فعال‌سازی، تشدید و بستن حادثه"},
	{IncidentLinkReport, "حوادث", "پیوند گزارش به حادثه"},
	{SitrepRead, "حوادث", "مشاهده گزارش وضعیت"},
	{SitrepIssue, "حوادث", "صدور گزارش وضعیت"},

	{ResourceRead, "منابع و امداد", "مشاهده منابع"},
	{ResourceManage, "منابع و امداد", "مدیریت منابع، ظرفیت و پناهگاه"},
	{ResourceAllocate, "منابع و امداد", "تخصیص منبع و مأموریت"},
	{AssignmentUpdate, "منابع و امداد", "ثبت وضعیت مأموریت خود"},
	{NeedRead, "منابع و امداد", "مشاهده نیازهای باز"},
	{NeedCreate, "منابع و امداد", "ثبت و لغو نیاز"},
	{NeedFulfill, "منابع و امداد", "تأمین نیاز"},
	{ShelterRead, "منابع و امداد", "مشاهده اسکان اضطراری"},
	{ShelterReadPublic, "منابع و امداد", "مشاهده نزدیک‌ترین پناهگاه‌ها (شهروند)"},
	{ShelterUpdate, "منابع و امداد", "ثبت پذیرش و ترخیص در پناهگاه"},

	{HospitalRead, "درمان و خسارت", "مشاهده بیمارستان‌ها"},
	{HospitalUpdate, "درمان و خسارت", "به‌روزرسانی ظرفیت بیمارستان"},
	{CasualtyRead, "درمان و خسارت", "مشاهده مصدومان"},
	{CasualtyRecord, "درمان و خسارت", "ثبت تریاژ و انتقال مصدوم"},
	{CasualtyRecordAsgn, "درمان و خسارت", "ثبت مصدوم در مأموریت خود"},
	{DamageRead, "درمان و خسارت", "مشاهده ارزیابی خسارت"},
	{DamageAssess, "درمان و خسارت", "ارزیابی سریع ساختمان"},

	{AlertReadPublic, "هشدار عمومی", "دریافت هشدارهای رسمی"},
	{AlertDraft, "هشدار عمومی", "پیش‌نویس هشدار"},
	{AlertApprove, "هشدار عمومی", "تأیید هشدار (چهارچشمی)"},
	{AlertDispatch, "هشدار عمومی", "ارسال هشدار"},
	{AlertCancel, "هشدار عمومی", "لغو هشدار"},
	{AlertReadDelivery, "هشدار عمومی", "مشاهده وضعیت تحویل هشدار"},
	{GuidanceReadPublic, "هشدار عمومی", "راهنمای دستیار شهروند"},
	{GuidanceEdit, "هشدار عمومی", "پیشنهاد متن راهنما"},
	{GuidanceApprove, "هشدار عمومی", "انتشار راهنما"},

	{GISRead, "نقشه", "مشاهده نقشه و لایه‌ها"},
	{GISManage, "نقشه", "مدیریت لایه‌ها، انسداد و محدوده اثر"},

	{UserManage, "هویت و امنیت", "تعریف و مدیریت کاربران"},
	{RoleManage, "هویت و امنیت", "تخصیص نقش"},
	{SessionManage, "هویت و امنیت", "مشاهده و ابطال نشست‌ها"},
	{ShahkarInquire, "هویت و امنیت", "استعلام شاهکار"},
	{SettingsManage, "هویت و امنیت", "تنظیمات امنیتی"},
	{EventReplay, "هویت و امنیت", "بازپخش رویدادها"},
	{IntegrityCheck, "هویت و امنیت", "بررسی صحت داده‌ها"},
	{AuditRead, "ممیزی", "مشاهده و خروجی ممیزی"},
	{AuditConfigure, "ممیزی", "پیکربندی و بایگانی ممیزی"},
	{DataExport, "خروجی داده", "خروجی کنترل‌شده داده‌های عملیاتی"},
}

// RoleTitles are the Persian names of the roles.
var RoleTitles = map[Role]string{
	RoleCitizen: "شهروند", RoleResponder: "امدادگر", RoleOperator: "اپراتور", RoleCommander: "فرمانده",
	RoleResourceManager: "مدیر منابع", RoleGISAnalyst: "کارشناس GIS", RoleSecurityAdmin: "مدیر امنیت",
	RoleSecurityAuditor: "ممیز امنیتی",
}

// RoleOrder is the display order of the roles.
var RoleOrder = []Role{RoleSecurityAdmin, RoleSecurityAuditor, RoleCommander, RoleOperator, RoleResourceManager,
	RoleGISAnalyst, RoleResponder, RoleCitizen}
