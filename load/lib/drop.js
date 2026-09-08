import http from 'k6/http';
import { check } from 'k6';

const SALE_URL = __ENV.SALE_URL || 'http://localhost:8085';

// createOpenDrop makes one drop and opens it (minting totalUnits real
// market listings, server-side inside sale, see internal/sale/drops.go).
// The open call blocks until every listing is minted, so its timeout scales
// with totalUnits; this is a one-time setup cost, never the hot path
// section 11 measures (see ADR 0002).
export function createOpenDrop(sellerToken, totalUnits, unitsPerUser) {
  let res = http.post(
    `${SALE_URL}/drops`,
    JSON.stringify({
      title: `Load test drop ${totalUnits}u`,
      description: 'k6 load test fixture',
      starts_at: new Date().toISOString(),
      total_units: totalUnits,
      price_minor: 1000,
      units_per_user: unitsPerUser,
      shard_count: 8,
    }),
    { headers: { Authorization: `Bearer ${sellerToken}`, 'Content-Type': 'application/json' } }
  );
  if (!check(res, { 'drop created': (r) => r.status === 201 })) {
    throw new Error(`create drop failed: ${res.status} ${res.body}`);
  }
  const dropId = res.json().id;

  res = http.post(`${SALE_URL}/drops/${dropId}/open`, null, {
    headers: { Authorization: `Bearer ${sellerToken}` },
    timeout: '180s',
  });
  if (!check(res, { 'drop opened': (r) => r.status === 200 })) {
    throw new Error(`open drop failed: ${res.status} ${res.body}`);
  }
  return dropId;
}
