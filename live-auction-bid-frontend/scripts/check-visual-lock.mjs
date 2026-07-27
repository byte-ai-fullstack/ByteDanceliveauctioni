#!/usr/bin/env node
// 表现锁 ① —— 样式字节锁
//
// 冻结 src/ 下全部 CSS 的字节内容与文件集合。
// 见 docs/REFACTORING_PLAN.md §0.2:样式是本轮唯一不许动的资产。
//
//   node scripts/check-visual-lock.mjs            校验
//   node scripts/check-visual-lock.mjs --update   重新生成锁文件(必须单独提 PR)
//
// 依赖:仅 Node 标准库。

import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const sourceRoot = join(projectRoot, 'src');
const lockPath = join(projectRoot, 'scripts', 'visual-lock.json');

function collectStylesheets(directory) {
  const found = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const absolute = join(directory, entry.name);
    if (entry.isDirectory()) {
      found.push(...collectStylesheets(absolute));
      continue;
    }
    if (entry.isFile() && entry.name.endsWith('.css')) found.push(absolute);
  }
  return found;
}

function buildManifest() {
  const entries = collectStylesheets(sourceRoot)
    .map((absolute) => {
      const contents = readFileSync(absolute);
      return {
        path: relative(projectRoot, absolute).split('\\').join('/'),
        bytes: contents.byteLength,
        sha256: createHash('sha256').update(contents).digest('hex'),
      };
    })
    .sort((left, right) => left.path.localeCompare(right.path));
  return {
    description: '表现锁 ① 样式字节锁。改动必须单独提 PR 并说明视觉影响。见 docs/REFACTORING_PLAN.md §0.2。',
    totalBytes: entries.reduce((sum, entry) => sum + entry.bytes, 0),
    stylesheets: entries,
  };
}

function readLock() {
  try {
    return JSON.parse(readFileSync(lockPath, 'utf8'));
  } catch {
    return null;
  }
}

function diffManifests(locked, current) {
  const lockedByPath = new Map(locked.map((entry) => [entry.path, entry]));
  const currentByPath = new Map(current.map((entry) => [entry.path, entry]));
  const failures = [];

  for (const [path, entry] of currentByPath) {
    const previous = lockedByPath.get(path);
    if (!previous) {
      failures.push(`新增样式文件未登记:${path}(${entry.bytes} B)`);
      continue;
    }
    if (previous.sha256 !== entry.sha256) {
      failures.push(`样式内容被修改:${path}\n    锁定 ${previous.sha256.slice(0, 16)}… (${previous.bytes} B)\n    当前 ${entry.sha256.slice(0, 16)}… (${entry.bytes} B)`);
    }
  }
  for (const path of lockedByPath.keys()) {
    if (!currentByPath.has(path)) failures.push(`已登记的样式文件被删除:${path}`);
  }
  return failures;
}

const manifest = buildManifest();

if (process.argv.includes('--update')) {
  writeFileSync(lockPath, `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(`已写入 ${relative(projectRoot, lockPath)}`);
  console.log(`锁定 ${manifest.stylesheets.length} 个样式文件,共 ${manifest.totalBytes} B`);
  process.exit(0);
}

const lock = readLock();
if (!lock) {
  console.error(`缺少锁文件 ${relative(projectRoot, lockPath)},先运行 node scripts/check-visual-lock.mjs --update`);
  process.exit(1);
}

const failures = diffManifests(lock.stylesheets, manifest.stylesheets);
if (failures.length > 0) {
  console.error('表现锁 ① 失败:样式发生变化。\n');
  for (const failure of failures) console.error(`  ✗ ${failure}`);
  console.error('\n本轮重构不允许改动表现。确需改样式时,请单独提 PR 并运行:');
  console.error('  node scripts/check-visual-lock.mjs --update\n');
  process.exit(1);
}

console.log(`表现锁 ① 通过:${manifest.stylesheets.length} 个样式文件逐字节一致(${manifest.totalBytes} B)`);
