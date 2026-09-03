# Building an OIDC provider from the RFCs (and what I got wrong first)

Most tutorials tell you to put an identity provider in front of your app and
never look inside. I did the opposite: for [Vault](https://github.com/NichoHo/Agora),
a small marketplace, I built the OAuth 2.0 / OIDC provider myself, reading the
RFCs instead of reaching for a library: Authorization Code + PKCE, RS256 with
JWKS, TOTP MFA, and rotating refresh tokens with reuse detection. Not because you
*should* run a hand-rolled IdP in production (you shouldn't; the README says so
in bold), but because building one is the fastest way to understand the failure
modes the vetted libraries are quietly protecting you from.

This is a tour of the parts that were more subtle than they looked, and two
things I got wrong on the first pass.

## The authorization code must be single-use, and proving it is a race

The Authorization Code grant (RFC 6749 §4.1) is a two-step dance: the client
gets a short-lived `code` from the browser redirect, then exchanges it
server-to-server for tokens. The code is a bearer credential in a URL, so it
*will* leak into logs, referrer headers, and browser history. The whole design
rests on one rule: **a code is redeemable exactly once.** Redeem it, or see it
redeemed twice, and it's dead.

My first implementation read the code, checked `used_at IS NULL` in Go, then
issued tokens and set `used_at`. That's a classic time-of-check-to-time-of-use
race: two concurrent exchanges both read `NULL`, both pass the check, both mint
tokens. An attacker who captures a code races the legitimate client and wins a
token.

The fix is to make the check and the claim the same atomic operation, and let
the database arbitrate:

```go
// single atomic claim: a replayed code finds used_at already set and gets nothing
err := s.pool.QueryRow(r.Context(),
    `UPDATE id.auth_codes SET used_at = now()
     WHERE code = $1 AND used_at IS NULL
     RETURNING user_id, client_id, redirect_uri, scope, nonce, code_challenge, expires_at`,
    code).Scan(&userID, &clientID, &redirectURI, &scope, &nonce, &challenge, &expiresAt)
if errors.Is(err, pgx.ErrNoRows) {
    invalidGrant(w) // unknown OR already-used: indistinguishable, deliberately
    return
}
```

`UPDATE ... WHERE used_at IS NULL ... RETURNING` is a single statement. Exactly
one concurrent exchange gets the row back; every other gets `ErrNoRows` and a
generic `invalid_grant`. The database's row lock is the mutex I don't have to
write. The test that pins this replays a code and asserts the second exchange
gets 400. Because I couldn't easily force a true concurrent race
deterministically, the atomic statement is what makes the sequential test
sufficient.

## PKCE, and refusing to be the downgrade

PKCE (RFC 7636) closes the code-interception gap for public clients: the client
sends `code_challenge = BASE64URL(SHA256(verifier))` up front, then proves it
holds the `verifier` at exchange time. The subtlety isn't computing the hash,
it's *refusing anything else*. If your `/authorize` accepts requests without a
challenge, or accepts `code_challenge_method=plain`, an attacker just omits PKCE
and you're back to bare OAuth. So the provider mandates S256:

```go
if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
    httpx.Error(w, 400, "PKCE S256 code_challenge required")
    return
}
```

And the verifier check is a constant-time compare of the recomputed challenge,
not `==`, which leaks timing:

```go
sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
if subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(challenge)) != 1 {
    invalidGrant(w)
    return
}
```

The same "refuse the downgrade" instinct applies to the redirect URI. RFC 6749
§3.1.2.4 says compare it exactly against the registered value, and on mismatch
**do not redirect**, or you're an open redirector. Exact string match,
error in place, no `Location` header.

## What I got wrong first: trusting the token's own `alg`

JWTs carry their signing algorithm in the header. The infamous mistake, the one
that's burned real systems, is verifying a token using the algorithm the token
*claims*. Accept `alg: none` and any unsigned token validates. Accept `alg: HS256`
against your RSA public key and an attacker signs tokens with your *public* key
as an HMAC secret.

My verifier hard-codes RS256 and rejects everything else before it even looks up
a key:

```go
if h.Alg != "RS256" {
    return c, fmt.Errorf("jwt: alg %q rejected", h.Alg)
}
pub, ok := keys[h.Kid]
if !ok {
    return c, fmt.Errorf("jwt: unknown kid %q", h.Kid)
}
```

The algorithm is the verifier's policy, never the token's suggestion. There's a
test that hands the verifier an `alg: none` token and asserts it's rejected, plus
one that tampers the payload and asserts the signature check catches it. Keys are
published at `/.well-known/jwks.json` and the resource services (market, pay)
fetch and cache them, refetching on an unknown `kid`, which is also how key
rotation propagates without downtime.

## Refresh token rotation, and treating reuse as theft

Access tokens are short-lived (15 minutes); refresh tokens are the long-lived
credential, so they get the most careful handling (RFC 6819 §5.2.2.3). Vault
*rotates* them: every refresh returns a new refresh token and marks the old one
used. A refresh token belongs to a **family**: the chain of rotations from one
original grant.

The security property comes from what happens on reuse. If a refresh token that
has already been rotated away is presented again, that means one of two things:
the legitimate client double-submitted, or a token was stolen and now two parties
hold the same chain. You can't tell which, so you assume the worst and revoke the
entire family:

```go
if usedAt != nil {
    // reuse detected: burn the whole family
    s.pool.Exec(r.Context(),
        `UPDATE id.refresh_tokens SET revoked_at = now()
         WHERE family_id = $1 AND revoked_at IS NULL`, familyID)
    s.audit(r.Context(), userID, "refresh.reuse_detected",
        map[string]any{"family": familyID, "client": clientID})
    invalidGrant(w)
    return
}
```

The test seeds a rotation chain, rotates twice, then re-presents the
*already-rotated* token and asserts that not only it but the newest live token in
the family is now dead. A stolen token can't outlive detection, and the real
user is forced to re-authenticate rather than silently sharing a session with an
attacker. Rotation itself is another atomic `UPDATE ... WHERE used_at IS NULL`,
same pattern as the auth code.

## MFA from the HMAC up

TOTP (RFC 6238 over RFC 4226) is just HMAC-SHA1 of a 30-second counter, truncated
to six digits. Building it from the primitive rather than from a library meant I
could test it against the RFC's published vectors, which is the kind of check
that actually catches a bug:

```go
func hotp(key []byte, counter uint64) string {
    var msg [8]byte
    binary.BigEndian.PutUint64(msg[:], counter)
    mac := hmac.New(sha1.New, key)
    mac.Write(msg[:])
    sum := mac.Sum(nil)
    offset := sum[len(sum)-1] & 0x0f
    code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
    return fmt.Sprintf("%06d", code%1_000_000)
}
```

Verification accepts the current step plus one either side (clock skew) and
compares in constant time; recovery codes are single-use, burned with (you
guessed it) a guarded `UPDATE`.

## What building it taught me

Three patterns recur through the whole provider: **let the database enforce
single-use** (guarded `UPDATE ... RETURNING` instead of check-then-write),
**make the server the policy authority** (mandate S256, hard-code RS256, and
never inherit a security decision from attacker-controlled input), and **treat
ambiguous security events as attacks** (refresh reuse revokes the family). None
of these are exotic. They're exactly what the vetted libraries do, and the
value of building the provider from the RFCs was ending up unable to *un*-see
why. In production I'd still reach for the vetted library. But now I know what
it's protecting me from.

*Vault's identity provider is [`internal/id`](../../internal/id/). The misuse
tests (replayed codes, wrong verifier, `alg:none`, refresh reuse) are the
loudest signal in the repo.*
