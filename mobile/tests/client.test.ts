import { createServer, type Server, type RequestListener } from 'node:http';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { NasClient, normalizeAddress, sameOriginURL, sessionCookie, unwrap } from '../src/nas/client';
const servers: Server[] = [];
afterEach(async () => { for (const server of servers.splice(0)) { server.closeAllConnections(); await new Promise<void>(r => server.close(() => r())); } });
async function listen(handler: RequestListener) {
  const server = createServer(handler); servers.push(server); await new Promise<void>(r => server.listen(0, '127.0.0.1', r));
  return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
}
export function vault() { let value: string | null = null; return { read: async () => value, write: async (v: string) => { value = v; }, clear: async () => { value = null; } }; }
const session = { csrfToken: 'csrf-test', expiresAt: '2026-11-06T00:00:00Z', user: { id: 'member-a', username: 'member', mustChangePassword: false } };
describe('NAS session and origin boundary', () => {
  it.each(['192.168.1.10', 'http://10.0.0.3:8080/', 'https://[fd00::1]'])('accepts local address %s', address => expect(normalizeAddress(address)).toMatch(/^https?:/));
  it.each(['http://example.com', 'http://8.8.8.8', 'http://192.168.1.2/api', 'http://user:pass@192.168.1.2', 'ftp://192.168.1.2', 'http://192.168.1.2?x=1'])('rejects %s before credentials leave', address => expect(() => normalizeAddress(address)).toThrow());
  it('refuses cross-origin transfer URLs', () => {
    expect(() => sameOriginURL('http://192.168.1.2', '//192.168.1.3/private')).toThrow();
    expect(sameOriginURL('http://192.168.1.2', '/api/v1/uploads/1')).toBe('http://192.168.1.2/api/v1/uploads/1');
  });
  it('parses cookies with an Expires comma', () => expect(sessionCookie('other=a; Expires=Wed, 11 Nov 2026 00:00:00 GMT, anas_session=token; HttpOnly; Path=/')).toBe('anas_session=token'));
  it('shares Cookie and CSRF across REST, persists no password, expires on 401', async () => {
    const requests: { method?: string; cookie?: string; csrf?: string; body: string }[] = [];
    let expired = false;
    const origin = await listen(async (req, res) => {
      let body = ''; for await (const part of req) body += part;
      requests.push({ method: req.method, cookie: req.headers.cookie, csrf: req.headers['x-csrf-token'] as string, body });
      res.setHeader('Content-Type', 'application/json');
      if (expired) { res.writeHead(401); res.end(JSON.stringify({ error: { code: 'unauthorized' } })); return; }
      if (req.method === 'POST') res.setHeader('Set-Cookie', 'anas_session=token; HttpOnly; Path=/');
      if (req.method === 'DELETE') { res.writeHead(204); res.end(); } else res.end(JSON.stringify(session));
    });
    const store = vault(), nas = new NasClient(fetch, store, ['127.0.0.1']);
    await nas.login(origin, 'member', 'test-only-password');
    expect(await store.read()).not.toContain('password');
    expect(requests[0].body).not.toContain('localConsole');
    const restored = new NasClient(fetch, store, ['127.0.0.1']); await restored.restore();
    expect(requests[1].cookie).toBe('anas_session=token');
    await restored.logout(); expect(requests[2].csrf).toBe('csrf-test'); expect(await store.read()).toBeNull();
    await nas.login(origin, 'member', 'test-only-password'); expired = true;
    const notify = vi.fn(); nas.onExpired = notify;
    await expect(unwrap(nas.api.GET('/api/v1/session'))).rejects.toThrow();
    expect(notify).toHaveBeenCalledOnce(); expect(nas.session).toBeNull(); expect(await store.read()).toBeNull();
  });
  it('does not follow login redirects or use unconfigured cleartext hosts', async () => {
    const origin = await listen((_req, res) => { res.writeHead(307, { Location: 'http://127.0.0.1:1/session' }); res.end(); });
    await expect(new NasClient(fetch, vault(), ['127.0.0.1']).login(origin, 'member', 'secret')).rejects.toThrow();
    const transport = vi.fn(); await expect(new NasClient(transport, vault(), []).login(origin, 'member', 'secret')).rejects.toThrow('host_not_configured'); expect(transport).not.toHaveBeenCalled();
  });
});
