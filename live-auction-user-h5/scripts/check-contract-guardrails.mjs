import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';

const ROOT = process.cwd();
const CONTRACT_ROOT = path.join(ROOT, 'src/shared/api');

async function listContractSources(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(entries.map(async (entry) => {
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      return entry.name === 'generated' ? [] : listContractSources(absolute);
    }
    return entry.name.endsWith('.ts') && !entry.name.endsWith('.test.ts') ? [absolute] : [];
  }));
  return nested.flat();
}

const files = await listContractSources(CONTRACT_ROOT);
const source = (await Promise.all(files.map((file) => readFile(file, 'utf8')))).join('\n');
const guards = [
  {
    name: 'snake_case payload literals',
    pattern: /['"][a-z][a-z0-9]*(?:_[a-z0-9]+)+['"]/g,
    maximum: 115,
  },
  {
    name: 'silent defaulting helper calls',
    pattern: /\b(?:stringValue|numberValue|normalizeEnum)\s*\(/g,
    maximum: 99,
  },
  {
    name: 'silent scalar fallback expressions',
    pattern: /(?:\?\?|\|\|)\s*(?:0|''|""|'CNY'|"CNY"|[A-Z][A-Z0-9_]*\.UNSPECIFIED)/g,
    maximum: 11,
  },
];

let failed = false;
for (const guard of guards) {
  const count = source.match(guard.pattern)?.length ?? 0;
  const status = count <= guard.maximum ? 'PASS' : 'FAIL';
  console.log(`[${status}] ${guard.name}: ${count}/${guard.maximum}`);
  if (count > guard.maximum) failed = true;
}

if (failed) {
  console.error('Contract guardrails failed: legacy parsing/defaulting debt may only decrease.');
  process.exit(1);
}

console.log(`Contract guardrails passed (${files.length} source files scanned).`);
