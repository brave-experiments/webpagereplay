/**
 * webpagereplay — Node.js bindings for WebPageReplay (WprGo).
 *
 * High-level, fully async/await API for recording and replaying web traffic
 * with the WebPageReplay proxy, implemented as first-class FFI bindings on top
 * of the native Go library (libwpr) rather than a CLI wrapper.
 *
 * @example
 * import { startRecord } from 'webpagereplay';
 *
 * const session = await startRecord({ ports: { http: 0, https: 0 } });
 * console.log(session.ports.http);   // kernel-selected port, e.g. 41234
 * // ...drive a browser through the proxy...
 * const { archive } = await session.stop();   // Buffer of the .wprgo archive
 */

import os from 'node:os';
import path from 'node:path';
import fs from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { getTransport } from './transport.js';
import { WprError, WprErrorCode } from './errors.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..', '..');

/**
 * Default per-scheme port settings.
 * `0` asks the kernel to auto-select an ephemeral port (the selected port is
 * then reported back via `session.ports`), `-1` disables a listener.
 * @type {{http: number, https: number, httpsToHttp: number}}
 */
const DEFAULT_PORTS = Object.freeze({ http: 0, https: 0, httpsToHttp: -1 });

/** Default certificates shipped with the repository. */
const DEFAULT_CERT_FILES = ['wpr_cert.pem', 'ecdsa_cert.pem'].map((f) => path.join(repoRoot, f));
const DEFAULT_KEY_FILES = ['wpr_key.pem', 'ecdsa_key.pem'].map((f) => path.join(repoRoot, f));
const DEFAULT_INJECT_SCRIPTS = ['deterministic.js'].map((f) => path.join(repoRoot, f));

/**
 * Normalizes the `ports` option against the defaults.
 * @param {{http?: number, https?: number, httpsToHttp?: number}} [ports]
 * @returns {{http: number, https: number, httpsToHttp: number}}
 */
function normalizePorts(ports = {}) {
  const out = { ...DEFAULT_PORTS, ...ports };
  for (const key of ['http', 'https', 'httpsToHttp']) {
    const v = out[key];
    if (!Number.isInteger(v) || v < -1) {
      throw new WprError(
        WprErrorCode.INVALID_CONFIG,
        `ports.${key} must be -1 (disabled), 0 (auto-select) or a positive port number; got ${JSON.stringify(v)}`,
      );
    }
  }
  return out;
}

/**
 * Resolves an array of file paths, making each absolute. Paths that are
 * relative are resolved against the current working directory (standard
 * Node.js behavior).
 * @param {string|string[]|undefined} files
 * @param {string} what Description for error messages.
 * @returns {string[]}
 */
function toAbsoluteFiles(files, what) {
  if (files == null) {
    return [];
  }
  const list = Array.isArray(files) ? files : [files];
  return list.map((f) => {
    if (typeof f !== 'string' || f === '') {
      throw new WprError(WprErrorCode.INVALID_CONFIG, `${what} entries must be non-empty strings`);
    }
    return path.resolve(f);
  });
}

/**
 * Fails fast when the bundled default files are missing (e.g. when this
 * package is consumed outside a full WebPageReplay repository checkout), so
 * users get an actionable error instead of a native TLS/script load failure.
 * @param {string[]} files
 * @param {string} optionName Option the user should set explicitly.
 * @param {boolean} explicit Whether the user passed the option themselves.
 */
function checkDefaultFilesExist(files, optionName, explicit) {
  if (explicit) {
    return;
  }
  const missing = files.filter((f) => !existsSync(f));
  if (missing.length > 0) {
    throw new WprError(
      WprErrorCode.INVALID_CONFIG,
      `Default ${optionName} not found: ${missing.join(', ')}. Pass the '${optionName}' ` +
        'option explicitly, or run the bindings from a WebPageReplay repository checkout.',
    );
  }
}

/**
 * Common session option normalization shared by record and replay.
 * @param {import('./types.js').WprOptions} options
 * @param {string} mode
 * @returns {object} Request body for the native `start` method.
 */
function buildStartRequest(options, mode) {
  if (typeof options !== 'object' || options === null) {
    throw new WprError(WprErrorCode.INVALID_CONFIG, 'options must be an object');
  }
  const {
    archive,
    host = '127.0.0.1',
    ports,
    certFiles = DEFAULT_CERT_FILES,
    keyFiles = DEFAULT_KEY_FILES,
    injectScripts = DEFAULT_INJECT_SCRIPTS,
    logLevel = 'ERROR',
    paramToIgnoreInURLPath = '',
    noArchiveCertificates = false,
    constantMathRandomResult = null,
    htmlInjection = true,
    jsInjection = false,
    quietMode = false,
    serveResponseInChronologicalSequence = false,
    disableFuzzyURLMatching = false,
    rulesFile = '',
    injectArchiveScripts = false,
  } = options;

  const request = {
    mode,
    archivePath: archive ?? '',
    host,
    ports: normalizePorts(ports),
    certFiles: toAbsoluteFiles(certFiles, 'certFiles'),
    keyFiles: toAbsoluteFiles(keyFiles, 'keyFiles'),
    injectScripts: toAbsoluteFiles(injectScripts, 'injectScripts'),
    logLevel,
    paramToIgnoreInURLPath,
    noArchiveCertificates: Boolean(noArchiveCertificates),
    constantMathRandomResult,
    htmlInjection: Boolean(htmlInjection),
    jsInjection: Boolean(jsInjection),
    quietMode: Boolean(quietMode),
    serveResponseInChronologicalSequence: Boolean(serveResponseInChronologicalSequence),
    disableFuzzyURLMatching: Boolean(disableFuzzyURLMatching),
    rulesFile: rulesFile ? path.resolve(rulesFile) : '',
    injectArchiveScripts: Boolean(injectArchiveScripts),
  };

  // Fail fast with actionable errors when bundled defaults are missing
  // (e.g. outside a repository checkout).
  if (certFiles === DEFAULT_CERT_FILES) {
    checkDefaultFilesExist(DEFAULT_CERT_FILES, 'certFiles', false);
  }
  if (keyFiles === DEFAULT_KEY_FILES) {
    checkDefaultFilesExist(DEFAULT_KEY_FILES, 'keyFiles', false);
  }
  if (injectScripts === DEFAULT_INJECT_SCRIPTS) {
    checkDefaultFilesExist(DEFAULT_INJECT_SCRIPTS, 'injectScripts', false);
  }

  if (typeof host !== 'string' || host === '') {
    throw new WprError(WprErrorCode.INVALID_CONFIG, 'host must be a non-empty string');
  }
  if (mode === 'replay') {
    if (typeof rulesFile !== 'string') {
      throw new WprError(WprErrorCode.INVALID_CONFIG, 'rulesFile must be a string');
    }
  } else {
    // Record-only guard: the CLI flag exists but is rejected by the native
    // layer; catching it here gives a synchronous JS-level error.
    if (options.enableExperimentalTimedChunk) {
      throw new WprError(
        WprErrorCode.INVALID_CONFIG,
        'enableExperimentalTimedChunk is not supported by the library bindings',
      );
    }
    if (rulesFile !== '' || serveResponseInChronologicalSequence || injectArchiveScripts) {
      throw new WprError(
        WprErrorCode.INVALID_CONFIG,
        'rulesFile, injectArchiveScripts and serveResponseInChronologicalSequence are replay-only options',
      );
    }
  }
  if (typeof disableFuzzyURLMatching !== 'boolean') {
    throw new WprError(WprErrorCode.INVALID_CONFIG, 'disableFuzzyURLMatching must be a boolean');
  }
  if (typeof logLevel !== 'string') {
    throw new WprError(WprErrorCode.INVALID_CONFIG, 'logLevel must be a string');
  }
  if (typeof paramToIgnoreInURLPath !== 'string') {
    throw new WprError(WprErrorCode.INVALID_CONFIG, 'paramToIgnoreInURLPath must be a string');
  }
  if (constantMathRandomResult !== null) {
    if (
      typeof constantMathRandomResult !== 'number' ||
      !Number.isFinite(constantMathRandomResult) ||
      constantMathRandomResult < 0 ||
      constantMathRandomResult >= 1
    ) {
      throw new WprError(
        WprErrorCode.INVALID_CONFIG,
        'constantMathRandomResult must be a number in [0, 1) or null',
      );
    }
  }
  if (archive !== undefined && typeof archive !== 'string' && !Buffer.isBuffer(archive)) {
    throw new WprError(WprErrorCode.INVALID_CONFIG, 'archive must be a file path string or a Buffer');
  }
  return request;
}

/** Monotonically increasing counter used for temporary archive files. */
let tempFileCounter = 0;

/**
 * Resolves the archive handling for a session:
 * - explicit string path: used as-is (for record this is where the archive is
 *   written; for replay this is the archive that is served).
 * - Buffer (replay only): written to a temporary file (deleted on stop).
 * - undefined: a temporary file is created by the native side (record) or the
 *   argument is mandatory (replay).
 * @param {string|Buffer|undefined} archive
 * @param {string} mode
 * @returns {Promise<{archivePath: string|null, tempArchivePath: string|null}>}
 */
async function resolveArchivePath(archive, mode) {
  if (archive === undefined) {
    if (mode === 'replay') {
      throw new WprError(WprErrorCode.INVALID_CONFIG, 'startReplay() requires an archive file path or Buffer');
    }
    return { archivePath: null, tempArchivePath: null }; // native side allocates a temp file
  }
  if (typeof archive === 'string') {
    if (mode === 'replay' && !existsSync(archive)) {
      throw new WprError(
        WprErrorCode.ARCHIVE_OPEN_FAILED,
        `Replay archive does not exist: ${archive}`,
      );
    }
    return { archivePath: path.resolve(archive), tempArchivePath: null };
  }
  if (mode === 'record') {
    throw new WprError(
      WprErrorCode.INVALID_CONFIG,
      'Record mode archives are outputs: pass a file path string (or nothing, '
        + 'to keep the archive in memory and receive it as a Buffer from stop()).',
    );
  }
  // Buffer (replay only): stage it in a temporary file.
  const tempPath = path.join(
    os.tmpdir(),
    `wpr-archive-${process.pid}-${Date.now()}-${tempFileCounter++}.wprgo`,
  );
  await fs.writeFile(tempPath, archive);
  return { archivePath: tempPath, tempArchivePath: tempPath };
}

/**
 * A running WebPageReplay session (record or replay).
 */
export class WprSession {
  /**
   * @param {object} params
   * @param {number} params.sessionId Numeric session id assigned by the native library.
   * @param {'record'|'replay'} params.mode
   * @param {{http: number, https: number, httpsToHttp: number}} params.ports Actually bound ports (-1 = disabled).
   * @param {string|null} params.archivePath Archive file path backing the session.
   * @param {string|null} params.tempArchivePath Temporary file that must be removed on stop.
   * @private
   */
  constructor({ sessionId, mode, ports, archivePath, tempArchivePath }) {
    /** @type {number} Numeric session handle used by the native library. */
    this.id = sessionId;
    /** @type {'record'|'replay'} Session mode. */
    this.mode = mode;
    /**
     * Ports actually bound by this session. When a port of `0` was requested,
     * this contains the kernel-selected ephemeral port. `-1` means the
     * listener is disabled.
     * @type {{http: number, https: number, httpsToHttp: number}}
     */
    this.ports = Object.freeze({ ...ports });
    /** @type {string|null} Path of the archive backing this session. */
    this.archivePath = archivePath;
    /** @type {boolean} */
    this.stopped = false;
    this._tempArchivePath = tempArchivePath;
    this._transport = getTransport();
    this._stopPromise = null;
    this._stopResult = null;
    this._mutex = Promise.resolve();
  }

  /**
   * Serializes native calls per session so concurrent stop()/stop() or
   * stop()/getArchive() cannot interleave.
   * @template T
   * @param {() => Promise<T>} fn
   * @returns {Promise<T>}
   */
  _serialized(fn) {
    const run = this._mutex.then(fn);
    // Keep the chain alive even if a call rejects.
    this._mutex = run.catch(() => {});
    return run;
  }

  /**
   * Gracefully stops the session: shuts down all proxy servers, flushes the
   * recorded archive to disk and (for record mode) hands back the archive.
   *
   * Idempotent: calling stop() again resolves with the same result.
   * Safe to call even if the session stopped itself via the
   * `/web-page-replay-command-exit` control URL.
   *
   * @param {{timeoutMs?: number}} [options]
   * @returns {Promise<{path: string|null, archive: Buffer|null}>}
   *   `path` is the fully-flushed archive file path (null if a throwaway temp
   *   file was used), `archive` is the recorded archive as a Buffer (record
   *   mode only, null in replay mode).
   * @throws {WprError} On native failures.
   */
  stop(options = {}) {
    if (this._stopPromise) {
      return this._stopPromise;
    }
    const attempt = this._serialized(async () => {
      const timeoutMs = options.timeoutMs ?? 5000;
      if (!Number.isInteger(timeoutMs) || timeoutMs <= 0) {
        throw new WprError(WprErrorCode.INVALID_CONFIG, `timeoutMs must be a positive integer; got ${timeoutMs}`);
      }
      const stopResp = await this._transport.call('stop', {
        session: this.id,
        timeoutMs,
      });
      const result = { path: stopResp.archivePath || null, archive: null };

      if (this.mode === 'record') {
        const length = stopResp.archiveLength ?? 0;
        if (length > 0) {
          const archiveResp = await this._transport.call('getArchive', { session: this.id });
          result.archive = Buffer.from(archiveResp.archiveBase64, 'base64');
          if (result.archive.length !== length) {
            throw new WprError(
              WprErrorCode.INTERNAL_ERROR,
              `Archive size mismatch: native reported ${length} bytes but transferred ${result.archive.length}`,
            );
          }
        }
      }

      // Remove the temp file we staged (replay-from-buffer); a staged temp
      // path must never leak out as the session's archive path.
      if (this._tempArchivePath) {
        result.path = null;
        try {
          await fs.unlink(this._tempArchivePath);
        } catch (err) {
          if (err.code !== 'ENOENT') {
            throw new WprError(
              WprErrorCode.INTERNAL_ERROR,
              `Failed to remove temporary archive ${this._tempArchivePath}: ${err.message}`,
              { cause: err },
            );
          }
        }
      }

      // Release the native session's retained state (registry + archive bytes).
      if (!this._destroyed) {
        this._destroyed = true;
        await this._transport.call('destroy', { session: this.id }).catch(() => {});
      }

      this.stopped = true;
      this._stopResult = result;
      return result;
    });
    // Memoize only success: a failed stop() remains retryable.
    this._stopPromise = attempt.then(
      (result) => result,
      (err) => {
        this._stopPromise = null;
        throw err;
      },
    );
    return this._stopPromise;
  }

  /**
   * Alias for {@link WprSession#stop}.
   */
  async close(options) {
    return this.stop(options);
  }

  /**
   * Human-readable summary, useful for logging.
   * @returns {string}
   */
  toString() {
    const ports = Object.entries(this.ports)
      .filter(([, p]) => p !== -1)
      .map(([scheme, p]) => `${scheme}=${p}`)
      .join(', ');
    return `WprSession(#${this.id}, mode=${this.mode}, ${ports || 'no listeners'})`;
  }
}

/**
 * Starts a WebPageReplay session in record mode.
 *
 * By default both the HTTP and HTTPS listeners auto-select an ephemeral port
 * (equivalent to the CLI's port value `0`); the kernel-selected ports are
 * reported back via the returned session's `ports` property.
 *
 * @param {import('./types.js').WprRecordOptions} [options]
 * @returns {Promise<WprSession>}
 * @throws {WprError} e.g. code PORT_BIND_FAILED if a fixed port is unavailable,
 *   ARCHIVE_OPEN_FAILED if the archive cannot be created, TLS_CONFIG_FAILED if
 *   certificates cannot be loaded.
 */
export async function startRecord(options = {}) {
  const { archive, ...rest } = options ?? {};
  const { archivePath, tempArchivePath } = await resolveArchivePath(archive, 'record');
  const request = buildStartRequest(
    { ...rest, archive: archivePath ?? undefined },
    'record',
  );
  const transport = getTransport();
  const resp = await transport.call('start', request);
  return new WprSession({
    sessionId: resp.session,
    mode: 'record',
    ports: resp.ports,
    archivePath: archivePath ?? resp.archivePath ?? null,
    tempArchivePath,
  });
}

/**
 * Starts a WebPageReplay session in replay mode, serving previously recorded
 * traffic from an archive.
 *
 * @param {import('./types.js').WprReplayOptions} options
 * @returns {Promise<WprSession>}
 * @throws {WprError} e.g. code PORT_BIND_FAILED, ARCHIVE_OPEN_FAILED,
 *   TLS_CONFIG_FAILED.
 */
export async function startReplay(options = {}) {
  const { archive, ...rest } = options ?? {};
  const { archivePath, tempArchivePath } = await resolveArchivePath(archive, 'replay');
  const request = buildStartRequest(
    {
      ...rest,
      archive: archivePath,
    },
    'replay',
  );
  const transport = getTransport();
  let resp;
  try {
    resp = await transport.call('start', request);
  } catch (err) {
    // Do not orphan a staged temp file when the start fails.
    if (tempArchivePath) {
      await fs.unlink(tempArchivePath).catch(() => {});
    }
    throw err;
  }
  return new WprSession({
    sessionId: resp.session,
    mode: 'replay',
    ports: resp.ports,
    archivePath,
    tempArchivePath,
  });
}

export { WprError, WprErrorCode } from './errors.js';
export { DEFAULT_PORTS };