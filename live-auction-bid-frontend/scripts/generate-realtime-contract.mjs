import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const backendApi = resolve(projectRoot, '../live-auction-bid-backend/api/auction/service/v1');
const realtimePath = join(backendApi, 'realtime.proto');
const auctionPath = join(backendApi, 'auction.proto');
const outputPath = join(projectRoot, 'src/shared/realtime/generated/realtime.contract.ts');

const realtimeProto = readFileSync(realtimePath, 'utf8');
const auctionProto = readFileSync(auctionPath, 'utf8');
const schemaHash = createHash('sha256').update(realtimeProto).update('\0').update(auctionProto).digest('hex');

function namedBlock(source, kind, name) {
  const match = new RegExp(`\\b${kind}\\s+${name}\\s*\\{`).exec(source);
  if (!match) throw new Error(`未找到 ${kind} ${name}`);
  const open = source.indexOf('{', match.index);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1;
    if (source[index] === '}') depth -= 1;
    if (depth === 0) return source.slice(open + 1, index);
  }
  throw new Error(`${kind} ${name} 缺少结束括号`);
}

function enumType(source, name, exported = false) {
  const values = [...namedBlock(source, 'enum', name).matchAll(/^\s*([A-Z][A-Z0-9_]*)\s*=\s*\d+\s*;/gm)].map((match) => `'${match[1]}'`);
  if (!values.length) throw new Error(`enum ${name} 没有可生成的值`);
  return `${exported ? 'export ' : ''}type ${name} =\n  | ${values.join('\n  | ')};`;
}

function camelCase(value) {
  return value.replace(/_([a-z0-9])/g, (_, letter) => letter.toUpperCase());
}

function fieldType(protoType) {
  if (protoType === 'string') return 'string';
  if (protoType === 'bool') return 'boolean';
  if (/^(u?int|sint|fixed|sfixed)(32|64)$/.test(protoType)) return 'number';
  return protoType;
}

function parseFields(body, forceOptional = false) {
  return [...body.matchAll(/^\s*(optional\s+|repeated\s+)?([A-Za-z][A-Za-z0-9_]*)\s+([a-z][a-z0-9_]*)\s*=\s*\d+\s*;/gm)].map((match) => {
    const modifier = match[1]?.trim();
    const type = `${fieldType(match[2])}${modifier === 'repeated' ? '[]' : ''}`;
    return `  ${camelCase(match[3])}${forceOptional || modifier === 'optional' ? '?' : ''}: ${type};`;
  });
}

function messageType(name, exported = false) {
  let body = namedBlock(realtimeProto, 'message', name);
  const oneofFields = [];
  body = body.replace(/\boneof\s+[A-Za-z][A-Za-z0-9_]*\s*\{([\s\S]*?)\}/g, (_, oneofBody) => {
    oneofFields.push(...parseFields(oneofBody, true));
    return '';
  });
  const fields = [...parseFields(body), ...oneofFields];
  if (!fields.length) throw new Error(`message ${name} 没有可生成的字段`);
  return `${exported ? 'export ' : ''}type ${name} = {\n${fields.join('\n')}\n};`;
}

const messages = [
  'PublicRankingItemV1',
  'PublicSettlementV1',
  'RoomSnapshotPublicV1',
  'PersonalDeltaV1',
  'RoomHeartbeatV1',
  'AdminRankingItemV1',
  'RoomSnapshotAdminV1',
  'ReconnectControlV1',
  'RealtimeEnvelopeV1',
];

const exportedMessages = new Set([
  'RoomSnapshotPublicV1',
  'PersonalDeltaV1',
  'RoomHeartbeatV1',
  'RoomSnapshotAdminV1',
  'RealtimeEnvelopeV1',
]);

const generated = [
  '/**',
  ' * Generated from live-auction-bid-backend/api/auction/service/v1/realtime.proto.',
  ` * Source SHA-256: ${schemaHash}`,
  ' * Do not edit by hand.',
  ' */',
  '',
  enumType(auctionProto, 'LotStatus', true),
  '',
  enumType(auctionProto, 'OrderVisibility'),
  '',
  ...messages.flatMap((name) => [messageType(name, exportedMessages.has(name)), '']),
].join('\n');

mkdirSync(dirname(outputPath), { recursive: true });
writeFileSync(outputPath, generated, 'utf8');
console.log(`已生成实时契约：${outputPath}`);
