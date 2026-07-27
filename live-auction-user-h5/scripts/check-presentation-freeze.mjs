import { createHash } from 'node:crypto';
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';

const ROOT = process.cwd();
const MANIFEST_PATH = new URL('./presentation-freeze.manifest.json', import.meta.url);
const manifest = JSON.parse(await readFile(MANIFEST_PATH, 'utf8'));

const pageCss = (await readdir(path.join(ROOT, 'src/pages')))
  .filter((name) => name.endsWith('-replica.css'))
  .map((name) => `src/pages/${name}`);
const frozenFiles = [
  'src/index.css',
  'src/app/styles.css',
  'src/app/styles.semantic-tokens.css',
  'src/app/recovered-overlays.css',
  'src/app/live-product-carousel.css',
  ...pageCss,
].sort();
const manifestedFiles = Object.keys(manifest).sort();

if (JSON.stringify(frozenFiles) !== JSON.stringify(manifestedFiles)) {
  console.error('Presentation freeze manifest does not match the frozen CSS file set.');
  console.error(`Expected: ${frozenFiles.join(', ')}`);
  console.error(`Manifest: ${manifestedFiles.join(', ')}`);
  process.exit(1);
}

const mismatches = [];
for (const relativePath of frozenFiles) {
  const content = await readFile(path.join(ROOT, relativePath));
  const actual = createHash('sha256').update(content).digest('hex');
  const expected = manifest[relativePath];
  if (actual !== expected) mismatches.push({ relativePath, expected, actual });
}

if (mismatches.length) {
  console.error(`Presentation freeze failed (${mismatches.length} CSS file(s) changed).`);
  for (const mismatch of mismatches) {
    console.error(`${mismatch.relativePath}\n  expected ${mismatch.expected}\n  actual   ${mismatch.actual}`);
  }
  process.exit(1);
}

console.log(`Presentation freeze passed (${frozenFiles.length} CSS files).`);
