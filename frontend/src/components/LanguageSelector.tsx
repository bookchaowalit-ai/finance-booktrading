/**
 * Language Selector Component
 * Changes URL path for SEO-friendly localization
 */
'use client';

import { useState, useEffect } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { motion, AnimatePresence } from 'framer-motion';
import { Globe, Check, Sun, Moon, ChevronDown } from 'lucide-react';

const locales = [
  { code: 'en', name: 'English', nativeName: 'English' },
  { code: 'th', name: 'Thai', nativeName: 'ไทย' },
];

export default function LanguageSelector() {
  const pathname = usePathname();
  const router = useRouter();
  const [isOpen, setIsOpen] = useState(false);
  const [isDark, setIsDark] = useState(false);

  // Extract current locale from pathname
  const segments = pathname.split('/');
  const currentLocaleCode = segments[1] || 'en';
  const currentLocale = locales.find((l) => l.code === currentLocaleCode);

  // Load dark mode preference
  useEffect(() => {
    const isDarkMode = localStorage.getItem('theme') === 'dark' ||
      (!localStorage.getItem('theme') && window.matchMedia('(prefers-color-scheme: dark)').matches);
    setIsDark(isDarkMode);
  }, []);

  const toggleDarkMode = () => {
    const newIsDark = !isDark;
    setIsDark(newIsDark);
    localStorage.setItem('theme', newIsDark ? 'dark' : 'light');
    if (newIsDark) {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
  };

  const handleLocaleChange = (newLocale: string) => {
    const newSegments = [...segments];
    newSegments[1] = newLocale;
    const newPathname = newSegments.join('/');

    document.cookie = `NEXT_LOCALE=${newLocale}; path=/; max-age=${60 * 60 * 24 * 365}`;

    router.push(newPathname);
    setIsOpen(false);
  };

  return (
    <div className="flex items-center gap-2">
      {/* Dark Mode Toggle */}
      <button
        onClick={toggleDarkMode}
        className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
        aria-label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
        title={isDark ? 'Light mode' : 'Dark mode'}
      >
        {isDark ? (
          <Sun className="w-5 h-5 text-yellow-500" />
        ) : (
          <Moon className="w-5 h-5 text-gray-600" />
        )}
      </button>

      {/* Language Selector */}
      <div className="relative">
        <button
          onClick={() => setIsOpen(!isOpen)}
          className="flex items-center gap-2 px-3 py-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
          aria-label="Select language"
        >
          <Globe className="w-5 h-5 text-gray-600 dark:text-gray-400" />
          <span className="text-sm text-gray-300 hidden sm:inline">
            {currentLocale?.nativeName || 'English'}
          </span>
          <ChevronDown className="w-4 h-4 text-gray-500" />
        </button>

        <AnimatePresence>
          {isOpen && (
            <>
              <div className="fixed inset-0 z-40" onClick={() => setIsOpen(false)} />
              <motion.div
                initial={{ opacity: 0, y: -10 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -10 }}
                className="absolute right-0 mt-2 w-48 bg-white dark:bg-gray-800 rounded-lg shadow-lg border border-gray-200 dark:border-gray-700 z-50 overflow-hidden"
              >
                <div className="py-2">
                  {locales.map((loc) => (
                    <button
                      key={loc.code}
                      onClick={() => handleLocaleChange(loc.code)}
                      className={`w-full flex items-center justify-between px-4 py-2 text-sm transition-colors ${currentLocaleCode === loc.code
                        ? 'bg-purple-50 dark:bg-purple-900/20 text-purple-600 dark:text-purple-400'
                        : 'text-gray-700 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700'
                        }`}
                    >
                      <span>{loc.nativeName}</span>
                      {currentLocaleCode === loc.code && <Check className="w-4 h-4" />}
                    </button>
                  ))}
                </div>
              </motion.div>
            </>
          )}
        </AnimatePresence>
      </div>
    </div>
  );
}
