"use client";

import { FILES_ENABLED } from "@/lib/features";
import { useState, useMemo, useTransition } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import type { DbRecord, BucketRecord, MetricsData, SystemDbRecord } from "./page";

// ─── Helpers ──────────────────────────────────────────────────────────────────

function formatBytes(bytes: number): string {
  if (bytes === 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}

function relativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  if (isNaN(date.getTime())) return "—";
  const diff = Date.now() - date.getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  if (days < 30) return `${days}d ago`;
  return date.toLocaleDateString();
}

// ─── Status badge ─────────────────────────────────────────────────────────────

function StatusBadge({ status }: { status: string }) {
  const active = status === "active";
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        className={`w-1.5 h-1.5 rounded-full ${active ? "bg-emerald-400" : "bg-zinc-600"}`}
        aria-hidden="true"
      />
      <span
        className={`text-xs font-medium ${active ? "text-emerald-400" : "text-zinc-500"}`}
      >
        {status}
      </span>
    </span>
  );
}

// ─── Volume bar ───────────────────────────────────────────────────────────────

export function VolumeBar({ metrics }: { metrics: MetricsData | null }) {
  if (!metrics) return null;
  const pct = Math.min(metrics.volume_used_percent, 100);
  const critical = pct > 85;
  const warning = pct > 65;

  return (
    <div className="flex items-center gap-3">
      <div className="w-28 h-1.5 bg-zinc-800 rounded-full overflow-hidden">
        <div
          className={`h-full rounded-full transition-all ${
            critical ? "bg-red-500" : warning ? "bg-amber-500" : "bg-emerald-500"
          }`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-xs text-zinc-400 tabular-nums whitespace-nowrap">
        <span className={critical ? "text-red-400" : warning ? "text-amber-400" : "text-zinc-300"}>
          {formatBytes(metrics.volume_used_bytes)}
        </span>
        {" / "}
        {formatBytes(metrics.volume_total_bytes)}
      </span>
    </div>
  );
}

// ─── Search input ─────────────────────────────────────────────────────────────

function SearchInput({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  return (
    <div className="relative">
      <svg
        className="absolute left-3 top-1/2 -translate-y-1/2 text-zinc-500 pointer-events-none"
        width="14"
        height="14"
        viewBox="0 0 16 16"
        fill="none"
        aria-hidden="true"
        suppressHydrationWarning
      >
        <circle cx="6.5" cy="6.5" r="5" stroke="currentColor" strokeWidth="1.5" suppressHydrationWarning />
        <path d="M10.5 10.5L14 14" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" suppressHydrationWarning />
      </svg>
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-64 bg-zinc-900 border border-zinc-800 rounded-md pl-8 pr-3 py-1.5 text-sm text-zinc-200 placeholder-zinc-600 focus:outline-none focus:border-zinc-600 focus:ring-1 focus:ring-zinc-700 transition-colors"
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange("")}
          className="absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-500 hover:text-zinc-300"
          aria-label="Clear search"
        >
          <svg width="12" height="12" viewBox="0 0 12 12" fill="none" suppressHydrationWarning>
            <path d="M2 2l8 8M10 2l-8 8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" suppressHydrationWarning />
          </svg>
        </button>
      )}
    </div>
  );
}

// ─── Empty state ──────────────────────────────────────────────────────────────

function EmptyState({
  query,
  noun,
  onNew,
}: {
  query: string;
  noun: string;
  onNew?: () => void;
}) {
  return (
    <div className="py-16 text-center">
      {query ? (
        <>
          <p className="text-sm text-zinc-500">
            No {noun}s matching{" "}
            <span className="font-mono text-zinc-300">"{query}"</span>
          </p>
        </>
      ) : (
        <>
          <p className="text-sm text-zinc-500">No {noun}s yet.</p>
          {onNew && (
            <button
              type="button"
              onClick={onNew}
              className="mt-3 text-sm text-white underline underline-offset-2 hover:no-underline"
            >
              Create one
            </button>
          )}
        </>
      )}
    </div>
  );
}

// ─── Create DB modal ──────────────────────────────────────────────────────────

function CreateDbModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      const res = await fetch("/api/db", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name,
          owner: "admin",
          description: description || undefined,
        }),
      });
      if (res.ok) {
        onCreated();
      } else {
        const data = await res.json().catch(() => ({}));
        setError(data.error ?? "Failed to create database");
      }
    } catch {
      setError("Network error — please try again");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal title="New database" onClose={onClose}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Name" hint="lowercase, hyphens, underscores">
          <input
            autoFocus
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="my-database"
            pattern="[a-z0-9_-]+"
            required
            className={inputCls}
          />
        </Field>
        <Field label="Description" optional>
          <input
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="What is this database for?"
            className={inputCls}
          />
        </Field>
        {error && <p className="text-red-400 text-xs">{error}</p>}
        <ModalActions onClose={onClose} loading={loading} label="Create database" />
      </form>
    </Modal>
  );
}

// ─── Create Bucket modal ──────────────────────────────────────────────────────

function CreateBucketModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      const res = await fetch("/api/buckets", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name,
          description: description || undefined,
        }),
      });
      if (res.ok) {
        onCreated();
      } else {
        const data = await res.json().catch(() => ({}));
        setError(data.error ?? "Failed to create bucket");
      }
    } catch {
      setError("Network error — please try again");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal title="New bucket" onClose={onClose}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Name" hint="lowercase, hyphens, underscores">
          <input
            autoFocus
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="my-bucket"
            pattern="[a-z0-9_-]+"
            required
            className={inputCls}
          />
        </Field>
        <Field label="Description" optional>
          <input
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="What is this bucket for?"
            className={inputCls}
          />
        </Field>
        {error && <p className="text-red-400 text-xs">{error}</p>}
        <ModalActions onClose={onClose} loading={loading} label="Create bucket" />
      </form>
    </Modal>
  );
}

// ─── Modal primitives ─────────────────────────────────────────────────────────

const inputCls =
  "w-full bg-zinc-900 border border-zinc-700 rounded-md px-3 py-2 text-sm text-zinc-100 placeholder-zinc-600 focus:outline-none focus:border-zinc-500 focus:ring-1 focus:ring-zinc-600 transition-colors";

function Field({
  label,
  hint,
  optional,
  children,
}: {
  label: string;
  hint?: string;
  optional?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="flex items-center gap-2 text-xs font-medium text-zinc-300 mb-1.5">
        {label}
        {hint && <span className="text-zinc-600 font-normal">{hint}</span>}
        {optional && <span className="text-zinc-600 font-normal">(optional)</span>}
      </label>
      {children}
    </div>
  );
}

function ModalActions({
  onClose,
  loading,
  label,
}: {
  onClose: () => void;
  loading: boolean;
  label: string;
}) {
  return (
    <div className="flex items-center justify-end gap-2 pt-2">
      <button
        type="button"
        onClick={onClose}
        className="text-sm px-4 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800 transition-colors"
      >
        Cancel
      </button>
      <button
        type="submit"
        disabled={loading}
        className="text-sm px-4 py-2 rounded-md bg-white text-zinc-950 font-medium hover:bg-zinc-200 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {loading ? "Creating…" : label}
      </button>
    </div>
  );
}

function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4"
      style={{ background: "rgba(0,0,0,0.75)", backdropFilter: "blur(4px)" }}
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="w-full max-w-md bg-zinc-950 border border-zinc-800 rounded-xl shadow-2xl overflow-hidden">
        <div className="flex items-center justify-between px-5 py-4 border-b border-zinc-800">
          <h2 className="text-sm font-semibold text-white">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="text-zinc-500 hover:text-zinc-200 transition-colors"
            aria-label="Close"
          >
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" suppressHydrationWarning>
              <path
                d="M3 3l10 10M13 3L3 13"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
                suppressHydrationWarning
              />
            </svg>
          </button>
        </div>
        <div className="px-5 py-4">{children}</div>
      </div>
    </div>
  );
}

// ─── Database list ────────────────────────────────────────────────────────────

function DbList({
  dbs,
  query,
  onNew,
}: {
  dbs: DbRecord[];
  query: string;
  onNew: () => void;
}) {
  const filtered = useMemo(() => {
    if (!query) return dbs;
    const q = query.toLowerCase();
    return dbs.filter(
      (d) =>
        d.name.toLowerCase().includes(q) || d.owner.toLowerCase().includes(q)
    );
  }, [dbs, query]);

  if (dbs.length === 0) {
    return <EmptyState query="" noun="database" onNew={onNew} />;
  }

  if (filtered.length === 0) {
    return <EmptyState query={query} noun="database" />;
  }

  return (
    <div className="overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-zinc-800">
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Name
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Owner
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Size
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Created
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Status
            </th>
            <th className="px-4 py-2.5" />
          </tr>
        </thead>
        <tbody>
          {filtered.map((db) => (
            <tr
              key={db.id}
              className="border-b border-zinc-900 hover:bg-zinc-900/60 transition-colors"
            >
              <td className="px-4 py-3">
                <span className="font-mono text-sm text-white">{db.name}</span>
              </td>
              <td className="px-4 py-3">
                <span className="text-zinc-400 text-sm">{db.owner || "—"}</span>
              </td>
              <td className="px-4 py-3">
                <span className="text-zinc-400 text-sm tabular-nums">
                  {formatBytes(db.size_bytes ?? 0)}
                </span>
              </td>
              <td className="px-4 py-3">
                <span className="text-zinc-500 text-sm" title={db.created_at}>
                  {relativeTime(db.created_at)}
                </span>
              </td>
              <td className="px-4 py-3">
                <StatusBadge status={db.status} />
              </td>
              <td className="px-4 py-3">
                <div className="flex items-center justify-end gap-3">
                  <Link
                    href={`/db/${db.name}/settings`}
                    className="text-xs text-zinc-500 hover:text-zinc-200 transition-colors"
                  >
                    Settings
                  </Link>
                  <Link
                    href={`/db/${db.name}`}
                    className="text-xs text-zinc-300 hover:text-white transition-colors font-medium"
                  >
                    Browse →
                  </Link>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <div className="px-4 py-3 border-t border-zinc-900">
        <Link
          href="/deleted"
          className="text-xs text-zinc-600 hover:text-zinc-400 transition-colors"
        >
          View deleted databases →
        </Link>
      </div>
    </div>
  );
}

// ─── Bucket list ──────────────────────────────────────────────────────────────

function BucketList({
  buckets,
  query,
  onNew,
}: {
  buckets: BucketRecord[];
  query: string;
  onNew: () => void;
}) {
  const filtered = useMemo(() => {
    if (!query) return buckets;
    const q = query.toLowerCase();
    return buckets.filter(
      (b) =>
        b.name.toLowerCase().includes(q) ||
        b.display_name.toLowerCase().includes(q)
    );
  }, [buckets, query]);

  if (buckets.length === 0) {
    return <EmptyState query="" noun="bucket" onNew={onNew} />;
  }

  if (filtered.length === 0) {
    return <EmptyState query={query} noun="bucket" />;
  }

  return (
    <div className="overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-zinc-800">
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Name
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Size
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Created
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Status
            </th>
            <th className="px-4 py-2.5" />
          </tr>
        </thead>
        <tbody>
          {filtered.map((b) => (
            <tr
              key={b.id}
              className="border-b border-zinc-900 hover:bg-zinc-900/60 transition-colors"
            >
              <td className="px-4 py-3">
                <span className="font-mono text-sm text-white">{b.name}</span>
                {b.description && (
                  <p className="text-xs text-zinc-500 mt-0.5 truncate max-w-[240px]">{b.description}</p>
                )}
              </td>
              <td className="px-4 py-3">
                <span className="text-zinc-400 text-sm tabular-nums">
                  {formatBytes(b.size_bytes)}
                </span>
              </td>
              <td className="px-4 py-3">
                <span className="text-zinc-500 text-sm" title={b.created_at}>
                  {relativeTime(b.created_at)}
                </span>
              </td>
              <td className="px-4 py-3">
                <StatusBadge status={b.status} />
              </td>
              <td className="px-4 py-3">
                <div className="flex items-center justify-end gap-3">
                  <Link
                    href={`/buckets/${b.name}/files`}
                    className="text-xs text-zinc-300 hover:text-white transition-colors font-medium"
                  >
                    Browse →
                  </Link>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ─── System DB list ───────────────────────────────────────────────────────────

function SystemList({ dbs }: { dbs: SystemDbRecord[] }) {

  return (
    <div className="overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-zinc-800">
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Name
            </th>
            <th className="text-left px-4 py-2.5 text-xs font-medium text-zinc-500 uppercase tracking-wider">
              Description
            </th>
            <th className="px-4 py-2.5" />
          </tr>
        </thead>
        <tbody>
          {dbs.map((db) => (
            <tr
              key={db.name}
              className="border-b border-zinc-900 hover:bg-zinc-900/60 transition-colors"
            >
              <td className="px-4 py-3">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm text-white">{db.name}</span>
                  <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-semibold bg-amber-950 text-amber-400 border border-amber-900">
                    system
                  </span>
                </div>
              </td>
              <td className="px-4 py-3">
                <span className="text-zinc-500 text-xs">
                  {db.description || "System database"}
                </span>
              </td>
              <td className="px-4 py-3">
                <div className="flex justify-end">
                  <Link
                    href={`/db/system/${db.name}`}
                    className="text-xs text-zinc-300 hover:text-white transition-colors font-medium"
                  >
                    Browse →
                  </Link>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ─── Tab bar ──────────────────────────────────────────────────────────────────

type Tab = "databases" | "files" | "system";

function TabBar({
  active,
  onChange,
  dbCount,
  bucketCount,
  systemCount,
}: {
  active: Tab;
  onChange: (t: Tab) => void;
  dbCount: number;
  bucketCount: number;
  systemCount: number;
}) {
  const tabs: { id: Tab; label: string; count: number }[] = [
    { id: "databases", label: "Databases", count: dbCount },
    ...(FILES_ENABLED ? [{ id: "files" as Tab, label: "Files", count: bucketCount }] : []),
    { id: "system", label: "System", count: systemCount },
  ];

  return (
    <div className="flex items-center border-b border-zinc-800">
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          onClick={() => onChange(t.id)}
          className={`flex items-center gap-2 px-4 py-3 text-sm font-medium transition-colors border-b-2 -mb-px ${
            active === t.id
              ? "border-white text-white"
              : "border-transparent text-zinc-500 hover:text-zinc-300"
          }`}
        >
          {t.label}
          <span
            className={`text-xs tabular-nums px-1.5 py-0.5 rounded ${
              active === t.id
                ? "bg-zinc-700 text-zinc-200"
                : "bg-zinc-900 text-zinc-600"
            }`}
          >
            {t.count}
          </span>
        </button>
      ))}
    </div>
  );
}

// ─── Main client component ────────────────────────────────────────────────────

interface HomeClientProps {
  userDbs: DbRecord[];
  systemDbs: SystemDbRecord[];
  buckets: BucketRecord[];
  metrics: MetricsData | null;
}

export function HomeClient({
  userDbs,
  systemDbs,
  buckets,
  metrics,
}: HomeClientProps) {
  const router = useRouter();
  const [, startTransition] = useTransition();

  const [tab, setTab] = useState<Tab>("databases");
  const [query, setQuery] = useState("");
  const [showDbModal, setShowDbModal] = useState(false);
  const [showBucketModal, setShowBucketModal] = useState(false);

  // Clear search when switching tabs
  function handleTabChange(t: Tab) {
    setTab(t);
    setQuery("");
  }

  function handleCreated() {
    setShowDbModal(false);
    setShowBucketModal(false);
    startTransition(() => {
      router.refresh();
    });
  }

  const activeDbs = userDbs.filter((d) => d.status === "active");
  const activeBuckets = buckets.filter((b) => b.status === "active");

  const searchPlaceholder =
    tab === "databases"
      ? "Filter by name or owner…"
      : tab === "files"
      ? "Filter by name or user…"
      : "";

  return (
    <>
      {/* Volume bar is rendered in the layout header slot via a separate export */}
      <div className="flex-1 overflow-auto">
        <div className="max-w-6xl mx-auto w-full px-6 py-6">
          {/* Stats row */}
          {metrics && (
            <div className="flex items-center gap-6 mb-6">
              <div className="flex items-center gap-2">
                <span className="text-xs text-zinc-500">Volume</span>
                <div className="w-24 h-1 bg-zinc-800 rounded-full overflow-hidden">
                  <div
                    className={`h-full rounded-full ${
                      metrics.volume_used_percent > 85
                        ? "bg-red-500"
                        : metrics.volume_used_percent > 65
                        ? "bg-amber-500"
                        : "bg-emerald-500"
                    }`}
                    style={{ width: `${Math.min(metrics.volume_used_percent, 100)}%` }}
                  />
                </div>
                <span className="text-xs text-zinc-400 tabular-nums">
                  {formatBytes(metrics.volume_used_bytes)} / {formatBytes(metrics.volume_total_bytes)}
                </span>
              </div>
              <span className="text-zinc-800">·</span>
              <span className="text-xs text-zinc-500">
                <span className="text-zinc-300 font-medium">{activeDbs.length}</span>{" "}
                active {activeDbs.length === 1 ? "database" : "databases"}
              </span>
              {FILES_ENABLED && (
                <>
                  <span className="text-zinc-800">·</span>
                  <span className="text-xs text-zinc-500">
                    <span className="text-zinc-300 font-medium">{activeBuckets.length}</span>{" "}
                    active {activeBuckets.length === 1 ? "bucket" : "buckets"}
                  </span>
                </>
              )}
            </div>
          )}

          {/* Main panel */}
          <div className="border border-zinc-800 rounded-xl overflow-hidden bg-zinc-950">
            {/* Tab bar + toolbar */}
            <div className="flex items-center justify-between pr-4 border-b border-zinc-800">
              <TabBar
                active={tab}
                onChange={handleTabChange}
                dbCount={userDbs.length}
                bucketCount={FILES_ENABLED ? buckets.length : 0}
                systemCount={systemDbs.length}
              />

              <div className="flex items-center gap-2">
                {tab !== "system" && (
                  <SearchInput
                    value={query}
                    onChange={setQuery}
                    placeholder={searchPlaceholder}
                  />
                )}

                {tab === "databases" && (
                  <button
                    type="button"
                    onClick={() => setShowDbModal(true)}
                    className="flex items-center gap-1.5 text-xs font-medium bg-white text-zinc-950 px-3 py-1.5 rounded-md hover:bg-zinc-200 transition-colors whitespace-nowrap"
                  >
                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true" suppressHydrationWarning>
                      <path d="M6 1v10M1 6h10" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" suppressHydrationWarning />
                    </svg>
                    New database
                  </button>
                )}

                {FILES_ENABLED && tab === "files" && (
                  <button
                    type="button"
                    onClick={() => setShowBucketModal(true)}
                    className="flex items-center gap-1.5 text-xs font-medium bg-white text-zinc-950 px-3 py-1.5 rounded-md hover:bg-zinc-200 transition-colors whitespace-nowrap"
                  >
                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true" suppressHydrationWarning>
                      <path d="M6 1v10M1 6h10" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" suppressHydrationWarning />
                    </svg>
                    New bucket
                  </button>
                )}
              </div>
            </div>

            {/* Tab content */}
            {tab === "databases" && (
              <DbList
                dbs={userDbs}
                query={query}
                onNew={() => setShowDbModal(true)}
              />
            )}
            {FILES_ENABLED && tab === "files" && (
              <BucketList
                buckets={buckets}
                query={query}
                onNew={() => setShowBucketModal(true)}
              />
            )}
            {tab === "system" && <SystemList dbs={systemDbs} />}
          </div>
        </div>
      </div>

      {/* Modals */}
      {showDbModal && (
        <CreateDbModal
          onClose={() => setShowDbModal(false)}
          onCreated={handleCreated}
        />
      )}
      {FILES_ENABLED && showBucketModal && (
        <CreateBucketModal
          onClose={() => setShowBucketModal(false)}
          onCreated={handleCreated}
        />
      )}
    </>
  );
}
