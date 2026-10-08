import { describe, expect, it } from "vitest";
import { ago, circlePolygon, t } from "./format";

describe("format", () => {
  it("translates known keys and passes through unknown ones", () => {
    expect(t("pending_approval")).toBe("در انتظار تأیید");
    expect(t("something_new")).toBe("something_new");
    expect(t(null)).toBe("—");
  });
  it("shows data age so stale data is visible", () => {
    const now = Date.parse("2026-10-08T20:00:00Z");
    expect(ago("2026-10-08T19:55:00Z", now)).toContain("دقیقه");
    expect(ago(null, now)).toBe("نامشخص");
  });
  it("builds a closed polygon for alert regions", () => {
    const p = circlePolygon(35.7, 51.4, 1000, 16);
    const ring = p.coordinates[0];
    expect(ring).toHaveLength(17);
    expect(ring[0]).toEqual(ring[16]);
    for (const [lng, lat] of ring) {
      const dy = (lat - 35.7) * 111320, dx = (lng - 51.4) * 111320 * Math.cos((35.7 * Math.PI) / 180);
      expect(Math.abs(Math.hypot(dx, dy) - 1000)).toBeLessThan(5);
    }
  });
});
