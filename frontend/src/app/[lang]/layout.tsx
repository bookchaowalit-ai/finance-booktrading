/**
 * Locale Layout - Dynamic [lang] route
 */
import { notFound } from 'next/navigation';
import { Inter } from 'next/font/google';
import '../globals.css';

const inter = Inter({ subsets: ['latin'] });

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
    <html lang={lang}>
      <head />
      <body className={inter.className}>{children}</body>
    </html>
  );
}
