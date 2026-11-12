import type { MetadataRoute } from 'next';

const ORIGIN = 'https://booktrading.bookchaowalit.com';

export default function sitemap(): MetadataRoute.Sitemap {
  return ['en', 'th'].map((lang) => ({ url: `${ORIGIN}/${lang}`, changeFrequency: 'monthly' as const, priority: 1 }));
}
