import { readFileSync } from 'node:fs';
import { describe, expect, it, vi } from 'vitest';
import robots from './robots';
import sitemap from './sitemap';

// next/font only works inside a Next build; the metadata needs none of it.
vi.mock('next/font/google', () => ({ Inter: () => ({ className: 'f', variable: 'v' }) }));
vi.mock('next/font/local', () => ({ default: () => ({ className: 'f', variable: 'v' }) }));
const { metadata } = await import('./[lang]/layout');

// Interface coverage (ADR-0015 §1.2, product): SEO basics and footer links to privacy and status.
describe('SEO basics and legal links', () => {
  it('has Open Graph metadata', () => {
    expect(metadata.openGraph?.title).toBeTruthy();
    expect(metadata.openGraph?.description).toBeTruthy();
  });

  it('robots.txt points at a sitemap of both locales and keeps the dashboard out', () => {
    const r = robots();
    expect(r.sitemap).toBe('https://booktrading.bookchaowalit.com/sitemap.xml');
    const rules = Array.isArray(r.rules) ? r.rules[0] : r.rules;
    expect(rules.disallow).toEqual(expect.arrayContaining(['/en/dashboard', '/th/dashboard']));
    expect(sitemap().map((e) => e.url)).toEqual(['https://booktrading.bookchaowalit.com/en', 'https://booktrading.bookchaowalit.com/th']);
  });

  it('the landing footer links privacy and the status page', () => {
    const src = readFileSync('src/app/[lang]/LandingPageClient.tsx', 'utf8');
    expect(src).toContain('href="https://bookchaowalit.com/privacy"');
    expect(src).toContain('href="https://monitor.bookchaowalit.com/"');
  });
});
