// Scenario 5 (AGORA_SPEC.md section 11): ledger write.
// Target: p99 < 150ms, invariants hold throughout.
//
// Pure ledger throughput: run this one with SWITCH_URL unset on pay, so
// escrow fund takes its wallet-only path (no card auth, no Java hop; that
// combination is scenario 4's job). "Invariants hold" is checked after the
// run with load/verify_invariants.sql, not inside this script: k6 measures
// latency and errors, Postgres is the source of truth for money conservation.
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { authPool } from './lib/auth.js';

const PAY_URL = __ENV.PAY_URL || 'http://localhost:8083';
const PAY_INTERNAL_TOKEN = __ENV.PAY_INTERNAL_TOKEN || 'dev-internal-token';
const AMOUNT_MINOR = 200;
const POOL_SIZE = 80;

export const options = {
  scenarios: {
    ledger: {
      executor: 'constant-arrival-rate',
      rate: 500,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 60,
      maxVUs: 200,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(99)<150'],
  },
};

export function setup() {
  const pool = authPool(POOL_SIZE);
  for (const identity of pool) {
    const res = http.post(
      `${PAY_URL}/deposits`,
      JSON.stringify({ idempotency_key: 'load-test-fund', amount_minor: 100000 }),
      { headers: { Authorization: `Bearer ${identity.token}`, 'Content-Type': 'application/json' } }
    );
    check(res, { 'wallet funded': (r) => r.status === 200 });
  }
  return { pool };
}

export default function (data) {
  const identity = data.pool[exec.vu.idInTest % data.pool.length];
  const orderId = `k6-ledger-${exec.scenario.iterationInTest}-${__VU}`;
  const res = http.post(
    `${PAY_URL}/internal/escrow/fund`,
    JSON.stringify({ order_id: orderId, buyer_id: identity.userId, amount_minor: AMOUNT_MINOR }),
    { headers: { 'X-Internal-Token': PAY_INTERNAL_TOKEN, 'Content-Type': 'application/json' } }
  );
  check(res, { 'funded or a defined business outcome': (r) => r.status === 200 || r.status === 402 });
}
