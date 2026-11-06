import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { expect, it } from "vitest";
import { Upload } from "tus-js-client";
import { NasClient } from "../src/nas/client";
import { NasTusStack } from "../src/transfers/protocol";

it("uploads and resumes through the NAS tus adapter without duplicating committed bytes", async () => {
  const original = Buffer.alloc(1024 * 1024 + 17, 91);
  let stored = Buffer.alloc(0);
  let creates = 0;
  let resumedOffset = -1;
  const server = createServer(async (req, res) => {
    res.setHeader("Tus-Resumable", "1.0.0");
    if (req.url === "/api/v1/session") {
      res.setHeader("Set-Cookie", "anas_session=token; HttpOnly");
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          csrfToken: "csrf",
          user: { id: "member" },
          expiresAt: "2026-12-01",
        }),
      );
      return;
    }
    expect(req.headers.cookie).toBe("anas_session=token");
    if (req.method !== "HEAD") expect(req.headers["x-csrf-token"]).toBe("csrf");
    if (req.method === "POST") {
      creates++;
      expect(req.headers["upload-metadata"]).toContain("filename");
      res.writeHead(201, { Location: "/api/v1/uploads/one" });
      res.end();
    } else if (req.method === "HEAD") {
      resumedOffset = stored.length;
      res.writeHead(200, {
        "Upload-Offset": stored.length,
        "Upload-Length": original.length,
      });
      res.end();
    } else if (req.method === "PATCH") {
      if (Number(req.headers["upload-offset"]) !== stored.length) {
        res.writeHead(409);
        res.end();
        return;
      }
      for await (const chunk of req) stored = Buffer.concat([stored, chunk]);
      res.writeHead(204, { "Upload-Offset": stored.length });
      res.end();
    } else {
      res.writeHead(404);
      res.end();
    }
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
  const nas = new NasClient(
    fetch,
    { read: async () => null, write: async () => {}, clear: async () => {} },
    ["127.0.0.1"],
  );
  let url = "";
  function transfer(pause: boolean) {
    return new Promise<void>((resolve, reject) => {
      const upload = new Upload(original, {
        endpoint: nas.url("/api/v1/uploads"),
        uploadUrl: url || undefined,
        chunkSize: 128 * 1024,
        storeFingerprintForResuming: false,
        retryDelays: null,
        httpStack: new NasTusStack(nas),
        fileReader: {
          openFile: async () => ({
            size: original.length,
            slice: async (start, end) => {
              const value = original.subarray(start, end);
              return {
                value: Object.assign(value, { size: value.length }),
                done: end >= original.length,
              };
            },
            close() {},
          }),
        },
        metadata: {
          filename: "中文资料.bin",
          spaceId: "private",
          conflict: "rename",
        },
        onUploadUrlAvailable: () => {
          url = upload.url!;
        },
        onError: reject,
        onSuccess: () => resolve(),
        onChunkComplete: () => {
          if (pause) void upload.abort().then(resolve, reject);
        },
      });
      upload.start();
    });
  }
  try {
    await nas.login(origin, "member", "test-password");
    await transfer(true);
    const offset = stored.length;
    expect(offset).toBeGreaterThan(0);
    expect(offset).toBeLessThan(original.length);
    await transfer(false);
    expect(creates).toBe(1);
    expect(resumedOffset).toBe(offset);
    expect(createHash("sha256").update(stored).digest("hex")).toBe(
      createHash("sha256").update(original).digest("hex"),
    );
  } finally {
    server.closeAllConnections();
    await new Promise<void>((r) => server.close(() => r()));
  }
});
