import { describe, it, expect, vi } from 'vitest';
import { fetchLiveStats } from './liveStats';

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as Response;
const fail = (status: number) => ({ ok: false, status, json: async () => ({}) }) as Response;

describe('fetchLiveStats', () => {
  it('returns null when every source fails, so the page shows no confirmed numbers', async () => {
    await expect(fetchLiveStats(vi.fn().mockResolvedValue(fail(502)))).resolves.toBeNull();
    await expect(fetchLiveStats(vi.fn().mockRejectedValue(new TypeError('fetch failed')))).resolves.toBeNull();
  });

  it('keeps a failed source unknown instead of reporting zero', async () => {
    const fetchImpl = vi.fn((url: string) =>
      Promise.resolve(url.includes('/api/paper/portfolio') ? ok({ total_pnl: 1.5, total_trades: 4, running: true }) : fail(502)),
    );
    const stats = await fetchLiveStats(fetchImpl as unknown as typeof fetch);
    expect(stats).toEqual({ paperPnl: 1.5, paperTrades: 4, polyPnl: null, polyTrades: null, arbScans: null, botsRunning: null });
  });

  it('counts running bots only when both bot sources answered', async () => {
    const fetchImpl = vi.fn((url: string) => {
      if (url.includes('/api/paper/portfolio')) return Promise.resolve(ok({ net_pnl: -2, trade_count: 1, running: false }));
      if (url.includes('/api/arb-paper/status')) return Promise.resolve(ok({ scan_count: 9, running: true }));
      return Promise.resolve(ok({ total_pnl: 0, total_trades: 0 }));
    });
    const stats = await fetchLiveStats(fetchImpl as unknown as typeof fetch);
    expect(stats).toEqual({ paperPnl: -2, paperTrades: 1, polyPnl: 0, polyTrades: 0, arbScans: 9, botsRunning: 1 });
  });
});
