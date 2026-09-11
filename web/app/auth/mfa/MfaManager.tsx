"use client";

import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      onClick={copy}
      aria-label={copied ? "Copied" : label}
    >
      {copied ? <Check className="text-success" /> : <Copy />}
    </Button>
  );
}

type State =
  | { step: "loading" }
  | { step: "signed_out" }
  | { step: "disabled" }
  | { step: "enrolling"; secret: string; uri: string }
  | { step: "codes"; codes: string[] }
  | { step: "enabled" };

export default function MfaManager() {
  const [state, setState] = useState<State>({ step: "loading" });
  const [error, setError] = useState("");

  useEffect(() => {
    fetch("/idp/mfa").then(async (resp) => {
      if (resp.status === 401) return setState({ step: "signed_out" });
      const body = (await resp.json()) as { enabled: boolean };
      setState({ step: body.enabled ? "enabled" : "disabled" });
    });
  }, []);

  async function enroll() {
    setError("");
    const resp = await fetch("/idp/mfa/enroll", { method: "POST" });
    if (!resp.ok) return setError("Could not start enrollment.");
    const body = (await resp.json()) as { secret: string; otpauth_uri: string };
    setState({ step: "enrolling", secret: body.secret, uri: body.otpauth_uri });
  }

  async function activate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError("");
    const code = new FormData(e.currentTarget).get("code");
    const resp = await fetch("/idp/mfa/activate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code }),
    });
    if (!resp.ok) return setError("Wrong code. Check your authenticator app and try again.");
    const body = (await resp.json()) as { recovery_codes: string[] };
    setState({ step: "codes", codes: body.recovery_codes });
  }

  async function disable(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError("");
    const code = new FormData(e.currentTarget).get("code");
    const resp = await fetch("/idp/mfa/disable", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code }),
    });
    if (!resp.ok) return setError("Wrong code.");
    setState({ step: "disabled" });
  }

  switch (state.step) {
    case "loading":
      return <p className="text-sm text-faint">Loading…</p>;
    case "signed_out":
      return (
        <p className="text-sm text-muted-foreground">
          You need to{" "}
          <a href="/auth/start?next=/auth/mfa" className="text-primary underline">
            sign in
          </a>{" "}
          first.
        </p>
      );
    case "disabled":
      return (
        <div className="flex flex-col gap-3">
          <p className="text-sm text-muted-foreground">
            Two-factor authentication is <span className="font-medium text-ink">off</span>.
            Enable it to require a 6-digit code at every sign-in.
          </p>
          {error ? <p className="text-sm text-danger">{error}</p> : null}
          <Button onClick={enroll}>Enable two-factor authentication</Button>
        </div>
      );
    case "enrolling":
      return (
        <form onSubmit={activate} className="flex flex-col gap-3">
          <p className="text-sm text-muted-foreground">
            Add this key to your authenticator app (Google Authenticator, 1Password, …) using
            “enter a setup key”, then confirm with a code.
          </p>
          <div className="rounded-control bg-fill p-3">
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-faint">Secret key</p>
              <CopyButton value={state.secret} label="Copy secret key" />
            </div>
            <code className="break-all text-sm font-bold tracking-wider text-ink">
              {state.secret}
            </code>
          </div>
          <details className="text-xs text-faint">
            <summary className="cursor-pointer">otpauth:// URI</summary>
            <code className="break-all">{state.uri}</code>
          </details>
          <Input
            name="code"
            required
            autoComplete="off"
            inputMode="numeric"
            placeholder="123456"
            className="text-center tracking-widest"
          />
          {error ? <p className="text-sm text-danger">{error}</p> : null}
          <Button type="submit">Confirm & enable</Button>
        </form>
      );
    case "codes":
      return (
        <div className="flex flex-col gap-3">
          <p className="rounded-control bg-success-tint px-3 py-2 text-sm text-success">
            Two-factor authentication is now on.
          </p>
          <p className="text-sm text-muted-foreground">
            Save these recovery codes somewhere safe. Each one works once if you lose your
            authenticator, and they will not be shown again.
          </p>
          <div className="grid grid-cols-2 gap-2 rounded-control bg-fill p-3">
            {state.codes.map((c) => (
              <div key={c} className="flex items-center justify-between gap-1">
                <code className="text-sm text-ink">{c}</code>
                <CopyButton value={c} label={`Copy recovery code ${c}`} />
              </div>
            ))}
          </div>
          <Button variant="outline" onClick={() => setState({ step: "enabled" })}>
            I saved them
          </Button>
        </div>
      );
    case "enabled":
      return (
        <form onSubmit={disable} className="flex flex-col gap-3">
          <p className="text-sm text-ink">
            Two-factor authentication is <span className="font-medium text-success">on</span>.
          </p>
          <p className="text-sm text-muted-foreground">To turn it off, enter a current code:</p>
          <Input
            name="code"
            required
            inputMode="numeric"
            placeholder="123456"
            className="text-center tracking-widest"
          />
          {error ? <p className="text-sm text-danger">{error}</p> : null}
          <Button type="submit" variant="destructive">
            Disable two-factor authentication
          </Button>
        </form>
      );
  }
}
