/**
 * Authentication Service
 * Connects to backend /api/auth endpoints
 */

import { backendOfflineMessage, currentLocale, isNetworkError, offlineErrorForStatus } from './backendStatus';

const API_BASE_URL = process.env.NEXT_PUBLIC_API_URL || '';

export interface User {
  id: string;
  email: string;
  name: string;
  role: string;
}

export interface AuthResult {
  success: boolean;
  user?: User;
  token?: string;
  error?: string;
}

/**
 * Authenticate user via backend API
 */
export async function authenticate(email: string, password: string): Promise<AuthResult> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const response = await fetch(`${API_BASE_URL}/api/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
      signal: controller.signal,
    });
    clearTimeout(timeout);

    const offline = await offlineErrorForStatus(response.status, currentLocale());
    if (offline) {
      return { success: false, error: offline };
    }

    if (!response.ok) {
      const fallback = response.status >= 500 ? 'Login failed. Please try again.' : 'Invalid email or password';
      const data = await response.json().catch(() => ({ error: fallback }));
      return { success: false, error: data.error || fallback };
    }

    const data: { token: string; user: User } = await response.json();

    localStorage.setItem('auth_token', data.token);
    localStorage.setItem('user', JSON.stringify(data.user));

    return { success: true, user: data.user, token: data.token };
  } catch (err) {
    clearTimeout(timeout);
    if (err instanceof Error && err.name === 'AbortError') {
      return { success: false, error: 'Request timed out. Please try again.' };
    }
    if (isNetworkError(err)) {
      return { success: false, error: backendOfflineMessage(currentLocale()) };
    }
    // The backend answered; the browser failed afterwards (e.g. storage full).
    return { success: false, error: 'The server answered, but this browser could not save the session. Please try again.' };
  }
}

export interface AuthConfig {
  /** Whether the sign-up form should be offered at all. */
  registrationOpen: boolean;
  /** Whether sign-up needs the operator's invite code. */
  inviteRequired: boolean;
}

/** Fail closed: hide sign-up when the backend cannot say it is open. */
export const CLOSED_AUTH_CONFIG: AuthConfig = { registrationOpen: false, inviteRequired: false };

/**
 * Read the public registration policy (GET /api/auth/config). The backend
 * closes sign-up by default in production (ALLOW_REGISTRATION /
 * REGISTRATION_INVITE_CODE); any error or unexpected payload counts as closed.
 */
export async function getAuthConfig(): Promise<AuthConfig> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const response = await fetch(`${API_BASE_URL}/api/auth/config`, { signal: controller.signal });
    if (!response.ok) return CLOSED_AUTH_CONFIG;
    const data: unknown = await response.json();
    if (!data || typeof data !== 'object') return CLOSED_AUTH_CONFIG;
    const { registrationOpen, inviteRequired } = data as Partial<AuthConfig>;
    return {
      registrationOpen: registrationOpen === true,
      inviteRequired: inviteRequired === true,
    };
  } catch {
    return CLOSED_AUTH_CONFIG;
  } finally {
    clearTimeout(timeout);
  }
}

/** Thai messages for the backend's 403 reasons on /api/auth/register. */
export const REGISTRATION_ERRORS_TH: Record<string, string> = {
  registration_closed: 'เซิร์ฟเวอร์นี้ปิดการสมัครสมาชิก — โปรดติดต่อผู้ดูแลระบบเพื่อขอบัญชี',
  invite_invalid: 'รหัสเชิญไม่ถูกต้อง — โปรดตรวจสอบรหัสเชิญจากผู้ดูแลระบบ',
};

/**
 * Register a new user via backend API. inviteCode is sent only when the
 * server is invite-only.
 */
export async function register(
  email: string,
  password: string,
  name: string,
  inviteCode?: string,
): Promise<AuthResult> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const body: Record<string, string> = { email, password, name };
    if (inviteCode) body.inviteCode = inviteCode;
    const response = await fetch(`${API_BASE_URL}/api/auth/register`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: controller.signal,
    });
    clearTimeout(timeout);

    const offline = await offlineErrorForStatus(response.status, currentLocale());
    if (offline) {
      return { success: false, error: offline };
    }

    if (!response.ok) {
      const data = await response.json().catch(() => ({ error: 'Registration failed' }));
      if (response.status === 403) {
        return {
          success: false,
          error: REGISTRATION_ERRORS_TH[data?.code] ?? REGISTRATION_ERRORS_TH.registration_closed,
        };
      }
      return { success: false, error: data.error || 'Registration failed' };
    }

    const data: { token: string; user: User } = await response.json();

    localStorage.setItem('auth_token', data.token);
    localStorage.setItem('user', JSON.stringify(data.user));

    return { success: true, user: data.user, token: data.token };
  } catch (err) {
    clearTimeout(timeout);
    if (err instanceof Error && err.name === 'AbortError') {
      return { success: false, error: 'Request timed out. Please try again.' };
    }
    if (isNetworkError(err)) {
      return { success: false, error: backendOfflineMessage(currentLocale()) };
    }
    // The backend answered; the browser failed afterwards (e.g. storage full).
    return { success: false, error: 'The server answered, but this browser could not save the session. Please try again.' };
  }
}

/**
 * Get current authenticated user (from localStorage)
 */
export function getCurrentUser(): User | null {
  if (typeof window === 'undefined') return null;
  const userStr = localStorage.getItem('user');
  if (userStr) {
    try {
      return JSON.parse(userStr);
    } catch {
      return null;
    }
  }
  return null;
}

/**
 * Whether a user may use admin-only strategy controls (kill switch, config
 * writes, backtests, tracker writes). The backend enforces this with a 403;
 * the UI only hides/disables controls so non-admins are not offered them.
 */
export function isAdmin(user: Pick<User, 'role'> | null | undefined = getCurrentUser()): boolean {
  return user?.role === 'admin';
}

/**
 * Check if user is authenticated
 */
export function isAuthenticated(): boolean {
  if (typeof window === 'undefined') return false;
  return !!localStorage.getItem('auth_token');
}

/**
 * Logout user
 */
export async function logout(): Promise<void> {
  const token = localStorage.getItem('auth_token');
  if (token) {
    await fetch(`${API_BASE_URL}/api/auth/logout`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
    }).catch(() => {});
  }
  localStorage.removeItem('auth_token');
  localStorage.removeItem('user');
}

/**
 * Get the stored auth token
 */
export function getAuthToken(): string | null {
  if (typeof window === 'undefined') return null;
  return localStorage.getItem('auth_token');
}

/**
 * Clear local session (without calling backend logout).
 * Used when a 401 is detected mid-session.
 */
export function clearSession(): void {
  localStorage.removeItem('auth_token');
  localStorage.removeItem('user');
}
