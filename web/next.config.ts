import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV === "development";

// CSP scoped to what this app actually loads (checked app/layout.tsx, globals.css,
// and every client-side fetch/img/script before writing this):
// - script-src has no 'unsafe-inline': no inline <script> tags or styled-jsx in the
//   app. 'unsafe-eval' is added only in dev because Next's webpack dev build (and
//   React's dev-mode stack-trace reconstruction) evals modules; production needs neither.
// - style-src needs 'unsafe-inline': base-ui/react and framer-motion (both deps)
//   position/animate elements via inline style attributes, not <style> tags.
// - img-src allows any https: source: web/app/sell/SellForm.tsx lets sellers paste
//   an arbitrary listing-photo URL, and ListingCard.tsx falls back to
//   https://picsum.photos when a listing has none — 'self' alone would break both.
// - font-src is 'self' only: next/font (Plus_Jakarta_Sans) self-hosts, no external
//   font CDN is referenced anywhere.
// - connect-src is 'self' only: every client-side fetch() call targets a same-origin
//   path (/idp/* via the rewrite below); the MARKET_URL/PAY_URL/etc. backend calls
//   all happen server-side in Server Components/route handlers, which CSP can't see.
// - frame-ancestors 'none' replaces X-Frame-Options: it supersedes that header in
//   evergreen browsers (see Next's own headers docs), so we don't set both.
const cspHeader = `
  default-src 'self';
  script-src 'self'${isDev ? " 'unsafe-eval'" : ""};
  style-src 'self' 'unsafe-inline';
  img-src 'self' https: data:;
  font-src 'self';
  connect-src 'self';
  object-src 'none';
  base-uri 'self';
  form-action 'self';
  frame-ancestors 'none';
  upgrade-insecure-requests;
`
  .replace(/\s{2,}/g, " ")
  .trim();

const nextConfig: NextConfig = {
  output: "standalone",
  async headers() {
    return [
      {
        source: "/(.*)",
        headers: [
          { key: "Content-Security-Policy", value: cspHeader },
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
    // same-origin proxy for the IdP so its session cookie needs no CORS games in dev
    return [
      {
        source: "/idp/:path*",
        destination: `${process.env.ID_URL ?? "http://localhost:8081"}/:path*`,
      },
    ];
  },
};

export default nextConfig;
