import { describe, it, expect, vi } from 'vitest';
import { checkBackend, backendOfflineMessage, isBackendDownStatus, isBackendUnreachableStatus } from './backendStatus';

const res = (status: number) => ({ status, ok: status >= 200 && status < 300 }) as Response;

describe('isBackendDownStatus', () => {
  it('treats gateway and server errors as down', () => {
    for (const s of [500, 502, 503, 504]) expect(isBackendDownStatus(s)).toBe(true);
  });
  it('treats success and client errors as up', () => {
    for (const s of [200, 204, 401, 403, 404, 429]) expect(isBackendDownStatus(s)).toBe(false);
  });
});

describe('isBackendUnreachableStatus', () => {
  it('only counts gateway answers, not a handler 500', () => {
    for (const s of [502, 503, 504]) expect(isBackendUnreachableStatus(s)).toBe(true);
    for (const s of [200, 401, 500]) expect(isBackendUnreachableStatus(s)).toBe(false);
  });
});

describe('checkBackend', () => {
  it('is online when /api/health answers 200', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(res(200));
    await expect(checkBackend(fetchImpl)).resolves.toBe('online');
    expect(fetchImpl).toHaveBeenCalledWith('/api/health', expect.objectContaining({ cache: 'no-store' }));
  });

  it('is offline when the proxy answers 502 (no backend host)', async () => {
    await expect(checkBackend(vi.fn().mockResolvedValue(res(502)))).resolves.toBe('offline');
  });

  it('is offline when the request fails or times out', async () => {
    await expect(checkBackend(vi.fn().mockRejectedValue(new TypeError('fetch failed')))).resolves.toBe('offline');
  });
});

describe('backendOfflineMessage', () => {
  it('says plainly in Thai that the backend cannot be reached, without claiming the trading state', () => {
    const msg = backendOfflineMessage('th');
    expect(msg).toContain('เชื่อมต่อระบบหลังบ้านไม่ได้');
    expect(msg).toContain('ยืนยันสถานะบอท');
    expect(msg).not.toContain('ไม่มีเงินจริง');
  });
  it('has an English version and falls back to it for unknown locales', () => {
    expect(backendOfflineMessage('en')).toContain('cannot be reached');
    expect(backendOfflineMessage('en')).not.toMatch(/no real money/i);
    expect(backendOfflineMessage('xx')).toBe(backendOfflineMessage('en'));
  });
});
