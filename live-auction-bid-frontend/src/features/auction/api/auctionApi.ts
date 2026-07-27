import { apiRequest } from '../../../shared/api/httpClient';
import { toQueryString } from '../../../shared/api/query';
import { assertOkResult, publicResultMessage } from '../../../shared/api/result';
import { normalizeLot, normalizeRoom, normalizeRoomSnapshot, normalizeTrustRevealCard, normalizeUploadedAsset } from '../../../shared/api/normalizers';
import { clientLog, createRequestId } from '../../../shared/lib/clientLogger';
import type { AdminLotsReply, CancelLotReply, CreateLotReply, CreateLotRequest, GetRoomSnapshotReply, ListRoomsReply, Lot, LotStatus, PatchLotDraftRequest, PatchLotDraftReply, QueueLotReply, RevealTrustCardReply, Room, RoomSnapshot, SettleLotReply, StartDuelReply, StartLotReply, TrustRevealCard, UploadedAsset, UploadImageReply } from '../../../shared/api/types';

export type AdminLotsQuery = {
  page?: number;
  pageSize?: number;
  status?: LotStatus | '';
  view?: 'current' | 'history' | 'library' | '';
  keyword?: string;
  roomId?: string;
};

export type AdminLotsPage = {
  lots: Lot[];
  total: number;
  page: number;
  pageSize: number;
};

export async function listAdminRooms(options: { signal?: AbortSignal } = {}): Promise<Room[]> {
  const reply = assertOkResult(await apiRequest<ListRoomsReply>({
    path: '/api/admin/rooms',
    method: 'GET',
    operation: 'admin-list-rooms',
    signal: options.signal,
  }));
  return requireArray(reply.rooms, 'rooms').map(normalizeRoom);
}

function formatApiError(input: { status?: number; code?: number; message?: string; requestId?: string; result?: { code?: number; message?: string; traceId?: string; trace_id?: string }; error?: string }) {
  const code = input.code ?? input.result?.code;
  return publicResultMessage(input.result ?? (code !== undefined ? { code, message: input.message } : undefined), input.message || input.error || (input.status ? `HTTP ${input.status}` : '请求失败'));
}

function requireLot(reply: { lot?: unknown }) {
  if (!reply.lot) throw new Error('response missing lot');
  return normalizeLot(reply.lot);
}

function requireArray<T>(value: T[] | undefined, field: string): T[] {
  if (!Array.isArray(value)) throw new Error(`response missing ${field}`);
  return value;
}

function requiredValue<T>(value: T | undefined | null, field: string): T {
  if (value === undefined || value === null || value === '') throw new Error(`response missing ${field}`);
  return value;
}

export async function listAdminLots(query: AdminLotsQuery = {}, signal?: AbortSignal): Promise<AdminLotsPage> {
  const page = query.page ?? 1;
  const pageSize = query.pageSize ?? 20;
  const reply = assertOkResult(await apiRequest<AdminLotsReply>({
    path: `/api/admin/lots${toQueryString({
      page,
      pageSize,
      status: query.status,
      view: query.view,
      keyword: query.keyword?.trim(),
      roomId: query.roomId,
    })}`,
    method: 'GET',
    operation: 'admin-list-lots',
    signal,
  }));
  return {
    lots: requireArray(reply.lots, 'lots').map(normalizeLot),
    total: Number(requiredValue(reply.total, 'total')),
    page: Number(requiredValue(reply.page, 'page')),
    pageSize: Number(requiredValue(reply.pageSize, 'pageSize')),
  };
}

export async function getRoomSnapshot(roomId: string, signal?: AbortSignal): Promise<RoomSnapshot> {
  const reply = assertOkResult(await apiRequest<GetRoomSnapshotReply>({
    path: `/api/rooms/${encodeURIComponent(roomId)}/snapshot`,
    method: 'GET',
    operation: 'room-snapshot',
    signal,
  }));
  if (!reply.snapshot) throw new Error('response missing snapshot');
  return normalizeRoomSnapshot(reply.snapshot);
}

export async function uploadImage(file: File, input?: { roomId?: string; bizType?: string }): Promise<UploadedAsset> {
  const form = new FormData();
  form.append('file', file);
  if (input?.roomId) form.append('roomId', input.roomId);
  if (input?.bizType) form.append('bizType', input.bizType);
  const requestId = createRequestId('upload');
  const startedAt = performance.now();
  clientLog('info', 'upload_image.request', {
    requestId,
    endpoint: '/api/uploads/images',
    roomId: input?.roomId,
    bizType: input?.bizType,
    fileName: file.name,
    fileType: file.type,
    fileSize: file.size,
  });
  try {
    const reply = await apiRequest<UploadImageReply>({
      path: '/api/uploads/images',
      method: 'POST',
      body: form,
      bodyMode: 'form-data',
      operation: 'upload-image',
      requestId,
    });
    const code = reply.code ?? reply.result?.code ?? 0;
    if (code !== 0) throw new Error(formatApiError(reply));
    const asset = normalizeUploadedAsset(reply.data?.asset);
    clientLog('info', 'upload_image.success', {
      requestId: reply.requestId ?? requestId,
      durationMs: Math.round(performance.now() - startedAt),
      code,
      assetId: asset.id,
      imageUrl: asset.imageUrl,
      objectKey: asset.objectKey,
      mimeType: asset.mimeType,
      sizeBytes: asset.sizeBytes,
    });
    return asset;
  } catch (error) {
    clientLog('error', 'upload_image.failure', {
      requestId,
      durationMs: Math.round(performance.now() - startedAt),
      error: error instanceof Error ? error.message : String(error),
    });
    throw error;
  }
}

export async function deleteUploadedImage(assetId: string, options?: { keepalive?: boolean; silent?: boolean }): Promise<void> {
  if (!assetId) return;
  const requestId = createRequestId('delete-upload');
  try {
    await apiRequest<void>({
      path: `/api/uploads/images/${encodeURIComponent(assetId)}`,
      method: 'DELETE',
      keepalive: options?.keepalive,
      operation: 'delete-upload',
      requestId,
      retryAuth: !options?.keepalive,
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (options?.silent) {
      clientLog('warn', 'upload_image.delete_failure', { requestId, assetId, message });
      return;
    }
    throw error;
  }
  clientLog('info', 'upload_image.deleted', { requestId, assetId });
}

export async function createDraftLot(payload: Partial<CreateLotRequest> = {}) {
  return requireLot(assertOkResult(await apiRequest<CreateLotReply>({ path: '/api/lots/drafts', method: 'POST', body: payload, operation: 'create-draft-lot' })));
}

export async function patchDraftLot(lotId: string, payload: Partial<CreateLotRequest>) {
  return requireLot(assertOkResult(await apiRequest<PatchLotDraftReply>({
    path: `/api/lots/${lotId}/draft`,
    method: 'PATCH',
    body: { lotId, ...payload } satisfies PatchLotDraftRequest,
    operation: 'patch-draft-lot',
  })));
}

export async function queueLot(lotId: string) {
  const reply = assertOkResult(await apiRequest<QueueLotReply>({ path: `/api/lots/${lotId}/queue`, method: 'POST', body: { lotId }, operation: 'queue-lot' }));
  if (!reply.lot) throw new Error('response missing lot');
  const lot = normalizeLot(reply.lot);
  return { lot, queuePosition: reply.queuePosition ?? lot.queuePosition } as { lot: Lot; queuePosition?: number };
}

export async function startLot(lotId: string) {
  return requireLot(assertOkResult(await apiRequest<StartLotReply>({ path: `/api/lots/${lotId}/start`, method: 'POST', body: {}, operation: 'start-lot' })));
}

export async function revealTrustCard(lotId: string, cardId: string) {
  const reply = assertOkResult(await apiRequest<RevealTrustCardReply>({
    path: `/api/lots/${lotId}/trust-cards/${cardId}/reveal`,
    method: 'POST',
    body: {},
    operation: 'reveal-trust-card',
  }));
  if (!reply.lot || !reply.trustCard) throw new Error('response missing lot or trust card');
  return { lot: normalizeLot(reply.lot), trustCard: normalizeTrustRevealCard(reply.trustCard) } as { lot: Lot; trustCard: TrustRevealCard };
}

export async function startDuel(lotId: string) {
  return requireLot(assertOkResult(await apiRequest<StartDuelReply>({ path: `/api/lots/${lotId}/duel`, method: 'POST', body: {}, operation: 'start-duel' })));
}

export async function settleLot(lotId: string) {
  return requireLot(assertOkResult(await apiRequest<SettleLotReply>({ path: `/api/lots/${lotId}/settle`, method: 'POST', body: {}, operation: 'settle-lot' })));
}

export async function cancelLot(lotId: string, reason: string) {
  return requireLot(assertOkResult(await apiRequest<CancelLotReply>({
    path: `/api/lots/${lotId}/cancel`,
    method: 'POST',
    body: { lotId, reason },
    operation: 'cancel-lot',
  })));
}
