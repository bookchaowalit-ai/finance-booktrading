/**
 * Dashboard Layout
 * Provides consistent sidebar navigation across all dashboard pages
 */
'use client';

import { useState, useEffect, useCallback } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import Sidebar from '@/components/Sidebar';
import { ToastProvider } from '@/components/ui/Toast';
import ErrorBoundary from '@/components/ErrorBoundary';
import { ThemeProvider } from '@/contexts/ThemeProvider';
import { Bell, Menu, LogOut, Command, Globe } from 'lucide-react';
import { isAuthenticated, logout, clearSession } from '@/services/auth';
import CommandPalette from '@/components/CommandPalette';
import { useKeyboardShortcuts } from '@/hooks/useKeyboardShortcuts';
import { WSStatusIndicator } from '@/components/WSStatusIndicator';
import ThemeToggle from '@/components/ThemeToggle';
import { Dropdown } from '@/components/ui';
import NotificationCenter from '@/components/NotificationCenter';
import FillNotificationToast from '@/components/FillNotificationToast';
import ForbiddenNotice from '@/components/ForbiddenNotice';
import MobileBottomNav from '@/components/MobileBottomNav';
import KeyboardShortcutsHelp from '@/components/KeyboardShortcutsHelp';
import { api } from '@/services/api';
import { useTranslation } from '@/i18n/translations';

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const { t } = useTranslation();
  const [isSidebarCollapsed, setIsSidebarCollapsed] = useState(false);
  const [isMobileSidebarOpen, setIsMobileSidebarOpen] = useState(false);
  const [mounted, setMounted] = useState(false);
  const [isCommandPaletteOpen, setIsCommandPaletteOpen] = useState(false);
  const [unreadCount, setUnreadCount] = useState(0);

  // Extract locale from pathname, e.g. /en/dashboard → "en"
  const locale = pathname.split('/')[1] || 'th';

  const fetchUnreadCount = useCallback(async () => {
    try {
      const notifs = await api.getNotifications();
      setUnreadCount(notifs.filter((n: { read: boolean }) => !n.read).length);
    } catch {
      // ignore
    }
  }, []);

  // Validate session against backend every 2 minutes — catches token invalidated by server restart
  const validateSession = useCallback(async () => {
    if (!isAuthenticated()) return;
    try {
      const token = localStorage.getItem('auth_token');
      const res = await fetch('/api/auth/me', {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (res.status === 401) {
        clearSession();
        router.push(`/${locale}`);
      }
    } catch {
      // network error — don't force logout, just wait for next check
    }
  }, [locale, router]);

  useEffect(() => {
    setMounted(true);
    fetchUnreadCount();
    const interval = setInterval(fetchUnreadCount, 15000);

    // Validate token every 2 minutes to detect server-side invalidation (e.g. restart)
    const sessionInterval = setInterval(validateSession, 2 * 60 * 1000);
    return () => {
      clearInterval(interval);
      clearInterval(sessionInterval);
    };
  }, [fetchUnreadCount, validateSession]);

  useEffect(() => {
    // Only run auth check after component is mounted (client-side only)
    if (!mounted) return;

    // Check authentication after a small delay to ensure localStorage is ready
    const timer = setTimeout(() => {
      const authenticated = isAuthenticated();
      if (!authenticated) {
        console.log('[DashboardLayout] Not authenticated, redirecting to landing');
        router.push(`/${locale}`);
      } else {
        console.log('[DashboardLayout] Authenticated, staying on dashboard');
      }
    }, 50); // 50ms delay to ensure localStorage is ready

    return () => clearTimeout(timer);
  }, [mounted]); // Only depend on mounted to run once

  const handleLogout = async () => {
    await logout();
    router.push(`/${locale}`);
  };

  const handleLanguageChange = (newLocale: string) => {
    // Replace current locale in pathname with new locale
    const newPathname = pathname.replace(`/${locale}`, `/${newLocale}`);
    router.push(newPathname);
  };

  // Keyboard shortcuts - observe-first command center navigation
  useKeyboardShortcuts([
    { key: 'k', ctrl: true, action: () => setIsCommandPaletteOpen(true) },
    { key: 'b', ctrl: true, action: () => setIsSidebarCollapsed(!isSidebarCollapsed) },
    { key: 'g', ctrl: true, alt: true, action: () => router.push(`/${locale}/dashboard`) },
    { key: 'd', ctrl: true, alt: true, action: () => router.push(`/${locale}/dashboard/daily-report`) },
    { key: 'e', ctrl: true, alt: true, action: () => router.push(`/${locale}/dashboard/evidence`) },
    { key: 'r', ctrl: true, alt: true, action: () => router.push(`/${locale}/dashboard/research`) },
    { key: 's', ctrl: true, alt: true, action: () => router.push(`/${locale}/dashboard/system`) },
  ]);

  // Define available commands. Keep default command palette read-only.
  const commands = [
    { id: 'command-center', label: 'Open Command Center', action: () => router.push(`/${locale}/dashboard`), shortcut: 'Alt+Ctrl+G' },
    { id: 'daily-report', label: 'Open Daily Report', action: () => router.push(`/${locale}/dashboard/daily-report`), shortcut: 'Alt+Ctrl+D' },
    { id: 'evidence', label: 'Open Evidence', action: () => router.push(`/${locale}/dashboard/evidence`), shortcut: 'Alt+Ctrl+E' },
    { id: 'research', label: 'Open Research', action: () => router.push(`/${locale}/dashboard/research`), shortcut: 'Alt+Ctrl+R' },
    { id: 'system', label: 'Open System', action: () => router.push(`/${locale}/dashboard/system`), shortcut: 'Alt+Ctrl+S' },
    { id: 'toggle-theme', label: 'Toggle Dark Mode', action: () => document.documentElement.classList.toggle('dark'), shortcut: 'D' },
    { id: 'logout', label: 'Logout', action: handleLogout, shortcut: 'L' },
  ];

  if (!mounted) {
    return null;
  }

  return (
    <ThemeProvider>
      <ToastProvider>
        <div className="min-h-screen bg-gray-50 text-gray-950 dark:bg-gray-950 dark:text-white">
          {/* Sidebar */}
          <Sidebar
            isCollapsed={isSidebarCollapsed}
            onToggle={() => setIsSidebarCollapsed(!isSidebarCollapsed)}
            isMobileOpen={isMobileSidebarOpen}
            onMobileClose={() => setIsMobileSidebarOpen(false)}
          />

          {/* Main Content */}
          <main
            className={`min-h-screen transition-all duration-300 ${isSidebarCollapsed ? 'ml-20' : 'ml-72'
              }`}
          >
            {/* Top Navigation */}
            <nav className="sticky top-0 z-40 border-b border-gray-200/80 bg-white/90 backdrop-blur dark:border-gray-800 dark:bg-gray-950/85">
              <div className="flex h-14 items-center justify-between px-5">
                {/* Mobile menu button */}
                <button
                  onClick={() => setIsMobileSidebarOpen(true)}
                  className="lg:hidden text-gray-500 hover:text-gray-700 dark:text-gray-400"
                  aria-label="Open menu"
                >
                  <Menu className="w-6 h-6" />
                </button>

                {/* Page Title */}
                <div className="hidden lg:block">
                  <h2 className="text-sm font-semibold text-gray-950 dark:text-white">
                    {t('brand.name')}
                  </h2>
                  <p className="text-xs text-gray-500 dark:text-gray-400">
                    {t('brand.tagline')}
                  </p>
                </div>

                <div className="flex-1"></div>

                <div className="flex items-center gap-3">
                  {/* WebSocket Status */}
                  <WSStatusIndicator />

                  {/* Theme Toggle */}
                  <ThemeToggle />

                  {/* Language Switcher */}
                  <div className="flex items-center gap-2">
                    <Globe className="w-4 h-4 text-gray-500" />
                    <Dropdown
                      options={[
                        { value: 'en', label: '🇬🇧 English' },
                        { value: 'th', label: '🇹🇭 ไทย' },
                      ]}
                      value={locale}
                      onChange={handleLanguageChange}
                      size="sm"
                    />
                  </div>

                  {/* Command Palette Trigger */}
                  <KeyboardShortcutsHelp />
                  <button
                    onClick={() => setIsCommandPaletteOpen(true)}
                    className="hidden items-center gap-2 rounded-md border border-gray-200 bg-white px-2.5 py-1.5 text-xs text-gray-600 hover:bg-gray-50 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-400 dark:hover:bg-gray-800 sm:flex"
                    title="Find view (Ctrl+K)"
                  >
                    <Command className="w-3.5 h-3.5" />
                    <span>Find</span>
                    <kbd className="px-1.5 py-0.5 text-xs bg-gray-100 dark:bg-gray-800 rounded">
                      Ctrl+K
                    </kbd>
                  </button>

                  {/* Notifications */}
                  <button
                    className="text-gray-500 hover:text-gray-700 dark:text-gray-400 relative"
                    aria-label={`Notifications${unreadCount > 0 ? `, ${unreadCount} unread` : ''}`}
                  >
                    <Bell className="w-5 h-5" />
                    {unreadCount > 0 && (
                      <span className="absolute -top-1 -right-1 w-4 h-4 bg-red-500 rounded-full text-xs text-white flex items-center justify-center">
                        {unreadCount > 9 ? '9+' : unreadCount}
                      </span>
                    )}
                  </button>

                  {/* User Profile */}
                  <div className="flex items-center gap-3 pl-3 border-l border-gray-200 dark:border-gray-800">
                    <div className="w-8 h-8 bg-gray-900 dark:bg-white rounded-full flex items-center justify-center text-white dark:text-gray-900 font-semibold">
                      U
                    </div>
                    <button
                      onClick={handleLogout}
                      className="flex items-center gap-2 text-gray-500 hover:text-gray-700 dark:text-gray-400 text-sm font-medium"
                    >
                      <span className="hidden sm:inline">Logout</span>
                      <LogOut className="w-4 h-4" />
                    </button>
                  </div>
                </div>
              </div>
            </nav>

            {/* Page Content - Scrollable. Skip-link target: lands after the top nav, not on it. */}
            <div id="main-content" tabIndex={-1} className="p-4 pb-20 lg:p-6 outline-none">
              <ErrorBoundary>
                {children}
              </ErrorBoundary>
            </div>

            {/* Mobile Bottom Navigation */}
            <MobileBottomNav />
          </main>

          {/* Command Palette */}
          <CommandPalette
            isOpen={isCommandPaletteOpen}
            onClose={() => setIsCommandPaletteOpen(false)}
            commands={commands}
          />

          {/* Real Grid Fill Notifications */}
          <FillNotificationToast />

          {/* Thai notice when an admin-only route answers 403 */}
          <ForbiddenNotice />
        </div>
      </ToastProvider>
    </ThemeProvider>
  );
}
