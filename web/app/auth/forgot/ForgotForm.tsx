"use client";

import { useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export default function ForgotForm() {
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    const data = new FormData(e.currentTarget);
    // Always 204: the API never reveals whether the email matched an
    // account, so the UI can't either.
    await fetch("/idp/password/forgot", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: data.get("email") }),
    });
    setBusy(false);
    setSent(true);
  }

  if (sent) {
    return (
      <p className="text-sm text-muted-foreground">
        If that email has an account, a reset link is on its way. In this demo, "on its way"
        means logged to the <code>id</code> service's console instead of a real inbox.
      </p>
    );
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        Enter the email on your account and we'll send a link to reset your password.
      </p>
      <Input
        name="email"
        type="email"
        required
        autoFocus
        autoComplete="email"
        spellCheck={false}
        aria-label="Email"
        placeholder="Email"
        className="h-10"
      />
      <Button size="lg" className="h-10" type="submit" disabled={busy}>
        {busy ? "Sending…" : "Send reset link"}
      </Button>
      <p className="text-center text-sm text-muted-foreground">
        <Link href="/auth/login" className="text-primary underline underline-offset-4">
          Back to sign in
        </Link>
      </p>
    </form>
  );
}
