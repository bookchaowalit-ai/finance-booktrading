/**
 * Self-hosted app fonts (ADR-0007 thai-font). Latin-first stack in app-theme.css: Inter draws Latin,
 * Noto Sans Thai draws Thai. next/font downloads both at build time and serves them from this origin.
 * Layouts put the variable classes on <html>, not <body>: the :root --font-sans reads these variables.
 */
import { Inter, Noto_Sans_Thai } from 'next/font/google';

export const inter = Inter({ subsets: ['latin'], variable: '--font-inter', display: 'swap' });

export const notoSansThai = Noto_Sans_Thai({
  subsets: ['thai', 'latin'],
  weight: ['400', '500', '600', '700', '800'],
  variable: '--font-noto-sans-thai',
  display: 'swap',
});

/** Class list for <html>: both font variables. */
export const fontClassName = `${inter.variable} ${notoSansThai.variable}`;
