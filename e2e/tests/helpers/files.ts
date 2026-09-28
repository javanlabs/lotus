import { mkdirSync, truncateSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// sizedFile makes a file of `bytes` zero bytes on disk and returns its path.
// Large uploads go to setInputFiles as a path, not a Buffer: a Buffer is
// sent to the browser base64-encoded over the protocol, which for 20+ MB
// ate the whole test timeout on a busy CI runner (and starved the tests
// running beside it). A path is handed to the browser as is.
export function sizedFile(name: string, bytes: number): string {
  // The browser shows the basename, so the file keeps `name` exactly; the
  // size gets its own directory instead.
  const dir = join(tmpdir(), 'lotus-e2e-files', String(bytes));
  mkdirSync(dir, { recursive: true });
  const path = join(dir, name);
  writeFileSync(path, '');
  truncateSync(path, bytes); // sparse: no 26 MB written either
  return path;
}
