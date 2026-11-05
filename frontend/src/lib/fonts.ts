/**
 * Self-hosted app fonts (ADR-0007 thai-font). Latin-first stack in app-theme.css: Inter draws Latin,
 * Noto Sans Thai draws Thai. Inter comes from next/font/google at build time; Noto Sans Thai is self-hosted from ./fonts.
 * Layouts put the variable classes on <html>, not <body>: the :root --font-sans reads these variables.
 */
import { Inter } from 'next/font/google';
import localFont from 'next/font/local';

export const inter = Inter({ subsets: ['latin'], variable: '--font-inter', display: 'swap' });

// Noto Sans Thai is a variable font committed under ./fonts (OFL, copied from web-starter), so the
// build does not depend on fetching it from Google Fonts.
export const notoSansThai = localFont({
  src: './fonts/NotoSansThai-Variable.woff2',
  weight: '100 900',
  variable: '--font-noto-sans-thai',
  display: 'swap',
});

/** Class list for <html>: both font variables. */
export const fontClassName = `${inter.variable} ${notoSansThai.variable}`;
