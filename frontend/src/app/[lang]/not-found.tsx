'use client';

import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useTranslation } from '@/i18n/translations';

export default function NotFound() {
  const { t } = useTranslation();
  const router = useRouter();

  return (
    <main id="main-content" tabIndex={-1} className="min-h-screen bg-gradient-to-br from-gray-900 via-blue-900 to-gray-900 flex items-center justify-center px-4 outline-none">
      <div className="max-w-2xl w-full text-center">
        {/* Animated 404 Text */}
        <div className="mb-8">
          <h1 className="text-9xl font-bold text-transparent bg-clip-text bg-gradient-to-r from-blue-400 via-purple-500 to-pink-500 animate-pulse">
            404
          </h1>
        </div>

        {/* Error Message */}
        <div className="mb-8">
          <h2 className="text-3xl md:text-4xl font-bold text-white mb-4">
            {t('notfound.pageNotFound')}
          </h2>
          <p className="text-gray-300 text-lg md:text-xl">
            {t('notfound.description')}
          </p>
        </div>

        {/* Trading Icon */}
        <div className="mb-12">
          <div className="inline-flex items-center justify-center w-24 h-24 rounded-full bg-gray-800/50 backdrop-blur-sm border border-gray-700">
            <svg
              className="w-12 h-12 text-gray-400"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={1.5}
                d="M13 7h8m0 0v8m0-8l-8 8-4-4-6 6"
              />
            </svg>
          </div>
        </div>

        {/* Action Buttons */}
        <div className="flex flex-col sm:flex-row gap-4 justify-center items-center">
          <Link
            href="/en/dashboard"
            className="px-8 py-3 bg-gradient-to-r from-blue-600 to-purple-600 hover:from-blue-700 hover:to-purple-700 text-white font-semibold rounded-lg shadow-lg transform transition-all duration-200 hover:scale-105 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:ring-offset-gray-900"
          >
            {t('notfound.goToDashboard')}
          </Link>

          <button
            onClick={() => router.back()}
            className="px-8 py-3 bg-gray-800 hover:bg-gray-700 text-white font-semibold rounded-lg border border-gray-700 shadow-lg transform transition-all duration-200 hover:scale-105 focus:outline-none focus:ring-2 focus:ring-gray-500 focus:ring-offset-2 focus:ring-offset-gray-900"
          >
            {t('notfound.goBack')}
          </button>

          <Link
            href="/"
            className="px-8 py-3 text-gray-300 hover:text-white font-semibold transition-colors duration-200"
          >
            {t('notfound.goHome')}
          </Link>
        </div>

        {/* Help Text */}
        <div className="mt-12 text-gray-400 text-sm">
          <p>{t('notfound.helpText')}</p>
        </div>
      </div>
    </main>
  );
}
