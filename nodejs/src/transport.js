/**
 * Transport for the WebPageReplay bindings.
 *
 * The transport implements the dispatch interface
 * `call(method, requestJson) -> Promise<responseJson>` via an in-process
 * FFI into the Go c-shared library (libwpr) via koffi. Calls run on the
 * libuv threadpool.
 */

import { findLibraryPath, loadFFI } from './native.js';

/** @type {{call: (method: string, request: object) => Promise<object>}|null} */
let _transport = null;

/**
 * Returns the process-wide transport (memoized).
 * @returns {{call: (method: string, requestJson: object) => Promise<object>}}
 */
export function getTransport() {
  if (_nativeTransportReady()) {
    return _transport;
  }
  _transport = loadFFI(findLibraryPath());
  return _transport;
}

function _nativeTransportReady() {
  return _transport !== null;
}

/** @internal Test-only: reset the memoized transport. */
export function _resetTransportForTesting() {
  _transport = null;
}