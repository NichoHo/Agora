// Scenario 4 (AGORA_SPEC.md section 11): checkout end to end.
// Target: p99 < 800ms including the Java hop.
//
// Tests pay's escrow-fund endpoint directly (the scenario is named "through
// pay -> switch", not "through market -> pay -> switch"): each call is a
// real card authorization against switch (see internal/pay/switchclient.go,
// docs/adr/0003), so switch's own stack must be running with SWITCH_URL set
// on pay. order_id is a fresh random id per call; pay's ledger keys on it
// but doesn't require a matching market.orders row (different services).
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { authPool } from './lib/auth.js';

const PAY_URL = __ENV.PAY_URL || 'http://localhost:8083';
const PAY_INTERNAL_TOKEN = __ENV.PAY_INTERNAL_TOKEN || 'dev-internal-token';
const AMOUNT_MINOR = 500;
const POOL_SIZE = 50;

export const options = {
  scenarios: {
    checkout: {
      executor: 'constant-arrival-rate',
      rate: 100,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 30,
      maxVUs: 100,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(99)<800'],
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
  const orderId = `k6-checkout-${exec.scenario.iterationInTest}-${__VU}`;
  const res = http.post(
    `${PAY_URL}/internal/escrow/fund`,
    JSON.stringify({ order_id: orderId, buyer_id: identity.userId, amount_minor: AMOUNT_MINOR }),
    { headers: { 'X-Internal-Token': PAY_INTERNAL_TOKEN, 'Content-Type': 'application/json' } }
  );
  check(res, { 'funded or a defined business outcome': (r) => r.status === 200 || r.status === 402 });
}
