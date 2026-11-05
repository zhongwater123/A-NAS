import { createServer } from 'node:http';
import { WebSocketServer, WebSocket as NodeSocket } from 'ws';
import { expect, it, vi } from 'vitest';
import { NasClient } from '../src/nas/client';
import { ChatController } from '../src/chat/controller';

it('uses the existing VNAS wire protocol, restores without resending, and isolates approvals', async () => {
  let submits = 0; let cookie = ''; let messages: { role: string; content: string }[] = [];
  const server = createServer(async (req, res) => {
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/api/v1/session') {
      res.setHeader('Set-Cookie', 'anas_session=member-token; HttpOnly; Path=/');
      res.end(JSON.stringify({ csrfToken: 'csrf', expiresAt: '2026-12-01', user: { id: 'member' } }));
    } else if (req.url?.endsWith('/status')) res.end('{"available":true}');
    else if (req.url?.endsWith('/sessions')) res.end('{"sessions":[]}');
    else if (req.url?.endsWith('/messages')) res.end(JSON.stringify({ messages }));
    else if (req.url?.endsWith('/approvals')) res.end(JSON.stringify({ approvals: [
      { requestId: 'mine', sessionId: 'live-1', storedSessionId: 'stored-1', status: 'awaiting', kind: 'command', createdAt: 1 },
      { requestId: 'another-chat', sessionId: 'live-2', storedSessionId: 'stored-2', status: 'awaiting', kind: 'command', createdAt: 1 },
    ] }));
    else { res.writeHead(404); res.end('{}'); }
  });
  const wss = new WebSocketServer({ server });
  wss.on('connection', (socket, req) => {
    cookie = req.headers.cookie ?? '';
    const event = (type: string, payload: unknown) => socket.send(JSON.stringify({ jsonrpc: '2.0', method: 'event', params: { type, session_id: type === 'gateway.ready' ? undefined : 'live-1', payload } }));
    event('gateway.ready', {});
    socket.on('message', raw => {
      const request = JSON.parse(raw.toString()); let result = {};
      if (request.method === 'session.create') result = { session_id: 'live-1', stored_session_id: 'stored-1' };
      if (request.method === 'session.resume') result = { session_id: 'live-1', running: false };
      socket.send(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }));
      if (request.method === 'prompt.submit') {
        submits++; messages = [{ role: 'user', content: request.params.text }, { role: 'assistant', content: '来自 NAS 的回答' }];
        event('message.start', {}); event('message.delta', { text: '来自 NAS ' }); event('message.delta', { text: '的回答' }); event('message.complete', { status: 'success' });
      }
    });
  });
  await new Promise<void>(r => server.listen(0, '127.0.0.1', r));
  const origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
  const nas = new NasClient(fetch, { read: async () => null, write: async () => {}, clear: async () => {} }, ['127.0.0.1']);
  await nas.login(origin, 'member', 'test-password');
  const chat = new ChatController(nas, url => new NodeSocket(url, { headers: nas.headers }) as unknown as WebSocket);
  try {
    await chat.start(); await vi.waitFor(() => expect(chat.snapshot().ready).toBe(true));
    expect(cookie).toBe('anas_session=member-token');
    expect(await chat.send('找一份文件')).toBe(true);
    await vi.waitFor(() => expect(chat.snapshot().messages.at(-1)?.text).toBe('来自 NAS 的回答'));
    expect(chat.snapshot().busy).toBe(false);
    await chat.refreshApprovals(); expect(chat.snapshot().approvals.map(a => a.requestId)).toEqual(['mine']);
    chat.setActive(false); chat.setActive(true);
    await vi.waitFor(() => expect(chat.snapshot().ready).toBe(true));
    await vi.waitFor(() => expect(chat.snapshot().messages).toHaveLength(2));
    expect(submits).toBe(1);
    await chat.open(); expect(chat.snapshot().messages).toEqual([]); expect(chat.snapshot().approvals).toEqual([]);
  } finally {
    chat.dispose(); for (const socket of wss.clients) socket.terminate();
    await new Promise<void>(r => wss.close(() => r())); server.closeAllConnections(); await new Promise<void>(r => server.close(() => r()));
  }
});
