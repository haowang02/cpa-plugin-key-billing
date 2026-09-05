// Run with: node --test scripts/frontend_crypto_test.mjs
import assert from "node:assert/strict";
import { createHash, webcrypto } from "node:crypto";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const html = readFileSync(new URL("../internal/plugin/ui.html", import.meta.url), "utf8");
const source = html.slice(
  html.indexOf("      async function sha256Hex("),
  html.indexOf("      async function configuredCredentialEntries("),
);
const expected = (value) => createHash("sha256").update(value).digest("hex");
const vectors = [
  "", "abc", "中文凭据🔑", "a\0b\0", "\ud800",
  ...[55, 56, 63, 64, 65, 119, 120, 127, 128, 129, 10000].map(n => "a".repeat(n)),
  "cli-proxy-api:caller-scope:v1\0sk-demo-0001",
  "admin|dummy", "account|sk-demo-0001",
];

for (const [name, crypto] of [["Web Crypto", webcrypto], ["HTTP fallback", {}]]) {
  const context = vm.createContext({ TextEncoder, crypto, window: { crypto } });
  vm.runInContext(source, context);
  test(`${name}: UTF-8, padding boundaries and caller scopes match SHA-256`, async () => {
    for (const value of vectors) {
      assert.equal(await context.sha256Hex(value), expected(value));
    }
  });
  test(`${name}: exact credential bindings preserve salts and collision suffixes`, async () => {
    const counters = new Map();
    const parts = [" sk-dummy-codex ", "https://example.invalid/v1", ""];
    const id = "codex:" + expected("codex\0sk-dummy-codex\0https://example.invalid/v1\0").slice(0, 12);
    for (const suffix of ["", "-1"]) {
      assert.equal(
        await context.configuredCredentialRef("codex", parts, counters),
        "sha256:" + expected("cpa-key-billing:credential:v1\0" + id + suffix),
      );
    }
  });
}
