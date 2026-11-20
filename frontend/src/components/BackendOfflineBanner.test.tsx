import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import BackendOfflineBanner from '@/components/BackendOfflineBanner';

afterEach(() => vi.unstubAllGlobals());

describe('BackendOfflineBanner', () => {
  it('tells the visitor in Thai that the backend is offline when /api/health answers 502', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ status: 502, ok: false }));
    render(<BackendOfflineBanner lang="th" />);
    expect(await screen.findByRole('status')).toHaveTextContent('ระบบหลังบ้านปิดอยู่');
  });

  it('renders nothing when the backend is healthy', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true });
    vi.stubGlobal('fetch', fetchMock);
    render(<BackendOfflineBanner lang="en" />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(screen.queryByRole('status')).toBeNull();
  });
});
