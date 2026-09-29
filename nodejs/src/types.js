/**
 * Type definitions (JSDoc) for the public API surface.
 *
 * @typedef {Object} WprPorts
 * @property {number} [http] Port for plain HTTP. 0 = auto-select, -1 = disabled. Default 0.
 * @property {number} [https] Port for HTTPS. 0 = auto-select, -1 = disabled. Default 0.
 * @property {number} [httpsToHttp] Port for the HTTP-to-HTTP2/HTTPS proxy. 0 = auto-select, -1 = disabled. Default -1.
 */

/**
 * @typedef {Object} WprBaseOptions
 * @property {WprPorts} [ports] Listener configuration. Defaults to `{ http: 0, https: 0, httpsToHttp: -1 }` (kernel auto-selection, reported back via `session.ports`).
 * @property {string} [host="127.0.0.1"] IP address/hostname to bind all servers to.
 * @property {string[]|string} [certFiles] PEM certificate files. Defaults to the PEMs bundled with WebPageReplay.
 * @property {string[]|string} [keyFiles] PEM private key files. Defaults to the PEMs bundled with WebPageReplay.
 * @property {string[]|string} [injectScripts] JavaScript sources to inject in all pages. Defaults to the bundled deterministic.js.
 * @property {string} [logLevel="ERROR"] Log verbosity ("DEBUG", "INFO", "WARN", "ERROR").
 * @property {string} [paramToIgnoreInURLPath=""] Ignore a query parameter for a given URL path ("{Full URL path}::{parameter}").
 * @property {boolean} [noArchiveCertificates=false] Do not store/read certificates in the archive.
 * @property {number} [constantMathRandomResult] Constant Math.random() result in [0, 1).
 * @property {boolean} [htmlInjection=true] Inject scripts into HTML responses.
 * @property {boolean} [jsInjection=false] Inject scripts into JavaScript responses.
 * @property {boolean} [quietMode=false] Quiet per-request logging.
 */

/**
 * @typedef {WprBaseOptions & {
 *   archive?: string,
 *   enableExperimentalTimedChunk?: never,
 * }} WprRecordOptions
 * Options for {@link startRecord}.
 * @property {string} [archive] Where to write the recorded archive. When omitted, the
 *   archive is kept in memory and returned as a Buffer by `session.stop()`.
 */

/**
 * @typedef {WprBaseOptions & {
 *   archive: string|Buffer,
 *   serveResponseInChronologicalSequence?: boolean,
 *   disableFuzzyURLMatching?: boolean,
 *   rulesFile?: string,
 *   injectArchiveScripts?: boolean,
 * }} WprReplayOptions
 * Options for {@link startReplay}.
 * @property {string|Buffer} archive Archive file path or raw archive contents.
 */

/**
 * @typedef {Object} WprStopResult
 * @property {string|null} path Fully-flushed archive file path (null when a throwaway temp file was used).
 * @property {Buffer|null} archive Recorded archive as a Buffer (record mode only).
 */

export {};