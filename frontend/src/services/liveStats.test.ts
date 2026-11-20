import { describe, it, expect, vi } from 'vitest';
import { fetchLiveStats } from './liveStats';

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as Response;
const fail = (status: number) => ({ ok: false, status, json: async () => ({}) }) as Response;

const route = (answers: Record<string, Response>) =>
  vi.fn((url: string) => {
    for (const [path, res] of Object.entries(answers)) if (url.includes(path)) return Promise.resolve(res);
    return Promise.resolve(fail(502));
  }) as unknown as typeof fetch;

describe('fetchLiveStats', () => {
  it('returns null when every source fails, so the page shows no confirmed numbers', async () => {
    await expect(fetchLiveStats(vi.fn().mockResolvedValue(fail(502)))).resolves.toBeNull();
    await expect(fetchLiveStats(vi.fn().mockRejectedValue(new TypeError('fetch failed')))).resolves.toBeNull();
  });

  it('returns null when sources answer without any usable figure', async () => {
    const fetchImpl = route({ '/api/paper/portfolio': ok({}), '/api/poly-paper/performance': ok(null), '/api/arb-paper/status': ok({ running: true }) });
    await expect(fetchLiveStats(fetchImpl)).resolves.toBeNull();
  });

  it('keeps a failed source and a missing field unknown instead of reporting zero', async () => {
    // The real /api/arb-paper/status has no scan_count.
    const fetchImpl = route({
      '/api/paper/portfolio': ok({ total_pnl: 1.5, total_trades: 4 }),
      '/api/arb-paper/status': ok({ running: true, total_trades: 2 }),
    });
    await expect(fetchLiveStats(fetchImpl)).resolves.toEqual({
      paperPnl: 1.5, paperTrades: 4, polyPnl: null, polyTrades: null, arbScans: null, botsRunning: null,
    });
  });

  it('keeps zero when the source really reports zero', async () => {
    const fetchImpl = route({ '/api/poly-paper/performance': ok({ total_pnl: 0, total_trades: 0 }) });
    const stats = await fetchLiveStats(fetchImpl);
    expect(stats?.polyPnl).toBe(0);
    expect(stats?.polyTrades).toBe(0);
  });

  it('counts running bots only when all three sources report a running flag', async () => {
    const two = route({
      '/api/paper/portfolio': ok({ total_pnl: 1, running: true }),
      '/api/arb-paper/status': ok({ running: true }),
      '/api/poly-paper/performance': ok({ total_pnl: 0 }),
    });
    expect((await fetchLiveStats(two))?.botsRunning).toBeNull();
    const three = route({
      '/api/paper/portfolio': ok({ total_pnl: 1, running: true }),
      '/api/arb-paper/status': ok({ running: false }),
      '/api/poly-paper/performance': ok({ total_pnl: 0, running: true }),
    });
    expect((await fetchLiveStats(three))?.botsRunning).toBe(2);
  });
});
