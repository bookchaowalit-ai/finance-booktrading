/**
 * Landing-page bot stats. Each field is null when its source did not answer,
 * so a failed request is shown as unknown, never as a confirmed zero.
 */
export interface LiveStats {
  paperPnl: number | null;
  paperTrades: number | null;
  polyPnl: number | null;
  polyTrades: number | null;
  arbScans: number | null;
  botsRunning: number | null;
}

type Json = Record<string, unknown>;

/** First finite number among the candidates; null when the source did not send one. */
const pick = (...values: unknown[]): number | null => {
  for (const v of values) if (typeof v === 'number' && Number.isFinite(v)) return v;
  return null;
};

const asObject = (v: unknown): Json | null => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Json) : null);

/**
 * Null when no source gave a usable figure; otherwise stats with unknown fields
 * left null. A missing field is unknown, never 0, and the running-bot count
 * needs a boolean running flag from all three bots.
 */
export async function fetchLiveStats(fetchImpl: typeof fetch = fetch): Promise<LiveStats | null> {
  const API_BASE = process.env.NEXT_PUBLIC_API_URL || '';
  const STRATEGY_API = process.env.NEXT_PUBLIC_STRATEGY_URL || '/strategy-api';
  const token = typeof window !== 'undefined' ? localStorage.getItem('auth_token') : null;
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const getJson = async (url: string): Promise<unknown> => {
    const res = await fetchImpl(url, { headers });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    return res.json();
  };

  const [paperRes, polyRes, arbRes] = await Promise.allSettled([
    getJson(`${API_BASE}/api/paper/portfolio`),
    getJson(`${STRATEGY_API}/api/poly-paper/performance`),
    getJson(`${STRATEGY_API}/api/arb-paper/status`),
  ]);
  const paper = paperRes.status === 'fulfilled' ? asObject(paperRes.value) : null;
  const poly = polyRes.status === 'fulfilled' ? asObject(polyRes.value) : null;
  const arb = arbRes.status === 'fulfilled' ? asObject(arbRes.value) : null;

  const flags = [paper?.running, poly?.running, arb?.running];
  const stats: LiveStats = {
    paperPnl: pick(paper?.total_pnl, paper?.net_pnl),
    paperTrades: pick(paper?.total_trades, paper?.trade_count),
    polyPnl: pick(poly?.total_pnl),
    polyTrades: pick(poly?.total_trades),
    arbScans: pick(arb?.scan_count),
    botsRunning: flags.every((f) => typeof f === 'boolean') ? flags.filter(Boolean).length : null,
  };
  return Object.values(stats).some((v) => v !== null) ? stats : null;
}
