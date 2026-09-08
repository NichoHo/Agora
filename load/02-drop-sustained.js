// Scenario 2 (AGORA_SPEC.md section 11): drop sustained.
// Target: p99 < 300ms, error rate < 0.1%.
//
// Once the drop's stock is gone, further attempts get a clean 409 sold_out,
// not an error: this scenario measures the reserve endpoint's throughput
// and latency under sustained load, not how long inventory lasts. Same
// admission-bypass scope note as 01-drop-spike.js.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { authPool } from './lib/auth.js';
import { createOpenDrop } from './lib/drop.js';

// See 01-drop-spike.js: 409 (sold out) and 403 (not yet admitted, the
// admission-bypass harness gap) are expected here, not failures.
http.setResponseCallback(http.expectedStatuses(200, 201, 403, 409));

const SALE_URL = __ENV.SALE_URL || 'http://localhost:8085';
const TOTAL_UNITS = 2000;
const POOL_SIZE = 100;

export const options = {
  scenarios: {
    sustained: {
      executor: 'constant-arrival-rate',
      rate: 2000,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 300,
      maxVUs: 600,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(99)<300'],
  },
};

export function setup() {
  const pool = authPool(POOL_SIZE);
  const seller = pool[0];
  const dropId = createOpenDrop(seller.token, TOTAL_UNITS, 10000);
  console.log(`drop ${dropId} opened with ${TOTAL_UNITS} units, admit the pool before running`);
  return { dropId, pool };
}

export default function (data) {
  const identity = data.pool[exec.vu.idInTest % data.pool.length];
  const res = http.post(
    `${SALE_URL}/drops/${data.dropId}/reserve`,
    JSON.stringify({ idempotency_key: `sustained-${exec.scenario.iterationInTest}` }),
    { headers: { Authorization: `Bearer ${identity.token}`, 'Content-Type': 'application/json' } }
  );
  check(res, { 'no server error': (r) => r.status < 500 });
}
