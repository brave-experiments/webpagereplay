# webpagereplay (Node.js bindings)

Node.js library bindings for [WebPageReplay (WprGo)](../README.md) — record and
replay web traffic from Node.js with a fully async/await API, implemented as
**first-class FFI bindings** into a native Go library (not a CLI wrapper).

```js
import { startRecord } from 'webpagereplay';

const session = await startRecord(); // kernel auto-selects ports by default
console.log(session.ports);          // { http: 43713, https: 45307, httpsToHttp: -1 }

// ...drive a browser through the proxy (session.ports.http / .https)...

const { path, archive } = await session.stop();
// `archive` is a Buffer with the recorded .wprgo archive; `path` is the
// fully-flushed archive file when an explicit archive path was configured.
```

## Features

- **First-class FFI bindings** — the Go runtime is embedded in-process via a
  c-shared library (`libwpr.so` / `libwpr.dylib` / `wpr.dll`) and called
  through [koffi](https://koffi.dev). No CLI is spawned.
- **Async/await API** — every native call runs off the event loop.
- **Automatic kernel port selection** — listeners default to port `0`
  (the CLI's auto-select behavior); the kernel-chosen ports are reported back
  to the caller via `session.ports`.
- **First-class error reporting** — every failure surfaces as a `WprError`
  with a stable `code` (e.g. `PORT_BIND_FAILED` when a port cannot be
  allocated), a human-readable message, and (for port-bind failures)
  structured `details` (`{ scheme, port, host }`).
- **Clean, awaitable exit** — `await session.stop()` shuts down every server
  gracefully, flushes the recorded archive to disk (fsync) and returns it as a
  `Buffer` and/or a fully-flushed written file. `stop()` is idempotent, and the
  session also shuts down cleanly if a browser drives the
  `/web-page-replay-command-exit` control URL.

## Installation

This package lives in the `nodejs/` directory of the WebPageReplay repository.

```shell
cd nodejs
npm install       # installs the koffi FFI runtime
npm run build     # builds the native artifacts from the Go sources
npm test          # runs the test suite
```

`npm run build` produces (for the host platform, or `GOOS`/`GOARCH` if set):

| Artifact | Purpose |
| --- | --- |
| `prebuilds/<plat>-<arch>/libwpr.so` (`.dylib`/`wpr.dll`) | Go c-shared library used by the FFI transport |

## API

### `startRecord(options?) → Promise<WprSession>`

Starts a record session. Options:

| Option | Default | Description |
| --- | --- | --- |
| `archive` | *(none)* | Path of the archive to write. When omitted, the archive is kept in memory and returned as a `Buffer` by `stop()`. |
| `ports` | `{ http: 0, https: 0, httpsToHttp: -1 }` | Listener config: `0` = kernel auto-select, `-1` = disabled, `> 0` = fixed port. |
| `host` | `'127.0.0.1'` | Bind address for all servers. |
| `certFiles` / `keyFiles` | bundled PEMs | TLS certificate/key PEM paths. |
| `injectScripts` | bundled `deterministic.js` | Scripts injected into all pages. |
| `logLevel` | `'ERROR'` | `DEBUG` / `INFO` / `WARN` / `ERROR`. Note: logging is process-global, so the most recent session's level applies to the whole process. |
| `paramToIgnoreInURLPath` | `''` | `"{Full URL path}::{parameter}"` to ignore while recording/replaying. |
| `noArchiveCertificates` | `false` | Do not store/read certificates in the archive. |
| `constantMathRandomResult` | *(unset)* | Constant `Math.random()` result in `[0, 1)`. |
| `htmlInjection` / `jsInjection` | `true` / `false` | Script injection targets. |
| `quietMode` | `false` | Quiet per-request logging. |

### `startReplay(options) → Promise<WprSession>`

Same options as record, plus:

| Option | Default | Description |
| --- | --- | --- |
| `archive` | *(required)* | Archive file path, or the raw archive contents as a `Buffer`. |
| `serveResponseInChronologicalSequence` | `false` | Serve matched responses in recorded order. |
| `disableFuzzyURLMatching` | `false` | Require exact URL matches. |
| `rulesFile` | *(unset)* | Replay rules file. |
| `injectArchiveScripts` | `false` | Inject scripts stored in the archive. |

### `class WprSession`

- `session.ports` — the ports actually bound (`-1` = disabled). For port `0`
  requests this is the kernel-selected ephemeral port.
- `session.ports.http` / `.https` / `.httpsToHttp`
- `await session.stop({ timeoutMs? })` — graceful shutdown; returns
  `{ path, archive }`:
  - `path` — the fully-flushed archive file path. `null` when a throwaway temp
    file was used (in-memory record archives, and archives staged from a
    `Buffer`).
  - `archive` — recorded archive as a `Buffer` (record mode only).
  - Idempotent: calling again resolves with the same result. A failed `stop()`
    can be retried.
  - Flush failures are reported as `WprError` code `ARCHIVE_FLUSH_FAILED`
    rather than silently returning an empty archive.
- `await session.close()` — alias for `stop()`.
- `session.mode`, `session.id`, `session.archivePath`, `session.stopped`

### Error handling

All failures throw (or reject with) a `WprError`:

```js
try {
  await startRecord({ ports: { http: 8080, https: -1 } });
} catch (err) {
  if (err instanceof WprError && err.code === WprErrorCode.PORT_BIND_FAILED) {
    console.error('port unavailable:', err.message);
  }
}
```

Error codes: `NATIVE_LIBRARY_LOAD_FAILED`, `INVALID_CONFIG`,
`ARCHIVE_OPEN_FAILED`, `TLS_CONFIG_FAILED`, `SCRIPT_LOAD_FAILED`,
`PORT_BIND_FAILED`, `ARCHIVE_FLUSH_FAILED`, `SESSION_NOT_FOUND`,
`NOT_READY`, `UNSUPPORTED_MODE`, `INTERNAL_ERROR`.

## Development

```shell
npm run build   # go build into prebuilds/
npm test        # node --test
```

Tests exercise the full record/replay lifecycle (ports, archive flush,
in-memory archives, error paths) and the FFI layer (against a mock native
library, so the mechanics are verified on any platform).
