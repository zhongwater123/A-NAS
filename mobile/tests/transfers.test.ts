import { describe, expect, it, vi } from 'vitest';
import { NasClient } from '../src/nas/client';
import { downloadFile, type DownloadSink } from '../src/transfers/protocol';
function memorySink(initial = '') {
  let data = new TextEncoder().encode(initial); let closed = false;
  const sink: DownloadSink = { size: () => data.length, open: append => { if (!append) data = new Uint8Array(); }, write: async part => { data = new Uint8Array([...data, ...part]); }, close: () => { closed = true; } };
  return { sink, text: () => new TextDecoder().decode(data), closed: () => closed };
}
function fixture(response: () => Response) {
  const request = vi.fn(async (_url: string, _options: RequestInit) => response());
  return { nas: { request } as unknown as NasClient, request };
}
describe('bounded download and resume', () => {
  it('appends only a matching Range and ETag', async () => {
    const { nas, request } = fixture(() => new Response('def', { status: 206, headers: { 'Content-Range': 'bytes 3-5/6', ETag: 'v1' } }));
    const target = memorySink('abc');
    await downloadFile(nas, { url: '/file', total: 6, etag: 'v1', signal: new AbortController().signal, sink: target.sink, progress: () => {}, version: () => {} });
    expect(request.mock.calls[0][1].headers).toEqual({ Range: 'bytes=3-', 'If-Range': 'v1' }); expect(target.text()).toBe('abcdef'); expect(target.closed()).toBe(true);
  });
  it('restarts when the server returns a full new version', async () => {
    const { nas } = fixture(() => new Response('UVWXYZ', { headers: { ETag: 'v2' } })); const target = memorySink('abc');
    await downloadFile(nas, { url: '/file', total: 6, etag: 'v1', signal: new AbortController().signal, sink: target.sink, progress: () => {}, version: () => {} }); expect(target.text()).toBe('UVWXYZ');
  });
  it.each([{ status: 206, etag: 'v2', range: 'bytes 3-5/6' }, { status: 206, etag: 'v1', range: 'bytes 2-5/6' }])('rejects mismatched partial content', async test => {
    const { nas } = fixture(() => new Response('def', { status: test.status, headers: { ETag: test.etag, 'Content-Range': test.range } })); const target = memorySink('abc');
    await expect(downloadFile(nas, { url: '/file', total: 6, etag: 'v1', signal: new AbortController().signal, sink: target.sink, progress: () => {}, version: () => {} })).rejects.toThrow(); expect(target.text()).toBe('abc');
  });
  it('never marks truncated or oversized content complete', async () => {
    for (const body of ['abc', 'abcdefg']) { const { nas } = fixture(() => new Response(body)); const target = memorySink(); await expect(downloadFile(nas, { url: '/file', total: 6, signal: new AbortController().signal, sink: target.sink, progress: () => {}, version: () => {} })).rejects.toThrow(); expect(target.closed()).toBe(true); }
  });
  it('supports zero-byte downloads', async () => {
    const { nas } = fixture(() => new Response('')); const target = memorySink(); await downloadFile(nas, { url: '/file', total: 0, signal: new AbortController().signal, sink: target.sink, progress: () => {}, version: () => {} }); expect(target.text()).toBe('');
  });
});
