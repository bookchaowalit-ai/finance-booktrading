// Tailwind v3 mapping of the ADR-0007 design floor (vendored in src/app/design-floor, unchanged).
// darkMode stays "class": the app toggles the `dark` class (ThemeProvider), and the floor snippet's
// data-theme selector would silently disable 90+ existing dark: utilities.
const floor = require('./src/app/design-floor/tailwind/v3.config.snippet.js');

/** Thai line-heights for every Tailwind text size (floor: body >= 1.6, headings >= 1.3). */
const thaiFontSize = {
  xs: ['0.75rem', { lineHeight: 'var(--leading-thai)' }],
  sm: ['0.875rem', { lineHeight: 'var(--leading-thai)' }],
  base: ['1rem', { lineHeight: 'var(--leading-thai)' }],
  lg: ['1.125rem', { lineHeight: 'var(--leading-thai)' }],
  xl: ['1.25rem', { lineHeight: 'var(--leading-thai)' }],
  '2xl': ['1.5rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '3xl': ['1.875rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '4xl': ['2.25rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '5xl': ['3rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '6xl': ['3.75rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '7xl': ['4.5rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '8xl': ['6rem', { lineHeight: 'var(--leading-thai-heading)' }],
  '9xl': ['8rem', { lineHeight: 'var(--leading-thai-heading)' }],
};

/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    './src/pages/**/*.{js,ts,jsx,tsx,mdx}',
    './src/components/**/*.{js,ts,jsx,tsx,mdx}',
    './src/app/**/*.{js,ts,jsx,tsx,mdx}',
  ],
  darkMode: 'class',
  theme: {
    extend: {
      ...floor.theme.extend,
      fontSize: thaiFontSize,
      colors: {
        ...floor.theme.extend.colors,
        primary: {
          50: '#f0f9ff',
          100: '#e0f2fe',
          200: '#bae6fd',
          300: '#7dd3fc',
          400: '#38bdf8',
          500: '#0ea5e9',
          600: '#0284c7',
          700: '#0369a1',
          800: '#075985',
          900: '#0c4a6e',
        },
      },
    },
  },
  plugins: [],
};
