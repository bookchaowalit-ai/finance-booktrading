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
});
