/**
 * Error codes reported by the native WPR library and the Node bindings.
 * @readonly
 * @enum {string}
 */
export const WprErrorCode = Object.freeze({
  /** Native library (libwpr) could not be located or loaded. */
  NATIVE_LIBRARY_LOAD_FAILED: 'NATIVE_LIBRARY_LOAD_FAILED',
  /** Request payload was malformed or contained invalid values. */
  INVALID_CONFIG: 'INVALID_CONFIG',
  /** The archive file could not be opened or created. */
  ARCHIVE_OPEN_FAILED: 'ARCHIVE_OPEN_FAILED',
  /** TLS certificates/keys could not be loaded or the TLS config failed. */
  TLS_CONFIG_FAILED: 'TLS_CONFIG_FAILED',
  /** An injected script could not be read. */
  SCRIPT_LOAD_FAILED: 'SCRIPT_LOAD_FAILED',
  /** One of the requested ports could not be bound (e.g. already in use). */
  PORT_BIND_FAILED: 'PORT_BIND_FAILED',
  /** The session stopped, but flushing the recorded archive failed. */
  ARCHIVE_FLUSH_FAILED: 'ARCHIVE_FLUSH_FAILED',
  /** The referenced session does not exist. */
  SESSION_NOT_FOUND: 'SESSION_NOT_FOUND',
  /** The session was already stopped (only raised by misuse; stop() is idempotent). */
  SESSION_ALREADY_STOPPED: 'SESSION_ALREADY_STOPPED',
  /** The operation is not valid in the current session state (e.g. archive before stop). */
  NOT_READY: 'NOT_READY',
  /** The requested mode is not supported. */
  UNSUPPORTED_MODE: 'UNSUPPORTED_MODE',
  /** Unexpected failure inside the native library. */
  INTERNAL_ERROR: 'INTERNAL_ERROR',
});

/**
 * First-class error type for all WebPageReplay failures.
 */
export class WprError extends Error {
  /**
   * @param {string} code One of the WprErrorCode values.
   * @param {string} message Human-readable description.
   * @param {{cause?: unknown, details?: Record<string, unknown>}} [options]
   */
  constructor(code, message, options = {}) {
    super(message, { cause: options.cause });
    this.name = 'WprError';
    this.code = code;
    if (options.details) {
      /** Extra structured details (e.g. the port and scheme that failed to bind). */
      this.details = options.details;
    }
  }
}