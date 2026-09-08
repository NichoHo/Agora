// Scenario 3 (AGORA_SPEC.md section 11): marketplace browse.
// Target: p99 < 100ms. GET /listings is public; no auth setup needed.
import http from 'k6/http';
import { check } from 'k6';

const MARKET_URL = __ENV.MARKET_URL || 'http://localhost:8082';

export const options = {
  scenarios: {
    browse: {
      executor: 'constant-arrival-rate',
      rate: 500,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 50,
      maxVUs: 150,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(99)<100'],
  },
};

export default function () {
  const res = http.get(`${MARKET_URL}/listings?limit=24`);
  check(res, { '200': (r) => r.status === 200 });
}
