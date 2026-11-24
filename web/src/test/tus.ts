// tus-js-client in tests. Vitest resolves it to its Node build, which cannot
// read a jsdom File (forms.ts), so a test file whose page uploads through the
// transfer center replaces the module with FakeUpload:
//
//   vi.mock("tus-js-client", async () => ({ Upload: (await import("../test/tus")).FakeUpload }));
//
// and answers each upload itself: the uploads made are in `uploads`, in
// order, with the options the transfer center gave them (onProgress,
// onSuccess, metadata and the rest). The file empties `uploads` before each
// of its tests. What only one file's tests do with an upload stays in that
// file, as a function of the upload.

export class FakeUpload {
  // Where the NAS keeps the upload: known from the start when it is resumed.
  url: string | null;
  // How often the transfer center stopped it.
  aborted = 0;
  constructor(
    readonly file: File,
    // tus-js-client's options, as the transfer center wrote them.
    readonly options: Record<string, any>,
  ) {
    this.url = options.uploadUrl ?? null;
    uploads.push(this);
  }
  start() {}
  abort() {
    this.aborted += 1;
    return Promise.resolve();
  }
  get metadata() {
    return this.options.metadata as Record<string, string>;
  }
  // created is the NAS making the upload, which the transfer id names.
  created(id: string) {
    this.url = `http://nas/api/v1/uploads/${id}`;
    this.options.onUploadUrlAvailable();
  }
  // fail is the request of this method answered with status and, from the
  // product API, an error of this code; tusd's own answers carry none. Status
  // 0 is no answer at all.
  fail(method: string, status: number, code = "") {
    this.options.onError(
      Object.assign(new Error(`${method} ${status}`), {
        originalRequest: { getMethod: () => method },
        originalResponse: status
          ? { getStatus: () => status, getBody: () => (code ? JSON.stringify({ error: { code, message: code } }) : "") }
          : null,
      }),
    );
  }
}

export const uploads: FakeUpload[] = [];
