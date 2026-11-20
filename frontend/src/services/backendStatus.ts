/**
 * Backend reachability.
 *
 * The frontend proxies /api/* to the Go backend (next.config.js rewrites).
 * When no backend is running, the proxy answers 5xx (502 DNS_HOSTNAME_NOT_FOUND
 * on Vercel) and every page looks broken for no stated reason. This module
 * lets the UI say plainly that the backend is offline instead.
 */

export type BackendState = 'checking' | 'online' | 'offline';

// Same base URL as auth.ts and api.ts: a direct backend when
// NEXT_PUBLIC_API_URL is set, otherwise the same-origin /api rewrite.
const HEALTH_URL = `${process.env.NEXT_PUBLIC_API_URL || ''}/api/health`;
const CHECK_TIMEOUT_MS = 5000;
export const RECHECK_INTERVAL_MS = 30000;

/** Any 5xx from the health route means the backend cannot serve. */
export function isBackendDownStatus(status: number): boolean {
  return status >= 500;
}

/**
 * Gateway answers (502/503/504) mean the request never reached a working
 * backend. A plain 500 from a handler such as login is that request failing,
 * not the backend being offline.
 */
export function isBackendUnreachableStatus(status: number): boolean {
  return status === 502 || status === 503 || status === 504;
}

/**
 * The offline message for a failed auth response, or null when the failure
 * is the request's own. 502/503/504 are gateway answers. A 500 is ambiguous:
 * the Next.js rewrite answers 500 when it cannot reach the upstream, and a
 * handler answers 500 when it fails, so ask the health route which it is.
 */
export async function offlineErrorForStatus(
  status: number,
  locale: string,
  fetchImpl: typeof fetch = fetch,
): Promise<string | null> {
  if (isBackendUnreachableStatus(status)) return backendOfflineMessage(locale);
  if (status === 500 && (await checkBackend(fetchImpl)) === 'offline') return backendOfflineMessage(locale);
  return null;
}

/** fetch() rejects with a TypeError when the network request itself fails. */
export function isNetworkError(err: unknown): boolean {
  return err instanceof TypeError;
}

/** One quick probe of the public health route: no retries, short timeout. */
export async function checkBackend(fetchImpl: typeof fetch = fetch): Promise<Exclude<BackendState, 'checking'>> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), CHECK_TIMEOUT_MS);
  try {
    const res = await fetchImpl(HEALTH_URL, { cache: 'no-store', signal: controller.signal });
    return isBackendDownStatus(res.status) ? 'offline' : 'online';
  } catch {
    return 'offline';
  } finally {
    clearTimeout(timer);
  }
}

const MESSAGES: Record<string, { title: string; body: string }> = {
  th: {
    title: 'เชื่อมต่อระบบหลังบ้านไม่ได้ตอนนี้',
    body: 'เข้าสู่ระบบและดูข้อมูลสดไม่ได้ชั่วคราว หน้านี้จึงยืนยันสถานะบอทและคำสั่งเทรดที่เปิดอยู่ไม่ได้',
  },
  en: {
    title: 'The backend cannot be reached right now',
    body: 'Sign-in and live data are unavailable for now, so this page cannot confirm the bot state or any open orders.',
  },
};

export function backendOfflineText(locale: string): { title: string; body: string } {
  return MESSAGES[locale] ?? MESSAGES.en;
}

/** One-line form for error slots such as the login form. */
export function backendOfflineMessage(locale: string): string {
  const { title, body } = backendOfflineText(locale);
  return `${title} — ${body}`;
}

/** Locale from the current URL (/th/... or /en/...), for code without a lang prop. */
export function currentLocale(): string {
  if (typeof window === 'undefined') return 'th';
  return window.location.pathname.split('/')[1] || 'th';
}
