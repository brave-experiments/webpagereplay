/**
 * Low-level FFI bindings to the native WebPageReplay library (libwpr).
 *
 * The native library is a Go c-shared object exporting:
 *   char* WPR_Call(const char* method, const char* requestJson);
 *   void  WPR_Free(char* p);
 *
 * All returned strings are heap-allocated by the Go runtime and released via
 * WPR_Free through a koffi "disposable" type, so no C memory is ever leaked by
 * the bindings regardless of whether calls are issued synchronously or
 * asynchronously.
 */

import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { existsSync } from 'node:fs';
import { promisify } from 'node:util';
import koffi from 'koffi';
import { WprError, WprErrorCode } from './errors.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * Locates the prebuilt native library for the current platform.
 * Layout: prebuilds/<platform>-<arch>/<library-name>
 * @returns {string} Absolute path to the native library.
 */
export function findLibraryPath() {
  const base = `prebuilds/${process.platform}-${process.arch}`;
  const names = {
    linux: ['libwpr.so'],
    darwin: ['libwpr.dylib'],
    win32: ['wpr.dll'],
  };
  const candidates = (names[process.platform] ?? ['libwpr.so']).map(
    (name) => path.join(__dirname, '..', base, name),
  );
  for (const candidate of candidates) {
    if (existsSync(candidate)) {
      return candidate;
    }
  }
  throw new WprError(
    WprErrorCode.NATIVE_LIBRARY_LOAD_FAILED,
    `No prebuilt native WebPageReplay library found for ${process.platform}-${process.arch}. ` +
      `Looked in: ${candidates.join(', ')}. Build it with: npm run build`,
  );
}

/**
 * Binds WPR_Call/WPR_Free on an already-loaded or to-be-loaded library.
 * Exported primarily so tests can exercise the FFI mechanics against a mock
 * library.
 * @param {string} libPath Path of the shared library to load.
 * @returns {{call: (method: string, requestJson: object) => Promise<object>}}
 *   An async dispatch function with the transport call interface.
 */
export function loadFFI(libPath) {
  let lib;
  try {
    lib = koffi.load(libPath);
  } catch (err) {
    throw new WprError(
      WprErrorCode.NATIVE_LIBRARY_LOAD_FAILED,
      `Failed to load native WebPageReplay library from ${libPath}: ${err.message}`,
      { cause: err },
    );
  }

  const wprFree = lib.func('void WPR_Free(char* ptr)');
  // Strings returned by the library are malloc'd C strings owned by the Go
  // runtime; an anonymous disposable type guarantees WPR_Free is invoked as
  // soon as the value has been decoded, for both sync and async calls.
  // (Anonymous types avoid koffi's process-global type-name registry, so
  // repeated loads are always safe.)
  const WprStr = koffi.disposable(null, 'str', (ptr) => wprFree(ptr));
  const wprCall = lib.func('WPR_Call', WprStr, ['const char*', 'const char*']);
  const wprCallAsync = promisify(wprCall.async);

  return {
    async call(method, requestJson) {
      let raw;
      try {
        raw = await wprCallAsync(method, JSON.stringify(requestJson ?? {}));
      } catch (err) {
        // Failure marshalling or calling the FFI entry point itself.
        throw new WprError(
          WprErrorCode.INTERNAL_ERROR,
          `Native call ${method}() failed: ${err.message}`,
          { cause: err },
        );
      }
      return parseResponse(method, raw);
    },
  };
}

/**
 * Parses and validates a raw JSON response from any transport.
 * @param {string} method Method name (for error messages).
 * @param {string|null} raw Raw response text.
 * @returns {object} Parsed response.
 */
export function parseResponse(method, raw) {
  if (raw == null || raw === '') {
    throw new WprError(
      WprErrorCode.INTERNAL_ERROR,
      `Native call ${method}() returned an empty response`,
    );
  }
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new WprError(
      WprErrorCode.INTERNAL_ERROR,
      `Native call ${method}() returned a non-JSON response: ${String(raw).slice(0, 500)}`,
      { cause: err },
    );
  }
  if (parsed && parsed.ok === false) {
    const error = parsed.error ?? {};
    throw new WprError(
      error.code ?? WprErrorCode.INTERNAL_ERROR,
      error.message ?? 'Unknown native error',
      { details: error.details },
    );
  }
  return parsed;
}