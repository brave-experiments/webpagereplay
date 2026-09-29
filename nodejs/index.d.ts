/**
 * Type definitions for the webpagereplay Node.js library — first-class FFI
 * bindings for WebPageReplay (WprGo).
 */

export declare const WprErrorCode: Readonly<{
  NATIVE_LIBRARY_LOAD_FAILED: 'NATIVE_LIBRARY_LOAD_FAILED';
  INVALID_CONFIG: 'INVALID_CONFIG';
  ARCHIVE_OPEN_FAILED: 'ARCHIVE_OPEN_FAILED';
  TLS_CONFIG_FAILED: 'TLS_CONFIG_FAILED';
  SCRIPT_LOAD_FAILED: 'SCRIPT_LOAD_FAILED';
  PORT_BIND_FAILED: 'PORT_BIND_FAILED';
  ARCHIVE_FLUSH_FAILED: 'ARCHIVE_FLUSH_FAILED';
  SESSION_NOT_FOUND: 'SESSION_NOT_FOUND';
  SESSION_ALREADY_STOPPED: 'SESSION_ALREADY_STOPPED';
  NOT_READY: 'NOT_READY';
  UNSUPPORTED_MODE: 'UNSUPPORTED_MODE';
  INTERNAL_ERROR: 'INTERNAL_ERROR';
}>;

export declare class WprError extends Error {
  readonly code: string;
  readonly details?: Record<string, unknown>;
  constructor(code: string, message: string, options?: {
    cause?: unknown;
    details?: Record<string, unknown>;
  });
}

export interface WprPorts {
  /** Port for plain HTTP. 0 = auto-select, -1 = disabled. Default 0. */
  http?: number;
  /** Port for HTTPS. 0 = auto-select, -1 = disabled. Default 0. */
  https?: number;
  /** Port for the HTTPS-to-HTTP proxy. 0 = auto-select, -1 = disabled. Default -1. */
  httpsToHttp?: number;
}

export interface WprBaseOptions {
  /** Listener configuration. Defaults to auto-selection of http+https. */
  ports?: WprPorts;
  /** IP address/hostname to bind all servers to. Default '127.0.0.1'. */
  host?: string;
  /** PEM certificate files. Defaults to the PEMs bundled with WebPageReplay. */
  certFiles?: string[] | string;
  /** PEM private key files. Defaults to the PEMs bundled with WebPageReplay. */
  keyFiles?: string[] | string;
  /** JavaScript sources injected in all pages. Defaults to bundled deterministic.js. */
  injectScripts?: string[] | string;
  /** Log verbosity: "DEBUG" | "INFO" | "WARN" | "ERROR". Default "ERROR". */
  logLevel?: string;
  /** Ignore a query parameter for a given URL path: "{Full URL path}::{parameter}". */
  paramToIgnoreInURLPath?: string;
  /** Do not store/read certificates in the archive. Default false. */
  noArchiveCertificates?: boolean;
  /** Constant Math.random() result in [0, 1). */
  constantMathRandomResult?: number;
  /** Inject scripts into HTML responses. Default true. */
  htmlInjection?: boolean;
  /** Inject scripts into JavaScript responses. Default false. */
  jsInjection?: boolean;
  /** Quiet per-request logging. Default false. */
  quietMode?: boolean;
}

export interface WprRecordOptions extends WprBaseOptions {
  /** Where to write the recorded archive. Omit to keep it in memory (returned as a Buffer by stop()). */
  archive?: string;
}

export interface WprReplayOptions extends WprBaseOptions {
  /** Archive file path or raw archive contents to serve. */
  archive: string | Buffer;
  /** Serve matched responses in chronological sequence. Default false. */
  serveResponseInChronologicalSequence?: boolean;
  /** Require exact URL matches. Default false. */
  disableFuzzyURLMatching?: boolean;
  /** File containing replay rules to apply to responses. */
  rulesFile?: string;
  /** Inject scripts stored in the archive on replay. Default false. */
  injectArchiveScripts?: boolean;
}

export interface WprStopResult {
  /** Fully-flushed archive file path (null when a throwaway temp file was used). */
  path: string | null;
  /** Recorded archive as a Buffer (record mode only). */
  archive: Buffer | null;
}

export declare class WprSession {
  /** Numeric session handle used by the native library. */
  readonly id: number;
  /** 'record' or 'replay'. */
  readonly mode: 'record' | 'replay';
  /**
   * Ports actually bound by this session. When a port of 0 was requested,
   * this contains the kernel-selected ephemeral port. -1 = listener disabled.
   */
  readonly ports: Readonly<{ http: number; https: number; httpsToHttp: number }>;
  /** Path of the archive backing this session. */
  readonly archivePath: string | null;
  /** True once stop() has completed. */
  readonly stopped: boolean;
  /**
   * Gracefully stop the session: shut down all proxy servers, flush the
   * recorded archive and return it. Idempotent.
   */
  stop(options?: { timeoutMs?: number }): Promise<WprStopResult>;
  /** Alias for stop(). */
  close(options?: { timeoutMs?: number }): Promise<WprStopResult>;
  toString(): string;
}

/** Start a WebPageReplay session in record mode. */
export declare function startRecord(options?: WprRecordOptions): Promise<WprSession>;

/** Start a WebPageReplay session in replay mode. */
export declare function startReplay(options: WprReplayOptions): Promise<WprSession>;

/** Default listener configuration: { http: 0, https: 0, httpsToHttp: -1 }. */
export declare const DEFAULT_PORTS: Readonly<{
  http: number;
  https: number;
  httpsToHttp: number;
}>;