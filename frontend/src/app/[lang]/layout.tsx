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
