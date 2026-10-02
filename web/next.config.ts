import type { NextConfig } from "next";

// Content-Security-Policy lives in middleware.ts, not here: it needs a fresh
// nonce per request for Next's own inline hydration/RSC-payload scripts,
// which a static header (what this file was setting until it broke every
// client component's onClick/onSubmit — see middleware.ts's comment) can't
// provide. Everything below is genuinely static, so it stays a plain header.
const nextConfig: NextConfig = {
  output: "standalone",
  async headers() {
    return [
      {
        source: "/(.*)",
        headers: [
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
          // Forward-looking only: browsers ignore Strict-Transport-Security on a
          // plain-HTTP response, and this deploy is HTTP-only for now (see
          // deploy/oracle/README.md). Safe to set unconditionally today; it starts
          // doing anything the day the deploy actually serves over HTTPS.
          {
            key: "Strict-Transport-Security",
            value: "max-age=63072000; includeSubDomains",
          },
        ],
      },
    ];
  },
  async rewrites() {
    return [
      // same-origin proxy for the IdP so its session cookie needs no CORS games in dev
      {
        source: "/idp/:path*",
        destination: `${process.env.ID_URL ?? "http://localhost:8081"}/:path*`,
      },
      // uploaded listing photos: market streams them from a MinIO bucket
      // that has no published port anywhere, so the browser's <img> tags
      // reach them only through this same-origin proxy, never MinIO directly
      {
        source: "/blobs/:path*",
        destination: `${process.env.MARKET_URL ?? "http://localhost:8082"}/:path*`,
      },
    ];
  },
};

export default nextConfig;
