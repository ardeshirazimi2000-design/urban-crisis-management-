import { afterEach, describe, expect, it, vi } from "vitest";
import { newKey, uuid } from "./api";

describe("uuid", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("works without crypto.randomUUID (plain-HTTP LAN deployments)", () => {
    // Simulate an insecure context: only getRandomValues is available.
    const real = globalThis.crypto;
    vi.stubGlobal("crypto", { getRandomValues: real.getRandomValues.bind(real) });
    expect(typeof crypto.randomUUID).toBe("undefined");
    const seen = new Set<string>();
    for (let i = 0; i < 100; i++) {
      const u = uuid();
      expect(u).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
      seen.add(u);
    }
    expect(seen.size).toBe(100);
    expect(newKey()).toMatch(/^web-[0-9a-f-]{36}$/); // valid Idempotency-Key format
  });
});
