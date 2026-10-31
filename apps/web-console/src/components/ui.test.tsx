import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";
import { ErrorBox } from "./ui";

describe("ErrorBox", () => {
  it("explains validation reasons in Persian instead of raw codes", () => {
    const html = renderToStaticMarkup(<ErrorBox error={new ApiError(422, "VALIDATION_ERROR", "درخواست معتبر نیست", "c1",
      [{ field: "role.role", reason: "app_only_user_needs_app_role" }, { field: "grants.role", reason: "unknown:XYZ" }])} />);
    expect(html).toContain("نقش");
    expect(html).toContain("«امدادگر» یا «شهروند»");
    expect(html).not.toContain("app_only_user_needs_app_role");
    expect(html).toContain("grants.role"); // unknown fields still shown as sent
  });
});
