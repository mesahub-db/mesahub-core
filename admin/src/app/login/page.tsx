"use client";

import { useState } from "react";

export default function LoginPage() {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);

    try {
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });

      if (res.ok) {
        // Hard redirect: ensures the session cookie is committed and the
        // middleware runs clean. push()+refresh() together cause two concurrent
        // RSC requests that fight each other, producing hangs.
        window.location.replace("/");
        return;
      } else {
        const data = await res.json().catch(() => ({}));
        setError(data.error ?? "Invalid token");
      }
    } catch {
      setError("Network error — please try again");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen bg-neutral-950 flex items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <h1 className="text-white text-xl font-semibold mb-1 tracking-tight">
          MesaHub
        </h1>
        <p className="text-neutral-500 text-sm mb-8">
          Enter your admin token to continue.
        </p>

        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <input
            type="password"
            placeholder="Admin token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            className="w-full bg-neutral-900 border border-neutral-800 rounded-md px-4 py-2.5 text-sm text-white placeholder-neutral-600 focus:outline-none focus:ring-1 focus:ring-neutral-600"
            autoFocus
            required
          />

          {error && (
            <p className="text-red-400 text-xs">{error}</p>
          )}

          <button
            type="submit"
            disabled={loading || !token}
            className="bg-white text-neutral-950 rounded-md px-4 py-2.5 text-sm font-medium hover:bg-neutral-200 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            {loading ? "Signing in…" : "Sign in"}
          </button>
        </form>
      </div>
    </div>
  );
}
