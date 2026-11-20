import { afterEach, describe, expect, it, vi } from 'vitest';

import { authenticate, register, UNEXPECTED_ANSWER_ERROR, STORAGE_ERROR } from './auth';
import { backendOfflineMessage } from './backendStatus';

const realFetch = global.fetch;
afterEach(() => {
  global.fetch = realFetch;
  vi.restoreAllMocks();
  localStorage.clear();
});

const html200 = () => new Response('<!doctype html><title>fallback</title>', { status: 200, headers: { 'Content-Type': 'text/html' } });
const json200 = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
const goodSession = { token: 't0k', user: { id: '1', email: 'a@b.c', name: 'A', role: 'user' } };

describe.each([
  ['authenticate', () => authenticate('a@b.c', 'pw')],
  ['register', () => register('a@b.c', 'pw', 'A')],
])('%s session handling', (_name, call) => {
  it('reports an unexpected answer, not a storage error, for an HTML page served with 200', async () => {
    global.fetch = vi.fn().mockResolvedValue(html200());
    await expect(call()).resolves.toEqual({ success: false, error: UNEXPECTED_ANSWER_ERROR });
  });

  it('reports an unexpected answer, not a network error, for JSON null or a body without a token', async () => {
    global.fetch = vi.fn().mockResolvedValue(json200(null));
    await expect(call()).resolves.toEqual({ success: false, error: UNEXPECTED_ANSWER_ERROR });
    global.fetch = vi.fn().mockResolvedValue(json200({ user: goodSession.user }));
    await expect(call()).resolves.toEqual({ success: false, error: UNEXPECTED_ANSWER_ERROR });
  });

  it('reports a storage error only when saving the session fails', async () => {
    global.fetch = vi.fn().mockResolvedValue(json200(goodSession));
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('full', 'QuotaExceededError');
    });
    await expect(call()).resolves.toEqual({ success: false, error: STORAGE_ERROR });
  });

  it('reports the backend offline when the request itself fails', async () => {
    global.fetch = vi.fn().mockRejectedValue(new TypeError('fetch failed'));
    await expect(call()).resolves.toEqual({ success: false, error: backendOfflineMessage('th') });
  });

  it('saves a valid session', async () => {
    global.fetch = vi.fn().mockResolvedValue(json200(goodSession));
    await expect(call()).resolves.toEqual({ success: true, user: goodSession.user, token: 't0k' });
    expect(localStorage.getItem('auth_token')).toBe('t0k');
  });
});
