"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export default function ResetForm({ token }: { token: string }) {
  const router = useRouter();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(e.currentTarget);
    const resp = await fetch("/idp/password/reset", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token, new_password: data.get("password") }),
    });
    if (resp.ok) {
      router.push("/auth/login");
      return;
    }
    setBusy(false);
    setError(
      resp.status === 400
        ? "This reset link is invalid or has expired. Request a new one."
        : "Something went wrong.",
    );
  }

  if (!token) {
    return <p className="text-sm text-danger">This reset link is missing its token.</p>;
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <Input
        name="password"
        type="password"
        required
        minLength={8}
        autoFocus
        autoComplete="new-password"
        aria-label="New password"
        placeholder="New password"
        className="h-10"
      />
      <p aria-live="polite" className="text-sm text-danger empty:hidden">
        {error}
      </p>
      <Button size="lg" className="h-10" type="submit" disabled={busy}>
        {busy ? "Resetting…" : "Reset password"}
      </Button>
    </form>
  );
}
