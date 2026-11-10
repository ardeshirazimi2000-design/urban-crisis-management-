import { describe, expect, it } from "vitest";
import { Ban, Check, FileSpreadsheet, LogOut, Megaphone, Send, X } from "lucide-react";
import { actionIcon } from "./Act";

describe("action icons", () => {
  it("gives the same action the same icon and does not confuse similar words", () => {
    expect(actionIcon("خروجی CSV")).toBe(FileSpreadsheet); // «خروجی» (export) is not «خروج» (sign out)
    expect(actionIcon("خروج")).toBe(LogOut);
    expect(actionIcon("خروج از سامانه")).toBe(LogOut);
    expect(actionIcon("ثبت مصدوم")).toBe(Check);
    expect(actionIcon("انصراف")).toBe(X);
    expect(actionIcon("ارسال برای تأیید")).toBe(Send);
    expect(actionIcon("ارسال")).toBe(Megaphone);
    expect(actionIcon("حذف فرمانده")).toBe(Ban);
    expect(actionIcon("متنی بی‌ربط")).toBeUndefined();
  });
});
