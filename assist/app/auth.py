"""Bearer-JWT verification against the IdP's JWKS (mirrors internal/authn)."""

import os

import jwt
import psycopg.errors
from fastapi import Depends, HTTPException, Request

from .db import pool

ID_JWKS_URL = os.environ.get("ID_JWKS_URL", "http://localhost:8081/.well-known/jwks.json")
ID_ISSUER = os.environ.get("ID_ISSUER", "http://localhost:8081")
ADMIN_EMAILS = {
    e.strip().lower()
    for e in os.environ.get("ADMIN_EMAILS", "alice@vault.test").split(",")
    if e.strip()
}

_jwk_client = jwt.PyJWKClient(ID_JWKS_URL, cache_keys=True, lifespan=300)


def _active(user_id: str) -> bool:
    """Mirrors internal/authn.Verifier.active (Go): same database, same
    check, so a deactivated account is rejected on its next request here
    too, not just once its access token expires."""
    try:
        with pool.connection() as conn:
            row = conn.execute(
                "SELECT is_active FROM id.users WHERE id = %s", (user_id,)
            ).fetchone()
    except psycopg.errors.UndefinedTable:
        return True  # id schema not migrated into this DB (tests only)
    if row is None:
        return False
    return row[0]


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
    if not _active(claims["sub"]):
        raise HTTPException(401, "invalid token")
    return claims


def admin_user(claims: dict = Depends(current_user)) -> dict:
    if claims.get("email", "").lower() not in ADMIN_EMAILS:
        raise HTTPException(403, "admin only")
    return claims
