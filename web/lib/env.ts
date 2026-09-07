export const ID_URL = process.env.ID_URL ?? "http://localhost:8081";
export const MARKET_URL = process.env.MARKET_URL ?? "http://localhost:8082";
export const PAY_URL = process.env.PAY_URL ?? "http://localhost:8083";
export const ASSIST_URL = process.env.ASSIST_URL ?? "http://localhost:8084";
export const RISK_URL = process.env.RISK_URL ?? "http://localhost:8086";
export const WEB_ORIGIN = process.env.WEB_ORIGIN ?? "http://localhost:3001";
export const ADMIN_EMAILS = (process.env.ADMIN_EMAILS ?? "alice@vault.test")
  .split(",")
  .map((e) => e.trim().toLowerCase())
  .filter(Boolean);
