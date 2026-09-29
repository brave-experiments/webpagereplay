#!/usr/bin/env node
/**
 * Builds the native WebPageReplay embedding artifact for the current
 * platform (or the platform specified via GOOS/GOARCH) into nodejs/prebuilds/:
 *
 *  1. The c-shared native library (libwpr.so / libwpr.dylib / wpr.dll), used
 *     by the FFI transport. Note: -buildmode=c-shared only works when building
 *     for the host platform (Go limitation), so it is skipped for cross builds.
 */

import { execFileSync } from 'node:child_process';
import path from 'node:path';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const pkgRoot = path.resolve(__dirname, '..');
const repoRoot = path.resolve(pkgRoot, '..');
const ffiSrc = path.join(repoRoot, 'src', 'wprffi');

const goos = process.env.GOOS || process.platform;
const goarch = process.env.GOARCH || process.arch;
const cross = Boolean(process.env.GOOS || process.env.GOARCH);

const libNames = { linux: 'libwpr.so', darwin: 'libwpr.dylib', win32: 'wpr.dll' };
const outDir = path.join(pkgRoot, 'prebuilds', `${goos}-${goarch}`);
fs.mkdirSync(outDir, { recursive: true });

if (!cross) {
  const libName = libNames[goos];
  if (!libName) {
    console.error(`Unsupported GOOS: ${goos} (supported: linux, darwin, win32)`);
    process.exit(1);
  }
  const outPath = path.join(outDir, libName);
  const args = ['build', '-buildmode=c-shared', '-o', outPath, '.'];
  console.log(`go ${args.join(' ')}   (cwd: ${ffiSrc})`);
  execFileSync('go', args, { cwd: ffiSrc, stdio: 'inherit' });

  // -buildmode=c-shared also emits a C header next to the library on unix.
  const headerPath = outPath.replace(/\.(so|dylib|dll)$/, '.h');
  if (fs.existsSync(headerPath)) {
    fs.copyFileSync(headerPath, path.join(outDir, 'libwpr.h'));
  }
  if (fs.statSync(outPath).size === 0) {
    console.error('Build produced an empty library; see error above.');
    process.exit(1);
  }
  console.log(`Built ${outPath}`);
} else {
  console.log(
    `Skipping c-shared library for cross target ${goos}-${goarch}: ` +
      '-buildmode=c-shared requires native compilation. Run "npm run build" on each target platform.',
  );
}