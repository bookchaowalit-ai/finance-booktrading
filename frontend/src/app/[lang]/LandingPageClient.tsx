'use client';

import { useState, useEffect } from 'react';
import Link from 'next/link';
import {
  TrendingUp,
  Bot,
  BarChart3,
  Shield,
  Zap,
  Globe,
  ChevronRight,
  Mail,
  Phone,
  MapPin,
  Twitter,
  Github,
  Menu,
  X,
} from 'lucide-react';
import LoginModal from '@/components/LoginModal';
import LanguageSelector from '@/components/LanguageSelector';

// ─── Translations ─────────────────────────────────────────────────────────────
export const t = (lang: string, key: string): string => {
  const translations: Record<string, Record<string, string>> = {
    en: {
      nav_features: 'Features',
      nav_news: 'News',
      nav_contact: 'Contact',
      nav_login: 'Login',
      nav_dashboard: 'Go to Dashboard',
      hero_badge: 'Automated Trading System',
      hero_title: 'Trade Smarter,',
      hero_title2: 'Not Harder',
      hero_desc:
        'TradeBot Pro runs rule-based grid, DCA and trend bots on technical indicators (moving averages, RSI, ADX). It starts in observe-only paper mode, and a kill switch halts trading when risk limits are hit.',
      hero_cta: 'Get Started Free',
      hero_cta2: 'View Dashboard',
      features_title: 'Why TradeBot Pro?',
      features_subtitle: 'Everything you need to trade like a professional',
      feat1_title: 'Rule-based Strategies',
      feat1_desc: 'Grid trading, DCA and trend-following bots with clear entry and exit rules you can backtest and paper-trade first.',
      feat2_title: 'Real-time Analytics',
      feat2_desc: 'Live charts, technical indicators, and portfolio tracking updated every second.',
      feat3_title: 'Bank-grade Security',
      feat3_desc: 'Your API keys are encrypted and never stored on our servers. Trade with confidence.',
      feat4_title: 'Lightning Fast',
      feat4_desc: 'Sub-millisecond order execution connected directly to Binance exchange.',
      feat5_title: 'Multi-language',
      feat5_desc: 'Full support for English and Thai with more languages coming soon.',
      feat6_title: 'Risk Management',
      feat6_desc: 'Built-in stop-loss, take-profit, and position sizing to protect your capital.',
      news_title: 'Latest Updates',
      news_subtitle: 'Stay up to date with TradeBot Pro',
      news1_date: 'March 2026',
      news1_title: 'Grid Trading Strategy v2.0 Released',
      news1_desc: 'Major improvements to the grid trading algorithm with smarter price range detection and auto-rebalancing.',
      news2_date: 'February 2026',
      news2_title: 'New Dashboard UI with Dark Mode',
      news2_desc: 'Completely redesigned dashboard with improved performance, dark mode support, and mobile-friendly layout.',
      news3_date: 'January 2026',
      news3_title: 'Thai Language Support Added',
      news3_desc: 'TradeBot Pro now fully supports Thai language across all pages and notifications.',
      contact_title: 'Get in Touch',
      contact_subtitle: 'Have questions? We\'d love to hear from you.',
      contact_email: 'Email Us',
      contact_phone: 'Call Us',
      contact_location: 'Location',
      contact_name: 'Your Name',
      contact_email_field: 'Email Address',
      contact_message: 'Message',
      contact_send: 'Send Message',
      footer_desc: 'Rule-based, paper-first trading bots for cryptocurrency markets.',
      footer_product: 'Product',
      footer_company: 'Company',
      footer_rights: '© 2026 TradeBot Pro. All rights reserved.',
    },
    th: {
      nav_features: 'ฟีเจอร์',
      nav_news: 'ข่าวสาร',
      nav_contact: 'ติดต่อ',
      nav_login: 'เข้าสู่ระบบ',
      nav_dashboard: 'ไปยังแดชบอร์ด',
      hero_badge: 'ระบบเทรดอัตโนมัติ',
      hero_title: 'เทรดอย่างชาญฉลาด',
      hero_title2: 'ไม่ใช่แค่พยายาม',
      hero_desc:
        'TradeBot Pro รันบอท Grid, DCA และตามเทรนด์แบบตามกฎ โดยใช้ indicator ทางเทคนิค (เส้นค่าเฉลี่ย, RSI, ADX) เริ่มจากโหมดเทรดกระดาษแบบดูอย่างเดียว และมี kill switch หยุดการเทรดเมื่อถึงขีดจำกัดความเสี่ยง',
      hero_cta: 'เริ่มต้นฟรี',
      hero_cta2: 'ดูแดชบอร์ด',
      features_title: 'ทำไมต้อง TradeBot Pro?',
      features_subtitle: 'ทุกสิ่งที่คุณต้องการเพื่อเทรดระดับมืออาชีพ',
      feat1_title: 'กลยุทธ์แบบตามกฎ',
      feat1_desc: 'บอท Grid Trading, DCA และตามเทรนด์ ที่มีกฎเข้าและออกชัดเจน ทดสอบย้อนหลังและเทรดกระดาษได้ก่อน',
      feat2_title: 'วิเคราะห์ข้อมูลเรียลไทม์',
      feat2_desc: 'กราฟสด Indicators เทคนิค และติดตามพอร์ตโฟลิโออัปเดตทุกวินาที',
      feat3_title: 'ความปลอดภัยระดับธนาคาร',
      feat3_desc: 'API Key ของคุณถูกเข้ารหัสและไม่ถูกเก็บบนเซิร์ฟเวอร์ของเรา เทรดได้อย่างมั่นใจ',
      feat4_title: 'รวดเร็วสายฟ้า',
      feat4_desc: 'ส่งออร์เดอร์ใน Sub-millisecond เชื่อมต่อโดยตรงกับ Binance',
      feat5_title: 'รองรับหลายภาษา',
      feat5_desc: 'รองรับภาษาอังกฤษและไทยอย่างสมบูรณ์ พร้อมภาษาอื่นๆ เร็วๆ นี้',
      feat6_title: 'จัดการความเสี่ยง',
      feat6_desc: 'Stop-loss, Take-profit และการกำหนดขนาด Position ในตัวเพื่อปกป้องเงินทุนของคุณ',
      news_title: 'อัปเดตล่าสุด',
      news_subtitle: 'ติดตามความเคลื่อนไหวของ TradeBot Pro',
      news1_date: 'มีนาคม 2569',
      news1_title: 'เปิดตัว Grid Trading Strategy v2.0',
      news1_desc: 'ปรับปรุงอัลกอริทึม Grid Trading ครั้งใหญ่ พร้อมการตรวจจับช่วงราคาที่ฉลาดขึ้นและ Auto-rebalancing',
      news2_date: 'กุมภาพันธ์ 2569',
      news2_title: 'UI แดชบอร์ดใหม่พร้อม Dark Mode',
      news2_desc: 'ออกแบบแดชบอร์ดใหม่ทั้งหมด ประสิทธิภาพดีขึ้น รองรับ Dark Mode และ Mobile',
      news3_date: 'มกราคม 2569',
      news3_title: 'เพิ่มรองรับภาษาไทย',
      news3_desc: 'TradeBot Pro รองรับภาษาไทยอย่างสมบูรณ์ทุกหน้าและการแจ้งเตือน',
      contact_title: 'ติดต่อเรา',
      contact_subtitle: 'มีคำถามไหม? เรายินดีรับฟังเสมอ',
      contact_email: 'อีเมล',
      contact_phone: 'โทรศัพท์',
      contact_location: 'ที่อยู่',
      contact_name: 'ชื่อของคุณ',
      contact_email_field: 'อีเมล',
      contact_message: 'ข้อความ',
      contact_send: 'ส่งข้อความ',
      footer_desc: 'บอทเทรดแบบตามกฎ เริ่มจากเทรดกระดาษ สำหรับตลาด Cryptocurrency',
      footer_product: 'ผลิตภัณฑ์',
      footer_company: 'บริษัท',
      footer_rights: '© 2026 TradeBot Pro. สงวนลิขสิทธิ์ทั้งหมด',
    },
  };
  return translations[lang]?.[key] ?? translations['en']?.[key] ?? key;
};

// ─── Component ─────────────────────────────────────────────────────────────────
export default function LandingPageClient({ lang }: { lang: string }) {
  const [loginOpen, setLoginOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [isLoggedIn, setIsLoggedIn] = useState(false);
  const [contactSent, setContactSent] = useState(false);
  const [liveStats, setLiveStats] = useState<{
    paperPnl: number;
    paperTrades: number;
    polyPnl: number;
    polyTrades: number;
    arbScans: number;
    botsRunning: number;
  } | null>(null);

  useEffect(() => {
    if (typeof window !== 'undefined') {
      const token = localStorage.getItem('auth_token');
      setIsLoggedIn(!!token);
    }
  }, []);

  // Fetch live bot stats for portfolio proof
  useEffect(() => {
    const fetchLiveStats = async () => {
      try {
        const API_BASE = process.env.NEXT_PUBLIC_API_URL || '';
        const STRATEGY_API = process.env.NEXT_PUBLIC_STRATEGY_URL || '/strategy-api';
        const token = typeof window !== 'undefined' ? localStorage.getItem('auth_token') : null;
        const headers: Record<string, string> = { 'Content-Type': 'application/json' };
        if (token) headers['Authorization'] = `Bearer ${token}`;

        const [paperRes, polyRes, arbRes] = await Promise.allSettled([
          fetch(`${API_BASE}/api/paper/portfolio`, { headers }).then(r => r.json()),
          fetch(`${STRATEGY_API}/api/poly-paper/performance`, { headers }).then(r => r.json()),
          fetch(`${STRATEGY_API}/api/arb-paper/status`, { headers }).then(r => r.json()),
        ]);

        const paperData = paperRes.status === 'fulfilled' ? paperRes.value : null;
        const polyData = polyRes.status === 'fulfilled' ? polyRes.value : null;
        const arbData = arbRes.status === 'fulfilled' ? arbRes.value : null;

        setLiveStats({
          paperPnl: paperData?.total_pnl || paperData?.net_pnl || 0,
          paperTrades: paperData?.total_trades || paperData?.trade_count || 0,
          polyPnl: polyData?.total_pnl || 0,
          polyTrades: polyData?.total_trades || 0,
          arbScans: arbData?.scan_count || 0,
          botsRunning: (arbData?.running ? 1 : 0) + (paperData?.running ? 1 : 0),
        });
      } catch {
        // Silently fail — stats are optional
      }
    };
    fetchLiveStats();
    const interval = setInterval(fetchLiveStats, 60000);
    return () => clearInterval(interval);
  }, []);

  const handleContactSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setContactSent(true);
    setTimeout(() => setContactSent(false), 4000);
  };

  const navLinks = [
    { label: t(lang, 'nav_features'), href: '#features' },
    { label: t(lang, 'nav_news'), href: '#news' },
    { label: t(lang, 'nav_contact'), href: '#contact' },
  ];

  const features = [
    { icon: <Bot className="w-7 h-7" />, title: t(lang, 'feat1_title'), desc: t(lang, 'feat1_desc') },
    { icon: <BarChart3 className="w-7 h-7" />, title: t(lang, 'feat2_title'), desc: t(lang, 'feat2_desc') },
    { icon: <Shield className="w-7 h-7" />, title: t(lang, 'feat3_title'), desc: t(lang, 'feat3_desc') },
    { icon: <Zap className="w-7 h-7" />, title: t(lang, 'feat4_title'), desc: t(lang, 'feat4_desc') },
    { icon: <Globe className="w-7 h-7" />, title: t(lang, 'feat5_title'), desc: t(lang, 'feat5_desc') },
    { icon: <TrendingUp className="w-7 h-7" />, title: t(lang, 'feat6_title'), desc: t(lang, 'feat6_desc') },
  ];

  const news = [
    { date: t(lang, 'news1_date'), title: t(lang, 'news1_title'), desc: t(lang, 'news1_desc'), tag: 'Update' },
    { date: t(lang, 'news2_date'), title: t(lang, 'news2_title'), desc: t(lang, 'news2_desc'), tag: 'Design' },
    { date: t(lang, 'news3_date'), title: t(lang, 'news3_title'), desc: t(lang, 'news3_desc'), tag: 'i18n' },
  ];

  return (
    <div className="min-h-screen bg-slate-950 text-white">
      {/* ── Navbar ─────────────────────────────────────────────────────────── */}
      <header className="fixed top-0 left-0 right-0 z-50 bg-slate-950/80 backdrop-blur-md border-b border-slate-800">
        <div className="max-w-7xl mx-auto px-6 h-16 flex items-center justify-between">
          {/* Logo */}
          <div className="flex items-center gap-2">
            <TrendingUp className="w-7 h-7 text-purple-500" />
            <span className="text-xl font-bold">TradeBot Pro</span>
          </div>

          {/* Desktop nav */}
          <nav className="hidden md:flex items-center gap-8">
            {navLinks.map((link) => (
              <a
                key={link.href}
                href={link.href}
                className="text-gray-400 hover:text-white text-sm transition"
              >
                {link.label}
              </a>
            ))}
          </nav>

          {/* Right side */}
          <div className="hidden md:flex items-center gap-4">
            <LanguageSelector />
            {isLoggedIn ? (
              <Link
                href={`/${lang}/dashboard`}
                className="bg-purple-600 hover:bg-purple-700 text-white text-sm px-4 py-2 rounded-lg transition"
              >
                {t(lang, 'nav_dashboard')}
              </Link>
            ) : (
              <button
                onClick={() => setLoginOpen(true)}
                className="bg-purple-600 hover:bg-purple-700 text-white text-sm px-4 py-2 rounded-lg transition"
              >
                {t(lang, 'nav_login')}
              </button>
            )}
          </div>

          {/* Mobile menu button */}
          <button
            className="md:hidden text-gray-400 hover:text-white"
            onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
          >
            {mobileMenuOpen ? <X className="w-6 h-6" /> : <Menu className="w-6 h-6" />}
          </button>
        </div>

        {/* Mobile menu */}
        {mobileMenuOpen && (
          <div className="md:hidden bg-slate-900 border-t border-slate-800 px-6 py-4 space-y-3">
            {navLinks.map((link) => (
              <a
                key={link.href}
                href={link.href}
                onClick={() => setMobileMenuOpen(false)}
                className="block text-gray-300 hover:text-white py-2 transition"
              >
                {link.label}
              </a>
            ))}
            <div className="pt-2 flex flex-col gap-3">
              <LanguageSelector />
              {isLoggedIn ? (
                <Link
                  href={`/${lang}/dashboard`}
                  className="bg-purple-600 text-white text-sm px-4 py-2 rounded-lg text-center"
                >
                  {t(lang, 'nav_dashboard')}
                </Link>
              ) : (
                <button
                  onClick={() => { setLoginOpen(true); setMobileMenuOpen(false); }}
                  className="bg-purple-600 text-white text-sm px-4 py-2 rounded-lg"
                >
                  {t(lang, 'nav_login')}
                </button>
              )}
            </div>
          </div>
        )}
      </header>

      {/* ── Hero ───────────────────────────────────────────────────────────── */}
      <section className="relative pt-32 pb-24 px-6 overflow-hidden">
        {/* Glow */}
        <div className="absolute top-0 left-1/2 -translate-x-1/2 w-[800px] h-[400px] bg-purple-600/20 rounded-full blur-3xl pointer-events-none" />

        <div className="max-w-4xl mx-auto text-center relative">
          <span className="inline-block bg-purple-600/20 text-purple-400 text-sm font-medium px-4 py-1.5 rounded-full border border-purple-500/30 mb-6">
            {t(lang, 'hero_badge')}
          </span>
          <h1 className="text-5xl md:text-7xl font-extrabold leading-tight mb-4">
            {t(lang, 'hero_title')}
            <span className="block text-transparent bg-clip-text bg-gradient-to-r from-purple-400 to-pink-400">
              {t(lang, 'hero_title2')}
            </span>
          </h1>
          <p className="text-gray-400 text-lg md:text-xl max-w-2xl mx-auto mt-6 mb-10 leading-relaxed">
            {t(lang, 'hero_desc')}
          </p>
          <div className="flex flex-col sm:flex-row gap-4 justify-center">
            <button
              onClick={() => setLoginOpen(true)}
              className="bg-purple-600 hover:bg-purple-700 text-white px-8 py-4 rounded-xl font-semibold text-lg transition flex items-center justify-center gap-2"
            >
              {t(lang, 'hero_cta')}
              <ChevronRight className="w-5 h-5" />
            </button>
            <Link
              href={`/${lang}/dashboard`}
              className="bg-slate-800 hover:bg-slate-700 text-white px-8 py-4 rounded-xl font-semibold text-lg transition text-center"
            >
              {t(lang, 'hero_cta2')}
            </Link>
          </div>

          {/* Stats row */}
          <div className="grid grid-cols-3 gap-6 mt-20 max-w-xl mx-auto">
            {[
              { value: '24/7', label: lang === 'th' ? 'เทรดตลอดเวลา' : 'Always Trading' },
              { value: '< 1ms', label: lang === 'th' ? 'ความเร็วออร์เดอร์' : 'Order Speed' },
              { value: '99.9%', label: lang === 'th' ? 'Uptime' : 'Uptime' },
            ].map((stat) => (
              <div key={stat.label} className="text-center">
                <div className="text-3xl font-bold text-purple-400">{stat.value}</div>
                <div className="text-gray-500 text-sm mt-1">{stat.label}</div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* ── Live Performance (Portfolio Proof) ────────────────────────────── */}
      <section className="py-16 px-6 bg-slate-900/30 border-y border-slate-800">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-10">
            <div className="inline-flex items-center gap-2 bg-green-600/10 text-green-400 text-sm font-medium px-4 py-1.5 rounded-full border border-green-500/30 mb-4">
              <span className="w-2 h-2 bg-green-500 rounded-full animate-pulse" />
              {lang === 'th' ? 'ระบบทำงานอยู่' : 'Systems Live'}
            </div>
            <h2 className="text-3xl font-bold mb-2">
              {lang === 'th' ? 'ประสิทธิภาพเรียลไทม์' : 'Real-Time Performance'}
            </h2>
            <p className="text-gray-400">
              {lang === 'th' ? 'ข้อมูลสดจากบอทเทรดที่กำลังทำงาน' : 'Live data from actively running trading bots'}
            </p>
          </div>
          <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
            {[
              {
                label: lang === 'th' ? 'Grid P&L' : 'Grid P&L',
                value: liveStats ? `$${liveStats.paperPnl.toFixed(2)}` : '—',
                color: liveStats && liveStats.paperPnl >= 0 ? 'text-green-400' : 'text-red-400',
              },
              {
                label: lang === 'th' ? 'Grid Trades' : 'Grid Trades',
                value: liveStats ? String(liveStats.paperTrades) : '—',
                color: 'text-purple-400',
              },
              {
                label: lang === 'th' ? 'Poly P&L' : 'Poly P&L',
                value: liveStats ? `$${liveStats.polyPnl.toFixed(2)}` : '—',
                color: liveStats && liveStats.polyPnl >= 0 ? 'text-green-400' : 'text-red-400',
              },
              {
                label: lang === 'th' ? 'Poly Trades' : 'Poly Trades',
                value: liveStats ? String(liveStats.polyTrades) : '—',
                color: 'text-blue-400',
              },
              {
                label: lang === 'th' ? 'Arb Scans' : 'Arb Scans',
                value: liveStats ? String(liveStats.arbScans) : '—',
                color: 'text-amber-400',
              },
              {
                label: lang === 'th' ? 'Bots Active' : 'Bots Active',
                value: liveStats ? `${liveStats.botsRunning}/3` : '—',
                color: 'text-emerald-400',
              },
            ].map((stat) => (
              <div
                key={stat.label}
                className="bg-slate-800/60 border border-slate-700 rounded-xl p-4 text-center hover:border-purple-500/40 transition"
              >
                <div className={`text-2xl font-bold ${stat.color}`}>{stat.value}</div>
                <div className="text-gray-500 text-xs mt-1 uppercase tracking-wider">{stat.label}</div>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* ── Features ───────────────────────────────────────────────────────── */}
      <section id="features" className="py-24 px-6 bg-slate-900/50">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-16">
            <h2 className="text-4xl font-bold mb-4">{t(lang, 'features_title')}</h2>
            <p className="text-gray-400 text-lg">{t(lang, 'features_subtitle')}</p>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-8">
            {features.map((feature) => (
              <div
                key={feature.title}
                className="bg-slate-800/50 border border-slate-700 rounded-2xl p-6 hover:border-purple-500/50 transition group"
              >
                <div className="w-12 h-12 bg-purple-600/20 rounded-xl flex items-center justify-center text-purple-400 mb-4 group-hover:bg-purple-600/30 transition">
                  {feature.icon}
                </div>
                <h3 className="text-lg font-semibold mb-2">{feature.title}</h3>
                <p className="text-gray-400 text-sm leading-relaxed">{feature.desc}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* ── News ───────────────────────────────────────────────────────────── */}
      <section id="news" className="py-24 px-6">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-16">
            <h2 className="text-4xl font-bold mb-4">{t(lang, 'news_title')}</h2>
            <p className="text-gray-400 text-lg">{t(lang, 'news_subtitle')}</p>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-8">
            {news.map((item) => (
              <article
                key={item.title}
                className="bg-slate-800/50 border border-slate-700 rounded-2xl p-6 hover:border-purple-500/50 transition"
              >
                <div className="flex items-center gap-3 mb-4">
                  <span className="bg-purple-600/20 text-purple-400 text-xs font-semibold px-3 py-1 rounded-full border border-purple-500/30">
                    {item.tag}
                  </span>
                  <span className="text-gray-500 text-xs">{item.date}</span>
                </div>
                <h3 className="text-white font-semibold text-lg mb-3 leading-snug">{item.title}</h3>
                <p className="text-gray-400 text-sm leading-relaxed">{item.desc}</p>
              </article>
            ))}
          </div>
        </div>
      </section>

      {/* ── Contact ────────────────────────────────────────────────────────── */}
      <section id="contact" className="py-24 px-6 bg-slate-900/50">
        <div className="max-w-7xl mx-auto">
          <div className="text-center mb-16">
            <h2 className="text-4xl font-bold mb-4">{t(lang, 'contact_title')}</h2>
            <p className="text-gray-400 text-lg">{t(lang, 'contact_subtitle')}</p>
          </div>
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-12">
            {/* Info */}
            <div className="space-y-8">
              {[
                { icon: <Mail className="w-5 h-5" />, label: t(lang, 'contact_email'), value: 'support@tradebotpro.io' },
                { icon: <Phone className="w-5 h-5" />, label: t(lang, 'contact_phone'), value: '+66 2 XXX XXXX' },
                { icon: <MapPin className="w-5 h-5" />, label: t(lang, 'contact_location'), value: 'Bangkok, Thailand' },
              ].map((item) => (
                <div key={item.label} className="flex items-start gap-4">
                  <div className="w-10 h-10 bg-purple-600/20 rounded-xl flex items-center justify-center text-purple-400 flex-shrink-0">
                    {item.icon}
                  </div>
                  <div>
                    <div className="text-gray-500 text-sm">{item.label}</div>
                    <div className="text-white font-medium">{item.value}</div>
                  </div>
                </div>
              ))}
            </div>

            {/* Form */}
            <form onSubmit={handleContactSubmit} className="space-y-4">
              <input
                type="text"
                placeholder={t(lang, 'contact_name')}
                required
                className="w-full bg-slate-800 border border-slate-700 text-white placeholder-gray-500 px-4 py-3 rounded-xl focus:outline-none focus:border-purple-500 transition"
              />
              <input
                type="email"
                placeholder={t(lang, 'contact_email_field')}
                required
                className="w-full bg-slate-800 border border-slate-700 text-white placeholder-gray-500 px-4 py-3 rounded-xl focus:outline-none focus:border-purple-500 transition"
              />
              <textarea
                rows={5}
                placeholder={t(lang, 'contact_message')}
                required
                className="w-full bg-slate-800 border border-slate-700 text-white placeholder-gray-500 px-4 py-3 rounded-xl focus:outline-none focus:border-purple-500 transition resize-none"
              />
              <button
                type="submit"
                className="w-full bg-purple-600 hover:bg-purple-700 text-white py-3 rounded-xl font-semibold transition"
              >
                {contactSent ? (lang === 'th' ? 'ส่งแล้ว ✓' : 'Sent ✓') : t(lang, 'contact_send')}
              </button>
            </form>
          </div>
        </div>
      </section>

      {/* ── Footer ─────────────────────────────────────────────────────────── */}
      <footer className="border-t border-slate-800 py-12 px-6">
        <div className="max-w-7xl mx-auto">
          <div className="grid grid-cols-1 md:grid-cols-4 gap-8 mb-8">
            {/* Brand */}
            <div className="md:col-span-2">
              <div className="flex items-center gap-2 mb-4">
                <TrendingUp className="w-6 h-6 text-purple-500" />
                <span className="text-lg font-bold">TradeBot Pro</span>
              </div>
              <p className="text-gray-400 text-sm leading-relaxed max-w-xs">
                {t(lang, 'footer_desc')}
              </p>
              <div className="flex gap-4 mt-4">
                <a href="#" className="text-gray-500 hover:text-white transition">
                  <Twitter className="w-5 h-5" />
                </a>
                <a href="#" className="text-gray-500 hover:text-white transition">
                  <Github className="w-5 h-5" />
                </a>
              </div>
            </div>

            {/* Product links */}
            <div>
              <h4 className="text-white font-semibold mb-4">{t(lang, 'footer_product')}</h4>
              <ul className="space-y-2 text-gray-400 text-sm">
                <li><a href="#features" className="hover:text-white transition">{t(lang, 'nav_features')}</a></li>
                <li><a href="#news" className="hover:text-white transition">{t(lang, 'nav_news')}</a></li>
                <li>
                  <Link href={`/${lang}/dashboard`} className="hover:text-white transition">
                    Dashboard
                  </Link>
                </li>
              </ul>
            </div>

            {/* Company links */}
            <div>
              <h4 className="text-white font-semibold mb-4">{t(lang, 'footer_company')}</h4>
              <ul className="space-y-2 text-gray-400 text-sm">
                <li><a href="#contact" className="hover:text-white transition">{t(lang, 'nav_contact')}</a></li>
                <li>
                  <button onClick={() => setLoginOpen(true)} className="hover:text-white transition">
                    {t(lang, 'nav_login')}
                  </button>
                </li>
              </ul>
            </div>
          </div>

          <div className="border-t border-slate-800 pt-8 text-center text-gray-500 text-sm">
            {t(lang, 'footer_rights')}
          </div>
        </div>
      </footer>

      {/* ── Login Modal ────────────────────────────────────────────────────── */}
      <LoginModal
        isOpen={loginOpen}
        onClose={() => setLoginOpen(false)}
        onLoginSuccess={() => setIsLoggedIn(true)}
      />
    </div>
  );
}
