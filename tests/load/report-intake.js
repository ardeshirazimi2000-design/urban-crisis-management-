// k6 load profile for report intake bursts after an earthquake (AT-11).
// Targets are placeholders until the official workload baseline is approved (decision D-08).
//   k6 run -e BASE=http://localhost:8080 tests/load/report-intake.js
import http from "k6/http";
import { check } from "k6";
import { uuidv4 } from "https://jslib.k6.io/k6-utils/1.4.0/index.js";

const BASE = __ENV.BASE || "http://localhost:8080";

export const options = {
  scenarios: {
    burst: { executor: "ramping-arrival-rate", startRate: 10, timeUnit: "1s", preAllocatedVUs: 200,
      stages: [{ target: 200, duration: "1m" }, { target: 200, duration: "3m" }, { target: 0, duration: "30s" }] },
  },
  thresholds: {
    "http_req_duration{name:create}": ["p(95)<500"],
    "checks{name:create}": ["rate>0.99"],
  },
};

// One dev identity per VU (local/staging only). Production load tests use a test IdP.
export function setup() {
  const tokens = [];
  for (let i = 0; i < 200; i++) {
    const r = http.post(`${BASE}/api/v1/dev/token`, JSON.stringify({ subject: `load-${i}`, name: "load", grants: [] }),
      { headers: { "Content-Type": "application/json" } });
    tokens.push(r.json("access_token"));
  }
  return { tokens };
}

export default function (data) {
  const token = data.tokens[__VU % data.tokens.length];
  const body = JSON.stringify({
    type: "structural_damage", description: "ترک در دیوار (آزمون بار)",
    location: { lat: 35.6 + Math.random() * 0.2, lng: 51.25 + Math.random() * 0.3, accuracy_m: 20 },
  });
  const r = http.post(`${BASE}/api/v1/reports`, body, { tags: { name: "create" }, headers: {
    "Content-Type": "application/json", Authorization: `Bearer ${token}`, "Idempotency-Key": `load-${uuidv4()}` } });
  check(r, { "202 or 429 (backpressure)": (x) => x.status === 202 || x.status === 429 }, { name: "create" });
}
