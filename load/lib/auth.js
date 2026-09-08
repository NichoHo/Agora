import http from 'k6/http';
import crypto from 'k6/crypto';
import encoding from 'k6/encoding';
import { check } from 'k6';

const ID_URL = __ENV.ID_URL || 'http://localhost:8081';
const WEB_ORIGIN = __ENV.WEB_ORIGIN || 'http://localhost:3001';
const CLIENT_ID = 'vault-web'; // seeded in cmd/seed/main.go
const REDIRECT_URI = `${WEB_ORIGIN}/auth/callback`;

function randString(n) {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
  let s = '';
  for (let i = 0; i < n; i++) s += chars[Math.floor(Math.random() * chars.length)];
  return s;
}

// registerAndAuthorize runs the real PKCE authorization-code flow against
// `id` for one fresh user and returns a real bearer access token: register,
// password login, grant consent, GET /authorize for the code, exchange it.
// No MFA (fresh registrations don't enroll it), so no TOTP step is needed.
// This is the same sequence the storefront's login form and consent screen
// drive; k6 is a real OAuth client here, not bypassing id's logic.
export function registerAndAuthorize() {
  const email = `loadtest-${randString(10)}@vault.test`;
  const password = 'LoadTest123!';
  const handle = `lt_${randString(10)}`;

  let res = http.post(
    `${ID_URL}/register`,
    JSON.stringify({ Email: email, Password: password, Handle: handle }),
    { headers: { 'Content-Type': 'application/json' } }
  );
  if (!check(res, { 'register ok': (r) => r.status === 201 })) {
    return null;
  }

  res = http.post(
    `${ID_URL}/login`,
    JSON.stringify({ Email: email, Password: password }),
    { headers: { 'Content-Type': 'application/json' } }
  );
  if (!check(res, { 'login ok': (r) => r.status === 200 })) {
    return null;
  }

  const verifier = randString(64);
  const challenge = encoding.b64encode(crypto.sha256(verifier, 'binary'), 'rawurl');

  res = http.post(
    `${ID_URL}/consent`,
    JSON.stringify({ ClientID: CLIENT_ID, Scope: 'openid profile' }),
    { headers: { 'Content-Type': 'application/json' } }
  );
  if (!check(res, { 'consent ok': (r) => r.status === 204 })) {
    return null;
  }

  res = http.get(
    `${ID_URL}/authorize?client_id=${CLIENT_ID}&redirect_uri=${encodeURIComponent(REDIRECT_URI)}` +
      `&response_type=code&code_challenge=${challenge}&code_challenge_method=S256&scope=openid+profile`,
    { redirects: 0 }
  );
  const loc = res.headers['Location'] || '';
  const code = (loc.match(/[?&]code=([^&]+)/) || [])[1];
  if (!check(code, { 'got authorization code': (c) => !!c })) {
    return null;
  }

  res = http.post(
    `${ID_URL}/token`,
    `grant_type=authorization_code&code=${code}&client_id=${CLIENT_ID}` +
      `&redirect_uri=${encodeURIComponent(REDIRECT_URI)}&code_verifier=${verifier}`,
    { headers: { 'Content-Type': 'application/x-www-form-urlencoded' } }
  );
  if (!check(res, { 'token exchange ok': (r) => r.status === 200 })) {
    return null;
  }
  const body = res.json();
  const payload = JSON.parse(encoding.b64decode(body.access_token.split('.')[1], 'rawurl', 's'));
  return { token: body.access_token, email, userId: payload.sub };
}

// authPool provisions n real authenticated users once, for setup() to hand
// to the default function. ponytail: sequential, not parallelized. Fine up
// to a few hundred (this repo's scripts stay under 300); parallelize with
// k6's SharedArray + multiple setup workers if a scenario ever needs more.
export function authPool(n) {
  const pool = [];
  for (let i = 0; i < n; i++) {
    const identity = registerAndAuthorize();
    if (identity) pool.push(identity);
  }
  return pool;
}
