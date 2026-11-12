/**
 * Locale Layout - Dynamic [lang] route
 */
import { notFound } from 'next/navigation';
import { fontClassName } from '@/lib/fonts';
import '../globals.css';

const supportedLocales = ['en', 'th'];

export function generateStaticParams() {
  return supportedLocales.map((locale) => ({ lang: locale }));
}

export const metadata = {
  title: 'BookFinance',
  description: 'Rule-based, paper-first crypto trading bots',
  metadataBase: new URL('https://booktrading.bookchaowalit.com'),
  openGraph: {
    type: 'website',
    siteName: 'BookFinance',
    title: 'BookFinance — rule-based, paper-first crypto trading bots',
    description: 'Rule-based trading bots that run on paper first, with kill switches before any real execution.',
  },
};

interface LocaleLayoutProps {
  children: React.ReactNode;
  params: Promise<{ lang: string }>;
}

export default async function LocaleLayout({
  children,
  params,
}: LocaleLayoutProps) {
  const { lang } = await params;

  if (!supportedLocales.includes(lang)) {
    notFound();
  }

  return (
    <html lang={lang} className={fontClassName}>
      <head />
      <body>
        <a className="skip-link" href="#main-content">
          {lang === 'th' ? 'ข้ามไปเนื้อหาหลัก' : 'Skip to main content'}
        </a>
        {children}
      </body>
    </html>
  );
}
