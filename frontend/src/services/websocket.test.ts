import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WebSocketService } from './websocket';

class FakeSocket {
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeSocket.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {}
  open() {
    this.readyState = FakeSocket.OPEN;
    this.onopen?.();
  }
  drop() {
    this.readyState = 3; // CLOSED
    this.onclose?.();
  }
}

describe('WebSocketService auth', () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeSocket);
    window.localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('sends the session token as the first message, never in the URL', () => {
    window.localStorage.setItem('auth_token', 'sess-123');
    const svc = new WebSocketService('ws://example.test/ws');
    svc.connect();

    const sock = FakeSocket.instances[0];
    expect(sock.url).toBe('ws://example.test/ws');
    expect(sock.url).not.toContain('sess-123');
    sock.open();
    expect(JSON.parse(sock.sent[0])).toEqual({ type: 'auth', token: 'sess-123' });
    svc.disconnect();
  });

  it('does not open a socket when signed out', () => {
    const svc = new WebSocketService('ws://example.test/ws');
    svc.connect();
    expect(FakeSocket.instances).toHaveLength(0);
  });

  it('does not hand the auth ack to message handlers', () => {
    window.localStorage.setItem('auth_token', 'sess-123');
    const svc = new WebSocketService('ws://example.test/ws');
    const handler = vi.fn();
    svc.onMessage(handler);
    svc.connect();
    const sock = FakeSocket.instances[0];
    sock.open();
    sock.onmessage?.({ data: JSON.stringify({ type: 'auth_ok' }) });
    expect(handler).not.toHaveBeenCalled();
    svc.disconnect();
  });

  it('keeps backing off while the server rejects auth', () => {
    vi.useFakeTimers();
    try {
      window.localStorage.setItem('auth_token', 'expired');
      const svc = new WebSocketService('ws://example.test/ws');
      svc.connect();
      const first = FakeSocket.instances[0];
      first.open();
      first.drop(); // server closed: auth rejected, no auth_ok
      vi.advanceTimersByTime(1200);
      const second = FakeSocket.instances[1];
      second.open();
      second.drop();
      // Attempt counter kept growing: the next retry is ~4 s out, not ~1 s.
      vi.advanceTimersByTime(1200);
      expect(FakeSocket.instances).toHaveLength(2);
      svc.disconnect();
    } finally {
      vi.useRealTimers();
    }
  });

  it('resets backoff once auth_ok arrives', () => {
    vi.useFakeTimers();
    try {
      window.localStorage.setItem('auth_token', 'sess-123');
      const svc = new WebSocketService('ws://example.test/ws');
      svc.connect();
      const first = FakeSocket.instances[0];
      first.open();
      first.drop();
      vi.advanceTimersByTime(1200);
      const second = FakeSocket.instances[1];
      second.open();
      second.onmessage?.({ data: JSON.stringify({ type: 'auth_ok' }) });
      second.drop();
      vi.advanceTimersByTime(1200);
      expect(FakeSocket.instances).toHaveLength(3);
      svc.disconnect();
    } finally {
      vi.useRealTimers();
    }
  });
});
