// Scenario 1 (AGORA_SPEC.md section 11): drop spike.
// Target: zero oversell. p99 reserve latency < 200ms. No 5xx.
//
// Scope note: the spec's target names "p99 admission", the queue/SSE stage
// (7.2). This scenario measures the reservation claim itself instead (the
// stage that actually decides oversell), bypassing admission the same way
// internal/sale/reservations_test.go's TestNoOversell does at the code
// level. See load/README.md.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { authPool } from './lib/auth.js';
import { createOpenDrop } from './lib/drop.js';

// 409 (sold out) and 403 (not yet admitted, see the admission-bypass note
// below) are expected outcomes here, not failures: k6's default
// http_req_failed treats any non-2xx/3xx as failed, which would make this
// threshold fail on a healthy sold-out drop. Tell it what "failed" means.
http.setResponseCallback(http.expectedStatuses(200, 201, 403, 409));

const SALE_URL = __ENV.SALE_URL || 'http://localhost:8085';
const TOTAL_UNITS = 1000;
const POOL_SIZE = 100; // real distinct identities; see load/README.md

export const options = {
  scenarios: {
    spike: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '5s', target: 10000 },
        { duration: '5s', target: 0 },
      ],
      gracefulRampDown: '10s',
    },
  },
  thresholds: {
    http_req_failed: ['rate==0'], // no 5xx
    'http_req_duration{name:reserve}': ['p(99)<200'],
  },
};

export function setup() {
  const pool = authPool(POOL_SIZE);
  const seller = pool[0];
  const dropId = createOpenDrop(seller.token, TOTAL_UNITS, 1000); // units_per_user high: the pool is small, the drop isn't
  console.log(`drop ${dropId} opened with ${TOTAL_UNITS} units, admit the pool before running:`);
  console.log(`  see load/README.md's "before each drop scenario" step`);
  return { dropId, pool };
}

export default function (data) {
  const identity = data.pool[exec.vu.idInTest % data.pool.length];
  const res = http.post(
    `${SALE_URL}/drops/${data.dropId}/reserve`,
    JSON.stringify({ idempotency_key: `spike-${exec.scenario.iterationInTest}` }),
    {
      headers: { Authorization: `Bearer ${identity.token}`, 'Content-Type': 'application/json' },
      tags: { name: 'reserve' },
    }
  );
  check(res, {
    'reserve succeeded or cleanly sold out': (r) => r.status === 201 || r.status === 409,
    'no server error': (r) => r.status < 500,
  });
}
