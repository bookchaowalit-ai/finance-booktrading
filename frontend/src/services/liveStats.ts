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

const num = (...values: unknown[]): number => {
  for (const v of values) if (typeof v === 'number' && Number.isFinite(v)) return v;
  return 0;
};

/** Null when no source answered; otherwise stats with unknown fields left null. */
export async function fetchLiveStats(fetchImpl: typeof fetch = fetch): Promise<LiveStats | null> {
  const API_BASE = process.env.NEXT_PUBLIC_API_URL || '';
  const STRATEGY_API = process.env.NEXT_PUBLIC_STRATEGY_URL || '/strategy-api';
  const token = typeof window !== 'undefined' ? localStorage.getItem('auth_token') : null;
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const getJson = async (url: string): Promise<Json> => {
    const res = await fetchImpl(url, { headers });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    return (await res.json()) as Json;
  };

  const [paperRes, polyRes, arbRes] = await Promise.allSettled([
    getJson(`${API_BASE}/api/paper/portfolio`),
    getJson(`${STRATEGY_API}/api/poly-paper/performance`),
    getJson(`${STRATEGY_API}/api/arb-paper/status`),
  ]);
  const paper = paperRes.status === 'fulfilled' ? paperRes.value : null;
  const poly = polyRes.status === 'fulfilled' ? polyRes.value : null;
  const arb = arbRes.status === 'fulfilled' ? arbRes.value : null;
  if (!paper && !poly && !arb) return null;

  return {
    paperPnl: paper ? num(paper.total_pnl, paper.net_pnl) : null,
    paperTrades: paper ? num(paper.total_trades, paper.trade_count) : null,
    polyPnl: poly ? num(poly.total_pnl) : null,
    polyTrades: poly ? num(poly.total_trades) : null,
    arbScans: arb ? num(arb.scan_count) : null,
    botsRunning: paper && arb ? (arb.running ? 1 : 0) + (paper.running ? 1 : 0) : null,
  };
}
