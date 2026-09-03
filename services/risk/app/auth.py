"""Bearer-JWT verification against the IdP's JWKS. Mirrors assist/app/auth.py
(internal/authn's Go equivalent) rather than importing it: assist and risk
are separate Python services with no shared package today, and this file is
~25 lines, well under the size where that would be worth building.
"""

import os

import jwt
from fastapi import Depends, HTTPException, Request

ID_JWKS_URL = os.environ.get("ID_JWKS_URL", "http://localhost:8081/.well-known/jwks.json")
ID_ISSUER = os.environ.get("ID_ISSUER", "http://localhost:8081")
ADMIN_EMAILS = {
    e.strip().lower()
    for e in os.environ.get("ADMIN_EMAILS", "alice@vault.test").split(",")
    if e.strip()
}

_jwk_client = jwt.PyJWKClient(ID_JWKS_URL, cache_keys=True, lifespan=300)


def current_user(request: Request) -> dict:
    header = request.headers.get("authorization", "")
    if not header.startswith("Bearer "):
        raise HTTPException(401, "bearer token required")
    token = header.removeprefix("Bearer ")
    try:
        key = _jwk_client.get_signing_key_from_jwt(token)
        claims = jwt.decode(
            token, key.key, algorithms=["RS256"], audience="vault", issuer=ID_ISSUER
        )
    except Exception:
        raise HTTPException(401, "invalid token")
    return claims


def admin_user(claims: dict = Depends(current_user)) -> dict:
    if claims.get("email", "").lower() not in ADMIN_EMAILS:
        raise HTTPException(403, "admin only")
    return claims
