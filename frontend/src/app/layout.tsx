/**
 * Root Layout - Only imports globals, no HTML structure
 */
import './globals.css';

export const metadata = {
  title: 'BookFinance',
  description: 'Rule-based, paper-first crypto trading bots',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return children;
}
