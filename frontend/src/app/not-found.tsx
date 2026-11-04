/**
 * Root not-found: a URL outside /en and /th has no locale, so this page sets its own <html lang>.
 * Without it Next renders a default 404 document with no lang attribute (ADR-0007 lang rule).
 */
import Link from 'next/link';
import { fontClassName } from '@/lib/fonts';

export const metadata = {
  title: 'BookFinance: 404',
};

export default function RootNotFound() {
  return (
    <html lang="en" className={fontClassName}>
      <body>
        <a className="skip-link" href="#main-content">
          Skip to main content
        </a>
        {/* Own background: globals.css paints a light gradient on body, which would sit behind the text. */}
        <main id="main-content" className="flex min-h-screen items-center justify-center bg-bg px-6 text-fg">
          <div className="max-w-xl text-center">
            <h1 className="mb-4 text-5xl font-extrabold">404</h1>
            <p className="mb-2 text-lg text-fg-muted">Page not found</p>
            <p className="mb-8 text-lg text-fg-muted">
              <span lang="th">ไม่พบหน้าที่คุณต้องการ</span>
            </p>
            <Link
              href="/en"
              className="inline-flex min-h-11 items-center rounded-lg bg-accent px-6 py-3 font-semibold text-accent-fg"
            >
              Back to home
              <span lang="th" className="ml-2">
                / กลับหน้าแรก
              </span>
            </Link>
          </div>
        </main>
      </body>
    </html>
  );
}
