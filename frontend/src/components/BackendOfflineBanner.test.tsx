import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import BackendOfflineBanner from '@/components/BackendOfflineBanner';

afterEach(() => vi.unstubAllGlobals());

describe('BackendOfflineBanner', () => {
  it('tells the visitor in Thai that the backend is offline when /api/health answers 502', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ status: 502, ok: false }));
    render(<BackendOfflineBanner lang="th" />);
    expect(await screen.findByRole('status')).toHaveTextContent('เชื่อมต่อระบบหลังบ้านไม่ได้');
  });

  it('hides the banner once a later probe finds the backend back', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ status: 502, ok: false })
      .mockResolvedValue({ status: 200, ok: true, json: async () => ({ status: 'healthy' }) });
    vi.stubGlobal('fetch', fetchMock);
    render(<BackendOfflineBanner lang="th" />);
    expect(await screen.findByRole('status')).toBeInTheDocument();
    await vi.advanceTimersByTimeAsync(30000);
    await waitFor(() => expect(screen.getByRole('status')).toBeEmptyDOMElement());
    vi.useRealTimers();
  });

  it('renders nothing when the backend is healthy', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true, json: async () => ({ status: 'healthy' }) });
    vi.stubGlobal('fetch', fetchMock);
    render(<BackendOfflineBanner lang="en" />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    // The empty live region is present from the start so a later warning is announced.
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });
});
