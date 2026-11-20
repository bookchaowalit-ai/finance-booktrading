/**
 * Backend reachability.
 *
 * The frontend proxies /api/* to the Go backend (next.config.js rewrites).
 * When no backend is running, the proxy answers 5xx (502 DNS_HOSTNAME_NOT_FOUND
 * on Vercel) and every page looks broken for no stated reason. This module
 * lets the UI say plainly that the backend is offline instead.
 */

export type BackendState = 'checking' | 'online' | 'offline';

const HEALTH_PATH = '/api/health';
const CHECK_TIMEOUT_MS = 5000;

/** A 5xx from the proxy or the backend means the backend cannot serve. */
export function isBackendDownStatus(status: number): boolean {
  return status >= 500;
}

/** One quick probe of the public health route: no retries, short timeout. */
export async function checkBackend(fetchImpl: typeof fetch = fetch): Promise<Exclude<BackendState, 'checking'>> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), CHECK_TIMEOUT_MS);
  try {
    const res = await fetchImpl(HEALTH_PATH, { cache: 'no-store', signal: controller.signal });
    return isBackendDownStatus(res.status) ? 'offline' : 'online';
  } catch {
    return 'offline';
  } finally {
    clearTimeout(timer);
  }
}

const MESSAGES: Record<string, { title: string; body: string }> = {
  th: {
    title: 'ระบบหลังบ้านปิดอยู่ตอนนี้',
    body: 'เข้าสู่ระบบและดูข้อมูลสดไม่ได้ชั่วคราว บอทเทรดหยุดอยู่ตามโหมดปกป้องเงินทุน และไม่มีเงินจริงถูกใช้',
  },
  en: {
    title: 'The backend is offline right now',
    body: 'Sign-in and live data are unavailable for now. The trading bot is halted in capital-protection mode and no real money is in use.',
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
