import { describe, it, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { startRecord, startReplay, WprError, WprErrorCode, DEFAULT_PORTS } from '../src/index.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const prebuild = path.join(__dirname, '..', 'prebuilds', `${process.platform}-${process.arch}`);
const hasNative = fs.existsSync(path.join(prebuild, process.platform === 'win32' ? 'wpr.dll' : 'libwpr.so'));

function httpGet(url) {
  return new Promise((resolve, reject) => {
    http
      .get(url, (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => resolve({ status: res.statusCode, body: Buffer.concat(chunks) }));
      })
      .on('error', reject);
  });
}

/** Reserves a fixed port so that another listener binding to it fails. */
function reservePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => resolve({ port: srv.address().port, release: () => srv.close() }));
    srv.on('error', reject);
  });
}

describe('webpagereplay (FFI bindings)', { skip: hasNative ? false : `native library not built in ${prebuild} (run: npm run build)` }, () => {
  describe('port selection', () => {
    it('auto-selects kernel ports and reports them back', async () => {
      const session = await startRecord();
      try {
        assert.equal(session.mode, 'record');
        assert.ok(session.ports.http > 0, `ports.http should be an actual port, got ${session.ports.http}`);
        assert.ok(session.ports.https > 0, `ports.https should be an actual port, got ${session.ports.https}`);
        assert.equal(session.ports.httpsToHttp, -1);
      } finally {
        await session.stop();
      }
    });

    it('honors explicit fixed ports and reports them back', async () => {
      const { port, release } = await reservePort();
      release();
      const session = await startRecord({ ports: { http: port, https: -1 } });
      try {
        assert.equal(session.ports.http, port);
        assert.equal(session.ports.https, -1);
      } finally {
        await session.stop();
      }
    });

    it('rejects invalid port values with a first-class error', async () => {
      await assert.rejects(
        startRecord({ ports: { http: -42 } }),
        (err) => err instanceof WprError && err.code === WprErrorCode.INVALID_CONFIG,
      );
    });
  });

  describe('record mode', () => {
    it('serves requests and yields a flushed archive file on stop()', async () => {
      const archivePath = path.join(os.tmpdir(), `wpr-test-${process.pid}-${Date.now()}.wprgo`);
      let session = null;
      try {
        session = await startRecord({ archive: archivePath });
        const res = await httpGet(`http://127.0.0.1:${session.ports.http}/web-page-replay-generate-200`);
        assert.equal(res.status, 200);

        const result = await session.stop();
        assert.equal(session.stopped, true);
        assert.equal(result.path, archivePath);
        assert.ok(Buffer.isBuffer(result.archive));
        assert.ok(result.archive.length > 0, 'archive buffer should not be empty');

        const onDisk = fs.readFileSync(archivePath);
        assert.deepEqual(onDisk, result.archive, 'flushed file must equal returned buffer');

        // stop() is idempotent and returns the same result.
        const again = await session.stop();
        assert.equal(again.archive.length, result.archive.length);
      } finally {
        if (session) await session.stop().catch(() => {});
        fs.rmSync(archivePath, { force: true });
      }
    });

    it('can keep the archive purely in memory (buffer only)', async () => {
      const session = await startRecord(); // no archive path
      try {
        await httpGet(`http://127.0.0.1:${session.ports.http}/web-page-replay-generate-200`);
        const result = await session.stop();
        assert.equal(result.path, null);
        assert.ok(Buffer.isBuffer(result.archive) && result.archive.length > 0);
      } catch (err) {
        await session.stop().catch(() => {});
        throw err;
      }
    });

    it('reports port allocation failures with PORT_BIND_FAILED', async () => {
      const { port, release } = await reservePort();
      try {
        await assert.rejects(
          startRecord({ ports: { http: port, https: -1 } }),
          (err) => {
            assert.ok(err instanceof WprError, `expected WprError, got ${err?.constructor?.name}: ${err}`);
            assert.equal(err.code, WprErrorCode.PORT_BIND_FAILED);
            assert.match(err.message, /http/i);
            assert.deepEqual(err.details, { scheme: 'http', port, host: '127.0.0.1' });
            return true;
          },
        );
      } finally {
        release();
      }
    });
  });

  describe('replay mode', () => {
    let recordedArchive;
    let recordedArchivePath;

    before(async () => {
      const session = await startRecord();
      try {
        await httpGet(`http://127.0.0.1:${session.ports.http}/web-page-replay-generate-200`);
        const result = await session.stop();
        recordedArchive = result.archive;
        recordedArchivePath = path.join(os.tmpdir(), `wpr-replay-src-${process.pid}.wprgo`);
        fs.writeFileSync(recordedArchivePath, recordedArchive);
      } catch (err) {
        await session.stop().catch(() => {});
        throw err;
      }
    });

    after(() => {
      if (recordedArchivePath) fs.rmSync(recordedArchivePath, { force: true });
    });

    it('replays from a file-path archive', async () => {
      const session = await startReplay({
        archive: recordedArchivePath,
        quietMode: true,
        serveResponseInChronologicalSequence: true,
        disableFuzzyURLMatching: true,
      });
      try {
        const res = await httpGet(`http://127.0.0.1:${session.ports.http}/web-page-replay-generate-200`);
        assert.equal(res.status, 200);
        const result = await session.stop();
        assert.equal(result.archive, null); // no archive is produced in replay mode
      } finally {
        await session.stop().catch(() => {});
      }
    });

    it('replays from a raw Buffer archive', async () => {
      const session = await startReplay({ archive: recordedArchive, quietMode: true });
      try {
        const res = await httpGet(`http://127.0.0.1:${session.ports.http}/web-page-replay-generate-200`);
        assert.equal(res.status, 200);
      } finally {
        await session.stop();
      }
    });

    it('rejects replay-only options in record mode', async () => {
      await assert.rejects(
        startRecord({ rulesFile: '/tmp/x.rules' }),
        (err) => err instanceof WprError && err.code === WprErrorCode.INVALID_CONFIG,
      );
    });

    it('rejects a missing replay archive', async () => {
      await assert.rejects(
        startReplay({ archive: '/nonexistent/wpr/missing.wprgo' }),
        (err) => err instanceof WprError && err.code === WprErrorCode.ARCHIVE_OPEN_FAILED,
      );
    });
  });

  describe('session ergonomics', () => {
    it('exposes default ports constant', () => {
      assert.deepEqual(DEFAULT_PORTS, { http: 0, https: 0, httpsToHttp: -1 });
    });

    it('handles the /web-page-replay-command-exit control URL gracefully', async () => {
      // The CLI kills the whole process on this URL; the library stops the
      // session instead and stop() still returns the recorded archive.
      const session = await startRecord();
      try {
        const res = await httpGet(`http://127.0.0.1:${session.ports.http}/web-page-replay-command-exit`);
        assert.equal(res.status, 200);
        const result = await session.stop();
        assert.ok(Buffer.isBuffer(result.archive) && result.archive.length > 0);
      } catch (err) {
        await session.stop().catch(() => {});
        throw err;
      }
    });

    it('toString() summarizes the session', async () => {
      const session = await startRecord({ ports: { https: -1 } });
      try {
        assert.match(session.toString(), /record/);
        assert.match(session.toString(), /http=\d+/);
      } finally {
        await session.stop();
      }
    });
  });
});