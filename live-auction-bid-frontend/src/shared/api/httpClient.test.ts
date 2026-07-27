// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiRequest } from './httpClient';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('apiRequest cancellation', () => {
  it('forwards the provided AbortSignal to fetch', async () => {
    const controller = new AbortController();
    const fetchMock = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.signal).toBe(controller.signal);
      return new Response(JSON.stringify({ result: { code: 0 } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    await apiRequest({ path: '/api/test', auth: 'none', signal: controller.signal });

    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('rejects an in-flight request when the signal is aborted', async () => {
    const controller = new AbortController();
    vi.stubGlobal('fetch', vi.fn((_url: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
    })));

    const request = apiRequest({ path: '/api/slow', auth: 'none', signal: controller.signal });
    controller.abort();

    await expect(request).rejects.toMatchObject({ name: 'AbortError' });
  });
});
