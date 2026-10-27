// Requests built with a FormData keep it for the test server, which reads it
// back with formOf(request).
//
// Vitest's jsdom environment gives the page jsdom's FormData, Blob and File
// but Node's fetch and Request (undici), and the two do not mix: undici fails
// to send a jsdom FormData whose parts are jsdom Files, and cannot parse a
// multipart body in this environment either (Vitest 5.0, jsdom 30.1, msw 3.0).
// A Blob or a File sent as the whole body arrives intact, so only FormData
// needs this. It is the first setup file, so that MSW wraps this Request.
//
// Uploads with progress go through jsdom's XMLHttpRequest, which encodes the
// form itself; their tests replace XMLHttpRequest instead (api/client.test.ts,
// photos/PhotosPanel.test.tsx). tus-js-client resolves to its Node build here,
// which cannot read a jsdom File, so its tests replace the module.
const formHeader = "x-test-form";
const forms = new Map<string, FormData>();

const Base = globalThis.Request;
globalThis.Request = class extends Base {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    if (!(init?.body instanceof FormData)) {
      super(input, init);
      return;
    }
    // The form stays here and the request carries its key, which survives the
    // copies MSW makes of the request; the body itself is left empty.
    const key = String(forms.size + 1);
    forms.set(key, init.body);
    const headers = new Headers(init.headers ?? (input instanceof Request ? input.headers : undefined));
    headers.set(formHeader, key);
    headers.set("Content-Type", "multipart/form-data");
    super(input, { ...init, headers, body: "" });
  }
};

// formOf returns the FormData a request was sent with.
export function formOf(request: Request): FormData {
  const form = forms.get(request.headers.get(formHeader) ?? "");
  if (!form) throw new Error(`${request.method} ${request.url} was not sent with a FormData`);
  return form;
}
