import type { MetadataRoute } from 'next';

const ORIGIN = 'https://booktrading.bookchaowalit.com';

export default function robots(): MetadataRoute.Robots {
  return {
    rules: { userAgent: '*', allow: '/', disallow: ['/en/dashboard', '/th/dashboard', '/api'] },
    sitemap: `${ORIGIN}/sitemap.xml`,
  };
}
