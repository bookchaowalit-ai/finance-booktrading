import { describe, it, expect, vi, afterEach } from 'vitest';
import { checkBackend, backendOfflineMessage, isBackendDownStatus, isBackendUnreachableStatus, offlineErrorForStatus, isNetworkError } from './backendStatus';

const res = (status: number, body: unknown = { status: 'healthy' }) =>
  ({ status, ok: status >= 200 && status < 300, json: async () => body }) as Response;

afterEach(() => vi.unstubAllEnvs());

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
  it('is online when /api/health answers the backend health body', async () => {
    vi.stubEnv('NEXT_PUBLIC_API_URL', '');
    const fetchImpl = vi.fn().mockResolvedValue(res(200));
    await expect(checkBackend(fetchImpl)).resolves.toBe('online');
    expect(fetchImpl).toHaveBeenCalledWith('/api/health', expect.objectContaining({ cache: 'no-store' }));
  });

  it('calls the backend directly when NEXT_PUBLIC_API_URL is set', async () => {
    vi.stubEnv('NEXT_PUBLIC_API_URL', 'http://localhost:8080');
    const fetchImpl = vi.fn().mockResolvedValue(res(200));
    await checkBackend(fetchImpl);
    expect(fetchImpl).toHaveBeenCalledWith('http://localhost:8080/api/health', expect.anything());
  });

  it('is offline when something other than the backend answers (proxy 404, HTML fallback)', async () => {
    await expect(checkBackend(vi.fn().mockResolvedValue(res(404, {})))).resolves.toBe('offline');
    const html = { status: 200, ok: true, json: async () => { throw new SyntaxError('Unexpected token <'); } } as unknown as Response;
    await expect(checkBackend(vi.fn().mockResolvedValue(html))).resolves.toBe('offline');
    await expect(checkBackend(vi.fn().mockResolvedValue(res(200, { status: 'ok' })))).resolves.toBe('offline');
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

describe('offlineErrorForStatus', () => {
  it('reports offline for a gateway answer without probing', async () => {
    const fetchImpl = vi.fn();
    await expect(offlineErrorForStatus(502, 'th', fetchImpl)).resolves.toBe(backendOfflineMessage('th'));
    expect(fetchImpl).not.toHaveBeenCalled();
  });
  it('reports offline for a proxy 500 when the health route is also down', async () => {
    await expect(offlineErrorForStatus(500, 'en', vi.fn().mockResolvedValue(res(500)))).resolves.toBe(backendOfflineMessage('en'));
  });
  it('leaves a handler 500 to the form when the health route answers', async () => {
    await expect(offlineErrorForStatus(500, 'en', vi.fn().mockResolvedValue(res(200)))).resolves.toBeNull();
  });
  it('leaves client errors alone', async () => {
    await expect(offlineErrorForStatus(401, 'en', vi.fn())).resolves.toBeNull();
  });
});

describe('isNetworkError', () => {
  it('counts only fetch network failures', () => {
    expect(isNetworkError(new TypeError('fetch failed'))).toBe(true);
    expect(isNetworkError(new DOMException('quota', 'QuotaExceededError'))).toBe(false);
    expect(isNetworkError(new SyntaxError('bad json'))).toBe(false);
  });
});
