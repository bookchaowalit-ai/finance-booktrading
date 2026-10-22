import { describe, expect, it } from 'vitest';
import { t } from './LandingPageClient';

// The strategies are rule-based bots on technical indicators (strategy/app/predictor.py:
// moving-average crossover + RSI), so the landing copy must not promise AI or machine learning.
const OVERCLAIM = /\bAI\b|machine learning|ขั้นสูง/i;

describe('landing copy', () => {
  for (const lang of ['en', 'th']) {
    it(`does not claim AI or machine learning (${lang})`, () => {
      for (const key of ['hero_desc', 'feat1_title', 'feat1_desc', 'footer_desc']) {
        expect(t(lang, key)).not.toMatch(OVERCLAIM);
      }
    });
  }

  it('says what the bots actually do', () => {
    expect(t('en', 'hero_desc')).toMatch(/rule-based/i);
    expect(t('en', 'hero_desc')).toMatch(/paper/i);
    expect(t('th', 'hero_desc')).toMatch(/ตามกฎ/);
  });
});
