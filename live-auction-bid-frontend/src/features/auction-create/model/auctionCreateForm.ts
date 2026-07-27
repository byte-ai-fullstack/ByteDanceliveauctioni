import type { CreateLotRequest, Money, TrustCardType, UploadedAsset } from '../../../shared/api/types';

type UploadedImage = {
  assetId?: string;
  imageUrl: string;
  fileName?: string;
  sizeBytes?: number | string;
};

export type TrustCardKey = 'certificate' | 'flaw' | 'detail' | 'service';

export type TrustCardDraft = {
  content: string;
  imageUrl: string;
  assetId?: string;
};

export type FormState = {
  title: string;
  description: string;
  imageUrl: string;
  mainImageAssetId?: string;
  gallery: UploadedImage[];
  categoryMode: 'preset' | 'custom';
  category: string;
  tags: string;
  estimatePrice: number | '';
  stock: number;
  afterSaleNotes: string;
  startPrice: number;
  minIncrement: number;
  capPrice: number | '';
  depositAmount: number;
  durationSeconds: number;
  antiSnipeWindowSeconds: number;
  antiSnipeExtendSeconds: number;
  maxExtendCount: number;
  trustCards: Record<TrustCardKey, TrustCardDraft>;
};

export type StepKey = 'product' | 'rules' | 'briefing' | 'review';

type FormIssue = { level: 'error' | 'warning' | 'success'; step: StepKey; text: string };

export const STEP_DEFS: Array<{ key: StepKey; label: string; hint: string }> = [
  { key: 'product', label: '拍品资料', hint: '图片 / 基础信息' },
  { key: 'rules', label: '竞拍规则', hint: '价格 / 延时机制' },
  { key: 'briefing', label: '主播讲解', hint: '证书 / 瑕疵 / 售后' },
  { key: 'review', label: '确认发布', hint: '检查后入队' },
];

export const TRUST_CARD_DEFS: Array<{ key: TrustCardKey; type: TrustCardType; title: string; label: string; placeholder: string }> = [
  { key: 'certificate', type: 'TRUST_CARD_TYPE_CERTIFICATE', title: '证书卡', label: '证书信息', placeholder: '证书编号、鉴定机构、材质证明等' },
  { key: 'flaw', type: 'TRUST_CARD_TYPE_FLAW', title: '瑕疵说明卡', label: '瑕疵说明', placeholder: '如实记录磨损、划痕、缺件或使用痕迹' },
  { key: 'detail', type: 'TRUST_CARD_TYPE_DETAIL', title: '细节展示卡', label: '细节说明', placeholder: '工艺、材质、尺寸、佩戴/使用细节' },
  { key: 'service', type: 'TRUST_CARD_TYPE_SERVICE', title: '售后说明卡', label: '售后说明', placeholder: '退换、支付、发货、客服承诺等' },
];

export const CUSTOM_CATEGORY_VALUE = '__custom__';

export const CATEGORY_OPTIONS = [
  '翡翠玉石',
  '珠宝彩宝',
  '黄金贵金属',
  '腕表配饰',
  '文玩收藏',
  '字画艺术',
  '陶瓷紫砂',
  '潮玩手办',
  '奢侈品',
  '酒水茶叶',
  '数码家电',
  '服饰箱包',
];

export const initialForm: FormState = {
  title: '',
  description: '',
  imageUrl: '',
  gallery: [],
  categoryMode: 'preset',
  category: '',
  tags: '',
  estimatePrice: '',
  stock: 1,
  afterSaleNotes: '',
  startPrice: 0,
  minIncrement: 50,
  capPrice: '',
  depositAmount: 0,
  durationSeconds: 300,
  antiSnipeWindowSeconds: 10,
  antiSnipeExtendSeconds: 15,
  maxExtendCount: 5,
  trustCards: {
    certificate: { content: '', imageUrl: '' },
    flaw: { content: '', imageUrl: '' },
    detail: { content: '', imageUrl: '' },
    service: { content: '', imageUrl: '' },
  },
};

export function imageFromAsset(asset: UploadedAsset, fileName: string): UploadedImage {
  return { assetId: asset.id, imageUrl: asset.imageUrl, fileName, sizeBytes: asset.sizeBytes };
}

export function auctionMoney(amount: number | ''): Money {
  return { amount: Math.max(0, Math.round(Number(amount || 0) * 100)), currency: 'CNY' };
}

function optionalMoney(amount: number | ''): Money | undefined {
  if (amount === '') return undefined;
  return auctionMoney(amount);
}

export function buildTrustCards(form: FormState): CreateLotRequest['trustCards'] {
  return TRUST_CARD_DEFS.flatMap((card) => {
    const draft = form.trustCards[card.key];
    if (!draft.content.trim() && !draft.imageUrl.trim()) return [];
    return [{
      id: `${card.key}-card`,
      type: card.type,
      title: card.title,
      content: draft.content.trim(),
      ...(draft.imageUrl.trim() ? { imageUrl: draft.imageUrl.trim() } : {}),
    }];
  });
}

export function toRequest(form: FormState, roomId: string): CreateLotRequest {
  const request: CreateLotRequest = {
    roomId,
    title: form.title.trim(),
    description: form.description.trim(),
    imageUrl: form.imageUrl.trim(),
    rule: {
      startPrice: auctionMoney(form.startPrice),
      minIncrement: auctionMoney(form.minIncrement),
      ...(form.capPrice !== '' ? { capPrice: auctionMoney(form.capPrice) } : {}),
      durationSeconds: form.durationSeconds,
      antiSnipeWindowSeconds: form.antiSnipeWindowSeconds,
      antiSnipeExtendSeconds: form.antiSnipeExtendSeconds,
      maxExtendCount: form.maxExtendCount,
    },
    trustCards: buildTrustCards(form),
    galleryImageUrls: form.gallery.map((image) => image.imageUrl),
    category: form.category.trim(),
    tags: parseTags(form.tags),
    stock: form.stock,
    afterSaleNotes: form.afterSaleNotes.trim(),
    depositAmount: auctionMoney(form.depositAmount),
  };
  const estimatePrice = optionalMoney(form.estimatePrice);
  if (estimatePrice) request.estimatePrice = estimatePrice;
  return request;
}

export function validate(form: FormState): FormIssue[] {
  const issues: FormIssue[] = [];
  if (!form.title.trim()) issues.push({ level: 'error', step: 'product', text: '拍品名称必填' });
  if (!form.category.trim()) issues.push({ level: 'error', step: 'product', text: form.categoryMode === 'custom' ? '请填写自定义分类' : '请选择拍品分类' });
  if (!form.imageUrl.trim()) issues.push({ level: 'error', step: 'product', text: '主图必须上传' });
  if (form.imageUrl && !isHTTPImageURL(form.imageUrl)) issues.push({ level: 'error', step: 'product', text: '主图必须是 TOS 返回的 http/https URL' });
  form.gallery.forEach((image, index) => {
    if (!isHTTPImageURL(image.imageUrl)) issues.push({ level: 'error', step: 'product', text: `轮播图 ${index + 1} 不是稳定 URL` });
  });
  if (form.gallery.length > 6) issues.push({ level: 'error', step: 'product', text: '轮播图最多 6 张' });
  for (const card of TRUST_CARD_DEFS) {
    const imageURL = form.trustCards[card.key].imageUrl;
    if (imageURL && !isHTTPImageURL(imageURL)) issues.push({ level: 'error', step: 'briefing', text: `${card.label}图片不是稳定 URL` });
  }
  if (!form.description.trim()) issues.push({ level: 'error', step: 'product', text: '拍品介绍必填' });
  if (form.stock < 1) issues.push({ level: 'error', step: 'product', text: '库存必须大于等于 1' });
  if (form.minIncrement <= 0) issues.push({ level: 'error', step: 'rules', text: '加价幅度必须大于 0' });
  if (form.depositAmount < 0) issues.push({ level: 'error', step: 'rules', text: '保证金不能小于 0' });
  if (form.durationSeconds < 60) issues.push({ level: 'error', step: 'rules', text: '竞拍时长必须大于等于 60 秒' });
  if (form.antiSnipeWindowSeconds <= 0) issues.push({ level: 'error', step: 'rules', text: '延时窗口必须大于 0 秒' });
  if (form.antiSnipeExtendSeconds < 10 || form.antiSnipeExtendSeconds > 30) issues.push({ level: 'error', step: 'rules', text: '每次延长必须在 10-30 秒之间' });
  if (form.maxExtendCount <= 0) issues.push({ level: 'error', step: 'rules', text: '最大延时次数必须大于 0' });
  if (form.capPrice !== '' && form.capPrice <= form.startPrice) issues.push({ level: 'error', step: 'rules', text: '封顶价必须大于起拍价' });
  if (!buildTrustCards(form).length) issues.push({ level: 'warning', step: 'briefing', text: '建议至少补充一张讲解卡' });
  return issues;
}

function isHTTPImageURL(value: string) {
  if (value.startsWith('blob:') || value.startsWith('data:')) return false;
  try {
    const url = new URL(value);
    return url.protocol === 'http:' || url.protocol === 'https:';
  } catch {
    return false;
  }
}

export function parseTags(value: string) {
  return value.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean);
}

export function stepIndex(stepKey: StepKey) {
  return STEP_DEFS.findIndex((step) => step.key === stepKey);
}

export function isBlockingIssue(issue: FormIssue) {
  return issue.level === 'error';
}

export function issueText(issues: FormIssue[], keyword: string) {
  return issues.find((issue) => issue.text.includes(keyword))?.text;
}

export function shortURL(value: string) {
  if (value.length <= 54) return value;
  return `${value.slice(0, 28)}...${value.slice(-18)}`;
}
