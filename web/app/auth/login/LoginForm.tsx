"use client";

import { useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  InputOTP,
  InputOTPGroup,
  InputOTPSlot,
} from "@/components/ui/input-otp";
import { REGEXP_ONLY_DIGITS } from "input-otp";

export default function LoginForm({ returnTo }: { returnTo: string }) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [step, setStep] = useState<"password" | "totp" | "recovery">("password");

  async function submitPassword(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(e.currentTarget);
    const resp = await fetch("/idp/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: data.get("email"), password: data.get("password") }),
    });
    if (!resp.ok) {
      setBusy(false);
      setError(resp.status === 401 ? "Invalid email or password." : "Something went wrong.");
      return;
    }
    const body = (await resp.json()) as { mfa_required?: boolean };
    if (body.mfa_required) {
      setBusy(false);
      setStep("totp");
    } else {
      window.location.href = returnTo;
    }
  }

  async function submitCode(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(e.currentTarget);
    const path = step === "totp" ? "/idp/login/totp" : "/idp/login/recovery";
    const resp = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: data.get("code") }),
    });
    if (resp.ok) {
      window.location.href = returnTo;
    } else {
      setBusy(false);
      setError(step === "totp" ? "Wrong code. Try again." : "Invalid recovery code.");
    }
  }

  if (step !== "password") {
    return (
      <form onSubmit={submitCode} className="flex flex-col gap-4">
        <p className="text-sm text-muted-foreground">
          {step === "totp"
            ? "Enter the 6-digit code from your authenticator app."
            : "Enter one of your recovery codes."}
        </p>
        {step === "totp" ? (
          <InputOTP
            key={step}
            name="code"
            maxLength={6}
            pattern={REGEXP_ONLY_DIGITS}
            required
            autoFocus
            autoComplete="off"
            aria-label="Authenticator code"
            containerClassName="justify-center"
          >
            <InputOTPGroup>
              {[0, 1, 2, 3, 4, 5].map((i) => (
                <InputOTPSlot key={i} index={i} className="size-11 text-lg" />
              ))}
            </InputOTPGroup>
          </InputOTP>
        ) : (
          <Input
            key={step}
            name="code"
            required
            autoFocus
            autoComplete="off"
            spellCheck={false}
            aria-label="Recovery code"
            placeholder="recovery code"
            className="h-10 text-center text-lg tracking-widest"
          />
        )}
        <p aria-live="polite" className="text-sm text-danger empty:hidden">
          {error}
        </p>
        <Button size="lg" className="h-10" type="submit" disabled={busy}>
          {busy ? "Verifying…" : "Verify"}
        </Button>
        <button
          type="button"
          onClick={() => setStep(step === "totp" ? "recovery" : "totp")}
          className="text-sm text-primary underline underline-offset-4"
        >
          {step === "totp" ? "Use a recovery code instead" : "Use authenticator code"}
        </button>
      </form>
    );
  }

  return (
    <form onSubmit={submitPassword} className="flex flex-col gap-4">
      <Input
        name="email"
        type="email"
        required
        autoComplete="email"
        spellCheck={false}
        aria-label="Email"
        placeholder="Email"
        className="h-10"
      />
      <Input
        name="password"
        type="password"
        required
        autoComplete="current-password"
        aria-label="Password"
        placeholder="Password"
        className="h-10"
      />
      <p aria-live="polite" className="text-sm text-danger empty:hidden">
        {error}
      </p>
      <Button size="lg" className="h-10" type="submit" disabled={busy}>
        {busy ? "Signing in…" : "Sign in"}
      </Button>
      <p className="text-center text-sm text-muted-foreground">
        <Link href="/auth/forgot" className="text-primary underline underline-offset-4">
          Forgot password?
        </Link>
      </p>
      <p className="text-center text-sm text-muted-foreground">
        No account?{" "}
        <Link
          href={`/auth/register?return_to=${encodeURIComponent(returnTo)}`}
          className="text-primary underline underline-offset-4"
        >
          Register
        </Link>
      </p>
    </form>
  );
}
