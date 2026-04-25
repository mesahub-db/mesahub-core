export const dynamic = "force-dynamic";

import { FILES_ENABLED } from "@/lib/features";
import { goFetchAdmin } from "@/lib/go-api";
import { HomeClient } from "./home-client";

export interface DbRecord {
  id: number;
  name: string;
  owner: string;
  status: string;
  created_at: string;
  size_bytes?: number;
}

export interface BucketRecord {
  id: string;
  name: string;
  display_name: string;
  description: string | null;
  status: string;
  size_bytes: number;
  created_at: string;
}

export interface MetricsData {
  volume_used_bytes: number;
  volume_total_bytes: number;
  volume_used_percent: number;
}

export interface SystemDbRecord {
  name: string;
  label: string;
  description: string;
  size_bytes: number;
}

let systemDbCache: { expiresAt: number; value: SystemDbRecord[] } | null = null;

async function getSystemDbs(): Promise<SystemDbRecord[]> {
  if (systemDbCache && Date.now() < systemDbCache.expiresAt) {
    return systemDbCache.value;
  }

  const res = await goFetchAdmin("/api/system/dbs").catch(() => null);
  const raw: unknown[] = res?.ok ? await res.json().catch(() => []) : [];
  const value: SystemDbRecord[] = (raw as Record<string, unknown>[]).map((r) => ({
    name: String(r.name ?? ""),
    label: String(r.label ?? ""),
    description: String(r.description ?? "System database"),
    size_bytes: Number(r.size_bytes ?? 0),
  }));

  systemDbCache = {
    value,
    expiresAt: Date.now() + 60_000,
  };

  return value;
}

export default async function DashboardPage() {
  const [dbsRes, metricsRes, bucketsRes, systemDbs] = await Promise.all([
    goFetchAdmin("/api/db").catch(() => null),
    goFetchAdmin("/api/metrics").catch(() => null),
    FILES_ENABLED ? goFetchAdmin("/api/buckets").catch(() => null) : Promise.resolve(null),
    getSystemDbs(),
  ]);

  const allDbs: DbRecord[] = dbsRes?.ok ? await dbsRes.json().catch(() => []) : [];
  const metrics: MetricsData | null = metricsRes?.ok
    ? await metricsRes.json().catch(() => null)
    : null;
  const bucketsRaw: unknown[] = bucketsRes?.ok ? await bucketsRes.json().catch(() => []) : [];

  const buckets: BucketRecord[] = (bucketsRaw as Record<string, unknown>[]).map((r) => ({
    id: String(r.id ?? ""),
    name: String(r.name ?? ""),
    display_name: String(r.display_name ?? ""),
    description: r.description ? String(r.description) : null,
    status: String(r.status ?? ""),
    size_bytes: Number(r.size_bytes ?? 0),
    created_at: String(r.created_at ?? ""),
  }));

  const systemDbNames = new Set(systemDbs.map((d) => d.name));
  const userDbs = allDbs.filter((d) => !systemDbNames.has(d.name));

  return (
    <HomeClient
      userDbs={userDbs}
      systemDbs={systemDbs}
      buckets={buckets}
      metrics={metrics}
    />
  );
}


