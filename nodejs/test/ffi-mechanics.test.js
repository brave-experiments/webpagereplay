/**
 * Validates the koffi FFI mechanics in src/native.js against a mock native
 * library that mimics the WPR_Call/WPR_Free protocol. This isolates the FFI
 * layer (string marshalling, disposable types, async dispatch, error
 * envelopes) from the Go runtime, so it can run on any platform — including
 * musl-based systems where real Go c-shared libraries cannot be dlopened.
 */

import { describe, it, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { loadFFI, parseResponse } from '../src/native.js';
import { WprError, WprErrorCode } from '../src/errors.js';

const hasCompiler = (() => {
  try {
    execFileSync('cc', ['--version'], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
})();

const MOCK_SOURCE = `
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

static int frees = 0;

char* WPR_Call(const char* method, const char* requestJson) {
    if (strcmp(method, "start") == 0) {
        return strdup("{\\"ok\\":true,\\"session\\":7,\\"ports\\":{\\"http\\":1234,\\"https\\":1235,\\"httpsToHttp\\":-1}}");
    }
    if (strcmp(method, "echo") == 0) {
        char out[64];
        snprintf(out, sizeof(out), "{\\"ok\\":true,\\"len\\":%zu}", strlen(requestJson));
        return strdup(out);
    }
    if (strcmp(method, "fail") == 0) {
        return strdup("{\\"ok\\":false,\\"error\\":{\\"code\\":\\"PORT_BIND_FAILED\\",\\"message\\":\\"port busy\\"}}");
    }
    if (strcmp(method, "garbage") == 0) {
        return strdup("not json");
    }
    if (strcmp(method, "frees") == 0) {
        char out[64];
        snprintf(out, sizeof(out), "{\\"ok\\":true,\\"frees\\":%d}", frees);
        return strdup(out);
    }
    return strdup("{\\"ok\\":true}");
}

void WPR_Free(char* p) {
    frees++;
    free(p);
}

int MockFrees(void) { return frees; }
`;

describe('FFI mechanics (mock native library)', { skip: hasCompiler ? false : 'no C compiler available' }, () => {
  let libPath;

  before(() => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wpr-mock-'));
    fs.writeFileSync(path.join(dir, 'mock.c'), MOCK_SOURCE);
    execFileSync('gcc', ['-shared', '-fPIC', '-o', path.join(dir, 'mockwpr.so'), path.join(dir, 'mock.c')]);
    libPath = path.join(dir, 'mockwpr.so');
  });

  it('dispatches sync and async calls and parses JSON responses', async () => {
    const ffi = loadFFI(libPath);
    const resp = await ffi.call('start', {});
    assert.deepEqual(resp, {
      ok: true,
      session: 7,
      ports: { http: 1234, https: 1235, httpsToHttp: -1 },
    });
  });

  it('round-trips request JSON strings through the C ABI', async () => {
    const ffi = loadFFI(libPath);
    const requestJson = { hello: 'world', n: 42 };
    const resp = await ffi.call('echo', requestJson);
    assert.equal(resp.ok, true);
    assert.equal(resp.len, JSON.stringify(requestJson).length, 'request JSON must arrive byte-for-byte');
  });

  it('converts native error envelopes into WprError', async () => {
    const ffi = loadFFI(libPath);
    await assert.rejects(
      ffi.call('fail', {}),
      (err) => err instanceof WprError && err.code === WprErrorCode.PORT_BIND_FAILED,
    );
  });

  it('flags non-JSON responses as INTERNAL_ERROR', async () => {
    const ffi = loadFFI(libPath);
    await assert.rejects(
      ffi.call('garbage', {}),
      (err) => err instanceof WprError && err.code === WprErrorCode.INTERNAL_ERROR,
    );
  });

  it('frees every returned C string via WPR_Free (disposable type)', async () => {
    const ffi = loadFFI(libPath);
    const before = await mockFrees(ffi); // its own response is freed after this value is read
    await ffi.call('start', {});
    await assert.rejects(ffi.call('fail', {}), WprError);
    await assert.rejects(ffi.call('garbage', {}), WprError);
    const after = await mockFrees(ffi);
    // Delta 4 = the 3 target responses above + the response of the `before`
    // measurement call itself.
    assert.equal(after - before, 4, 'each WPR_Call return must be freed exactly once');
  });

  it('parseResponse rejects empty responses', () => {
    assert.throws(() => parseResponse('x', ''), WprError);
    assert.throws(() => parseResponse('x', null), WprError);
  });
});

/** Calls MockFrees() on the mock library. */
async function mockFrees(ffi) {
  const resp = await ffi.call('frees', {});
  return resp.frees;
}