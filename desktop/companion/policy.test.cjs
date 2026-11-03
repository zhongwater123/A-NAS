const { test } = require("node:test");
const assert = require("node:assert/strict");
const { nasOrigin, safeDelta, clampWindow, validConversation } = require("./policy.cjs");

test("NAS origin rejects credential URLs, scripts and public cleartext", () => {
  for (const url of ["javascript:alert(1)", "file:///tmp/index.html", "http://example.com", "http://user:pass@192.168.1.2"]) {
    assert.throws(() => nasOrigin(url));
  }
  assert.equal(nasOrigin("http://192.168.1.20:8080/path"), "http://192.168.1.20:8080");
  assert.equal(nasOrigin("https://nas.example.com/path"), "https://nas.example.com");
});
test("native movement stays bounded on displays with a negative origin", () => {
  assert.equal(safeDelta(Infinity), 0);
  assert.equal(safeDelta("4"), 0);
  assert.equal(safeDelta(900), 120);
  assert.deepEqual(clampWindow({ x: -9000, y: 8000 }, { x: -1920, y: 0, width: 1920, height: 1080 }, { width: 350, height: 280 }), { x: -1920, y: 800 });
});

test("conversation IPC accepts stored conversations and scheduled task runs only", () => {
 assert.equal(validConversation("20261102_090000_abcdef"),true);
 assert.equal(validConversation("cron_aaaaaaaaaaaa_20261103_100000"),true);
 for (const value of [null,42,"../foo","https://evil.example","cron_abc","cron_aaaaaaaaaaaa_20261103_100000?open=files","cron_aaaaaaaaaaaa_20261103_100000/../foo","20261102_090000_ABCDEF","20261102_090000_abcdef?open=files"]) assert.equal(validConversation(value),false);
});
