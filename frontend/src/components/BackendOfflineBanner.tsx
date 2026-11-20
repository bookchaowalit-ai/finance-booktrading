'use client';

import { useEffect, useState } from 'react';
import { AlertTriangle } from 'lucide-react';
import { BackendState, RECHECK_INTERVAL_MS, backendOfflineText, checkBackend } from '@/services/backendStatus';

interface Props {
  lang: string;
  className?: string;
}

/**
 * States plainly that the backend is offline. Shows nothing while checking
 * or when the backend answers, so a healthy deployment looks unchanged.
 */
export default function BackendOfflineBanner({ lang, className = '' }: Props) {
  const [state, setState] = useState<BackendState>('checking');

  useEffect(() => {
    let cancelled = false;
    const probe = () =>
      checkBackend().then((s) => {
        if (!cancelled) setState(s);
      });
    probe();
    // Keep following the backend: hide the banner once it recovers, show it if it drops.
    const timer = setInterval(probe, RECHECK_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  const offline = state === 'offline';
  const { title, body } = backendOfflineText(lang);

  // The live region stays in the DOM (empty, no box) so screen readers
  // announce the warning when it appears, on load or mid-session.
  return (
    <div role="status" aria-live="polite" className={offline ? className : undefined}>
      {offline && (
        <div className="flex items-start gap-3 rounded-lg border border-amber-500/50 bg-amber-50 p-4 text-left text-amber-950 dark:bg-amber-950/40 dark:text-amber-100">
          <AlertTriangle aria-hidden="true" className="mt-0.5 h-5 w-5 shrink-0 text-amber-600 dark:text-amber-400" />
          <div className="leading-relaxed">
            <p className="font-semibold">{title}</p>
            <p className="text-sm">{body}</p>
          </div>
        </div>
      )}
    </div>
  );
}
