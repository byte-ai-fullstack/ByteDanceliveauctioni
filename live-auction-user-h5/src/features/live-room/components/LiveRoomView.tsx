import { type CSSProperties, type TouchEvent, type WheelEvent, useEffect, useRef, useState } from 'react';
import { LivePlayer } from '../../live/components/LivePlayer';
import { resolveLivePlaylist } from '../../live/hooks/useLivePlayer';
import { DepositPayModal } from '../../payment-flow/components/DepositPayModal';
import { MockPayModal } from '../../payment-flow/components/MockPayModal';
import { ResultModal } from '../../result-modal/components/ResultModal';
import { businessErrorMessage } from '../../../shared/api/errors';
import type { Lot } from '../../../shared/api/types';
import { formatMoney, moneyNumber } from '../../../shared/lib/money';
import { getServerNowMs } from '../../../shared/lib/time';
import { navigateTo } from '../../../shared/navigation';
import type { LiveRoomController } from '../hooks/useLiveRoomController';
import { deriveLotDisplayState, orderForLot } from '../model/lotDisplayState';
import { AuctionDrawer } from './AuctionDrawer';
import { AuctionNoticeLayer } from './AuctionNoticeLayer';
import { LiveProductDetailOverlay } from './LiveProductDetailOverlay';

function shortCount(value: number): string {
  if (value >= 10000) return `${(value / 10000).toFixed(value >= 100000 ? 0 : 1)}w`;
  return value.toLocaleString('zh-CN');
}

const SHARE_OPTIONS = ['转发', '私信朋友', '复制链接', '生成图片', '举报', '不感兴趣'];
const SHARE_FRIENDS = ['拍友A', '严选群', '收藏顾问', '直播搭子'];
const GIFT_OPTIONS = [
  { name: '小心心', value: '1', image: '/douyin-assets/gifts/xiao-xin-xin.png' },
  { name: '棒棒糖', value: '9', image: '/douyin-assets/gifts/bang-bang-tang.png' },
  { name: '人气票', value: '18', image: '/douyin-assets/gifts/ren-qi-piao.png' },
  { name: '烟花', value: '66', image: '/douyin-assets/gifts/yan-hua.png' },
  { name: '星河', value: '188', image: '/douyin-assets/gifts/xing-he.png' },
  { name: '嘉年华', value: '999', image: '/douyin-assets/gifts/jia-nian-hua.png' },
];
const MORE_ACTIONS = ['清屏', '小窗播放', '画质', '直播公告', '订单', '举报'];
const LIVE_ROOM_SWIPE_DISTANCE = 52;
const LIVE_ROOM_QUICK_SWIPE_MS = 280;
const LIVE_ROOM_WHEEL_SWIPE_DISTANCE = 88;
const LIVE_ROOM_WHEEL_LOCK_MS = 420;
const LIVE_ROOM_WHEEL_STALE_MS = 180;
const LIVE_ROOM_SETTLE_MS = 230;
const LIVE_AVATAR_POOL = [
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/avatar-71158770-d8597.jpeg',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/avatar-lsy0508-160edjy.jpeg',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/avatar-ll991221-1bmdvg4.jpeg',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/avatar-sunmeng333-qheb8m.jpeg',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/avatar-jingyiziran-176539n.jpeg',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/avatar-8357999-1bd1vnm.jpeg',
];

type RoomSwipeDirection = 1 | -1;
type RoomSwipePhase = 'idle' | 'dragging' | 'settling';
type RoomSwitchPreview = { id: string; name: string };

function roomSwipeDistance(distance: number, elapsed: number) {
  return Math.abs(distance) > (elapsed < LIVE_ROOM_QUICK_SWIPE_MS ? LIVE_ROOM_SWIPE_DISTANCE * 0.72 : LIVE_ROOM_SWIPE_DISTANCE);
}

function liveRoomViewportHeight() {
  return Math.max(window.innerHeight || 0, document.documentElement.clientHeight || 0, 640);
}

function rubberBandRoomSwipeOffset(distance: number) {
  const viewportHeight = liveRoomViewportHeight();
  const limit = viewportHeight * 0.96;
  const absDistance = Math.abs(distance);
  if (absDistance <= limit) return distance;
  return Math.sign(distance) * (limit + (absDistance - limit) * 0.18);
}

function shouldIgnoreRoomSwipe(target: EventTarget | null) {
  if (!(target instanceof Element)) return false;
  return Boolean(target.closest([
    'button',
    'a',
    'input',
    'textarea',
    'select',
    '[role="button"]',
    '.auctionDrawerMask',
    '.auctionDrawer',
    '.dyCommentOverlay',
    '.dyCommentSheet',
    '.douyinSheetMask',
    '.douyinBottomSheet',
    '.liveProductFloatCard',
    '.liveBidLeaderboard',
    '.liveComposer',
    '.depositPayModal',
    '.mockPayModal',
    '.resultModal',
    '.liveProductDetailOverlay',
  ].join(',')));
}

function stableAvatarIndex(key: string): number {
  return Array.from(key || 'live-room').reduce((hash, char) => (hash * 31 + char.charCodeAt(0)) % LIVE_AVATAR_POOL.length, 0);
}

function liveAvatarFor(key: string, offset = 0): string {
  return LIVE_AVATAR_POOL[(stableAvatarIndex(key) + offset) % LIVE_AVATAR_POOL.length];
}

function livePreviewSourceFor(key: string): string {
  const playlist = resolveLivePlaylist();
  if (!playlist.length) return '/demo-live.mp4';
  return playlist[stableAvatarIndex(key) % playlist.length] || playlist[0] || '/demo-live.mp4';
}

function firstNameChar(name: string): string {
  return name.trim().slice(0, 1) || '拍';
}

function AvatarMedia({ src, name }: { src: string; name: string }) {
  const [failed, setFailed] = useState(false);
  if (failed) return <>{firstNameChar(name)}</>;
  return <img src={src} alt="" referrerPolicy="no-referrer" loading="lazy" onError={() => setFailed(true)} />;
}

function currentLotDisplayState(controller: LiveRoomController) {
  const { currentLot, room } = controller;
  if (!currentLot) return undefined;
  return deriveLotDisplayState(currentLot, {
    order: orderForLot(room.orders, currentLot),
    paymentKnownPaid: Boolean(room.paidLotIds[currentLot.id]),
    nowMs: getServerNowMs(room.serverTimeUnixMs, room.serverTimeReceivedAtUnixMs),
  });
}

function liveLotStatusLabel(controller: LiveRoomController): string {
  const state = currentLotDisplayState(controller);
  if (!state) return '等待上架';
  if (state === 'live') return '竞拍中';
  if (state === 'pendingPayment') return '待支付';
  if (state === 'upcoming' || state === 'syncing') return '等待上架';
  return '已结束';
}

function LiveRoomChrome({ controller, onCloseRoom }: { controller: LiveRoomController; onCloseRoom?: () => void }) {
  const { anchorName, currentLot, room } = controller;
  const statusLabel = liveLotStatusLabel(controller);
  const likes = Math.max(room.snapshot?.onlineCount || 0, 93000);
  const viewers = room.ranking.slice(0, 3);
  const viewerAvatars = viewers.length ? viewers.map((viewer, index) => {
    const name = viewer.nickname || viewer.userId || `拍友${index + 1}`;
    return {
      id: viewer.userId || `viewer-${index}`,
      name,
      avatarUrl: viewer.avatarUrl || liveAvatarFor(`${room.roomId}:${name}`, index + 1),
    };
  }) : [
    { id: 'fallback-buyer', name: '拍友', avatarUrl: liveAvatarFor(`${room.roomId}:buyer`, 1) },
    { id: 'fallback-collector', name: '藏家', avatarUrl: liveAvatarFor(`${room.roomId}:collector`, 2) },
    { id: 'fallback-bidder', name: '竞拍', avatarUrl: liveAvatarFor(`${room.roomId}:bidder`, 3) },
  ];
  const topRank = Math.max(1, Math.min(99, Number(String(room.roomId).replace(/\D/g, '').slice(-2)) || 15));
  const closeRoom = () => {
    if (onCloseRoom) {
      onCloseRoom();
      return;
    }

    navigateTo('/home', { replace: true });
  };

  return (
    <section className="liveRoomChrome" aria-label="直播间信息">
      <div className="liveRoomTop">
        <div className="liveRoomTopLeft">
          <div className="liveAnchorLine">
            <div className="anchorAvatar">
              <AvatarMedia src={liveAvatarFor(`${room.roomId}:${anchorName}:anchor`)} name={anchorName} />
            </div>
            <div className="anchorCopy">
              <strong>{anchorName}</strong>
              <span>{shortCount(likes)}本场点赞</span>
            </div>
            <button type="button" className="followAnchor" onClick={() => controller.actions.showNotice('已关注主播')}>关注</button>
          </div>
          <div className="liveAnchorTags" aria-label="直播标签">
            <span>讲解</span>
            <span>严选第{topRank}名</span>
            <button type="button" onClick={() => controller.actions.showNotice(currentLot ? `${currentLot.title}：${statusLabel}` : '等待主播上架商品')}>
              {statusLabel} ›
            </button>
          </div>
        </div>
        <div className="liveRoomTopRight">
          <div className="liveViewerStack" aria-label="在线观众">
            {viewerAvatars.map((viewer, index) => (
              <span key={`${viewer.id}-${index}`}>
                <AvatarMedia src={viewer.avatarUrl} name={viewer.name} />
              </span>
            ))}
            <b>{shortCount(Math.max(room.snapshot?.onlineCount || 0, 107))}</b>
            <button type="button" className="closeLive" aria-label="关闭直播间" onClick={closeRoom}>×</button>
          </div>
          <button type="button" className="moreCity" onClick={() => controller.actions.showNotice('更多同城直播暂未开放')}>
            <span>更多同城</span>
            <i>›</i>
          </button>
        </div>
      </div>
    </section>
  );
}

type LeaderboardEntry = LiveRoomController['ranking'][number];

function uniqueLeaderboardEntries(entries: LeaderboardEntry[]): LeaderboardEntry[] {
  const byBidder = new Map<string, LeaderboardEntry>();
  entries.forEach((entry, index) => {
    const key = entry.userId
      ? `user:${entry.userId}`
      : entry.nickname
        ? `public:${entry.rank || index + 1}:${entry.nickname}:${moneyNumber(entry.amount)}`
        : `rank:${entry.rank || index + 1}:${moneyNumber(entry.amount)}`;
    const existing = byBidder.get(key);
    if (!existing || moneyNumber(entry.amount) > moneyNumber(existing.amount)) byBidder.set(key, entry);
  });

  return Array.from(byBidder.values())
    .sort((a, b) => moneyNumber(b.amount) - moneyNumber(a.amount))
    .map((entry, index) => ({ ...entry, rank: index + 1 }));
}

function LiveBidLeaderboard({
  controller,
  onOpenAuction,
  collapsed,
  onToggleCollapsed,
}: {
  controller: LiveRoomController;
  onOpenAuction: () => void;
  collapsed: boolean;
  onToggleCollapsed: () => void;
}) {
  const { currentLot, ranking, room } = controller;
  if (!currentLot) return null;
  if (currentLotDisplayState(controller) !== 'live') return null;

  const acceptedRecentBids = room.recentBids.filter((bid) => bid.accepted !== false && (!bid.lotId || bid.lotId === currentLot.id));
  const bidCount = Math.max(currentLot.stats.bidCount, acceptedRecentBids.length);
  const uniqueRanking = uniqueLeaderboardEntries(ranking);
  const participantCount = Math.max(currentLot.stats.participantCount, uniqueRanking.length);
  const fallbackLeader: LeaderboardEntry | null = !uniqueRanking.length && (currentLot.leadingNickname || currentLot.leadingUserId)
    ? {
        rank: 1,
        userId: currentLot.leadingUserId || 'leader',
        nickname: currentLot.leadingNickname || '领先拍友',
        amount: currentLot.currentPrice,
        isMe: currentLot.leadingUserId === controller.meId,
      }
    : null;
  const entries = (uniqueRanking.length ? uniqueRanking : fallbackLeader ? [fallbackLeader] : []).slice(0, 3);
  const hasBid = bidCount > 0 || entries.length > 0;
  const leadAmountText = hasBid ? formatMoney(entries[0]?.amount || currentLot.currentPrice) : '未出价';

  if (collapsed) {
    return (
      <button
        type="button"
        className={`liveBidLeaderboard isCollapsed${hasBid ? '' : ' empty'}`}
        onClick={onToggleCollapsed}
        aria-label="展开竞拍排行榜"
      >
        <span className="leaderboardCollapsedPill">
          <span>{hasBid ? '竞拍榜' : '等首拍'}</span>
          <strong title={leadAmountText}>{leadAmountText}</strong>
          <i aria-hidden="true">›</i>
        </span>
      </button>
    );
  }

  return (
    <section
      className={`liveBidLeaderboard${hasBid ? '' : ' empty'}`}
      aria-label="竞拍排行榜"
    >
      <button type="button" className="leaderboardTop" onClick={onToggleCollapsed} aria-label="收起竞拍排行榜">
        <span className="leaderboardTitle">
          <b>{hasBid ? '竞拍榜' : '等首拍'}</b>
          <small>{participantCount}人围观 · {bidCount}口</small>
        </span>
        <strong title={leadAmountText}>{leadAmountText}</strong>
        <i className="leaderboardChevron" aria-hidden="true" />
      </button>
      <button type="button" className="leaderboardRows" onClick={onOpenAuction} aria-label="打开商品橱窗查看出价">
        {entries.length ? entries.map((entry, index) => {
          const name = entry.nickname || (entry.userId ? `拍友${entry.userId.slice(-4)}` : `拍友${index + 1}`);
          const amountText = formatMoney(entry.amount);
          return (
            <span className={`leaderboardRow${entry.isMe ? ' me' : ''}`} key={`${entry.userId || name}-${index}`}>
              <em>{entry.rank || index + 1}</em>
              <span className="leaderboardAvatar">
                <AvatarMedia src={entry.avatarUrl || liveAvatarFor(`${room.roomId}:${name}:rank`, index)} name={name} />
              </span>
              <b>{entry.isMe ? '我' : name}</b>
              <strong title={amountText}>{amountText}</strong>
            </span>
          );
        }) : <span className="leaderboardEmpty">等第一口出价</span>}
      </button>
    </section>
  );
}

type GiftBurst = { id: number; name: string };

function LiveRoomEffectsLayer({ controller, giftBurst }: { controller: LiveRoomController; giftBurst: GiftBurst | null }) {
  const { anchorName, currentLot, room, notices } = controller;
  type ChatMessage = { id: string; name: string; text: string; system?: boolean };
  const seededMessages = room.recentBids.slice(0, 4).map((bid, index) => ({
    id: bid.id || `${bid.userId}-${index}`,
    name: bid.nickname || `拍友${bid.userId.slice(-4)}`,
    text: bid.accepted === false
      ? businessErrorMessage(bid.rejectReason, { lot: currentLot || undefined }) || '刚刚互动未成功'
      : `刚刚关注了这件商品`,
  }));
  const messages: ChatMessage[] = seededMessages.length ? seededMessages : [
    { id: 'demo-welcome', name: anchorName, text: currentLot ? `正在竞拍 ${currentLot.title}` : '欢迎来到直播间' },
    { id: 'demo-detail', name: '拍友7281', text: '主播可以再看一下细节吗' },
    { id: 'demo-product', name: '收藏新手', text: '等主播讲到细节我再看商品' },
  ];
  const safetyNotice = {
    id: 'system-safe-notice',
    name: '系统提示',
    text: '平台倡导理性消费，请勿私下交易，订单和支付以 LiveAuction 页面为准。',
    system: true,
  };
  const chatMessages: ChatMessage[] = [safetyNotice, ...messages];
  const joiners = (room.ranking.length ? room.ranking : [
    { userId: 'demo-a', nickname: '严选拍友' },
    { userId: 'demo-b', nickname: '收藏顾问' },
    { userId: 'demo-c', nickname: '拍场观察' },
  ]).slice(0, 3);
  const barrage = [
    currentLot ? `${currentLot.title} 正在竞拍` : `${anchorName} 正在直播`,
    notices[0] || '喜欢这件可以打开商品',
    room.recentBids[0] ? `${room.recentBids[0].nickname || '拍友'} 刚刚关注商品` : '直播间热度上升',
  ];

  return (
    <section className="liveEffectsLayer" aria-label="直播互动动态">
      <div className="liveBarrageLane" aria-hidden="true">
        {barrage.map((text, index) => <span key={`${text}-${index}`}>{text}</span>)}
      </div>
      <div className="liveJoinTicker" aria-hidden="true">
        {joiners.map((viewer, index) => (
          <span key={`${viewer.userId}-${index}`}>{viewer.nickname || viewer.userId} 进入直播间</span>
        ))}
      </div>
      <div className="liveGiftBurst" aria-hidden="true">
        <i />
        <span>{currentLot ? '商品热度上升' : '直播间热度上升'}</span>
      </div>
      {giftBurst ? (
        <div className="liveGiftCombo" key={giftBurst.id} aria-hidden="true">
          <span className="giftSender">{anchorName.slice(0, 1)}</span>
          <p><b>{anchorName}</b><small>送出 {giftBurst.name}</small></p>
          <i>{giftBurst.name.slice(0, 1)}</i>
          <strong>x1</strong>
        </div>
      ) : null}
      <div className="liveChatStream">
        {chatMessages.map((message, index) => (
          <p className={message.system ? 'system' : ''} key={message.id} style={{ '--live-chat-index': index } as CSSProperties}>
            <b>{message.name}</b>
            <span>{message.text}</span>
          </p>
        ))}
      </div>
    </section>
  );
}

function liveProductPrice(lot: NonNullable<LiveRoomController['currentLot']>) {
  return moneyNumber(lot.currentPrice) > 0 ? lot.currentPrice : lot.rule.startPrice;
}

function compactFloatPrice(value: Parameters<typeof formatMoney>[0]): string {
  return formatMoney(value).replace('元', '').replace(/\.00$/, '');
}

function LiveProductFloatCard({
  controller,
  onOpenDetail,
  onClose,
}: {
  controller: LiveRoomController;
  onOpenDetail: () => void;
  onClose: () => void;
}) {
  const { currentLot, room } = controller;
  if (!currentLot) return null;
  if (currentLotDisplayState(controller) !== 'live') return null;

  const price = liveProductPrice(currentLot);
  const leaderEntry = uniqueLeaderboardEntries(controller.ranking)[0];
  const leaderName = leaderEntry?.nickname || currentLot.leadingNickname || currentLot.leadingUserId || '';
  const leaderText = leaderName ? `${firstNameChar(leaderName)}**领先` : '等你领先';
  const leaderAvatarKey = leaderName || currentLot.id;
  const leaderAvatarUrl = leaderEntry?.avatarUrl || liveAvatarFor(`${room.roomId}:${leaderAvatarKey}:float-leader`);

  return (
    <aside className="liveProductFloatCard" aria-label="当前讲解商品">
      <button type="button" className="liveProductFloatClose" aria-label="关闭商品浮层" onClick={onClose}>
        <svg viewBox="0 0 18 18" aria-hidden="true">
          <path d="M14.75 4.46a.86.86 0 0 0-1.21-1.21L9 7.79 4.46 3.25a.86.86 0 0 0-1.21 1.21L7.79 9l-4.54 4.54a.86.86 0 0 0 1.21 1.21L9 10.21l4.54 4.54a.86.86 0 0 0 1.21-1.21L10.21 9l4.54-4.54Z" />
        </svg>
      </button>
      <button type="button" className="liveProductFloatBody" onClick={onOpenDetail}>
        <span className="liveProductFloatMedia">
          {currentLot.imageUrl ? <img src={currentLot.imageUrl} alt="" loading="lazy" /> : <b>拍</b>}
          <em>竞拍中</em>
        </span>
        <span className="liveProductFloatInfo">
          <span className="liveProductFloatPrice">
            <i>¥</i>
            <b>{compactFloatPrice(price)}</b>
          </span>
          <span className="liveProductFloatLeader">
            <span><AvatarMedia src={leaderAvatarUrl} name={leaderText} /></span>
            <b>{leaderText}</b>
          </span>
          <span className="liveProductFloatBidCta">去出价</span>
        </span>
      </button>
    </aside>
  );
}

function ComposerIcon({ name }: { name: 'product' | 'gift' | 'more' | 'heart' }) {
  if (name === 'product') {
    return (
      <svg className="composerCartIcon" viewBox="0 0 28 28" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
        <g filter="url(#composerCartFilter0)">
          <mask id="composerCartMask" maskUnits="userSpaceOnUse" x="3" y="6" width="21" height="13" style={{ maskType: 'alpha' }}>
            <path d="M5.056 6.223h.94c.73 0 2.009.325 2.654.844.212.17.461.322.734.322h12.414c.99 0 1.729.913 1.521 1.882l-1.535 7.163a2.333 2.333 0 0 1-2.281 1.844h-9.435a2.333 2.333 0 0 1-2.307-1.983L6.693 9.256c-.04-.354-.34-.7-.696-.7h-.941a1.167 1.167 0 1 1 0-2.333z" fill="url(#composerCartPaint0)" />
          </mask>
          <g mask="url(#composerCartMask)">
            <g filter="url(#composerCartFilter1)">
              <path d="M5.056 6.223h.94c.73 0 2.009.325 2.654.844.212.17.461.322.734.322h12.414c.99 0 1.729.913 1.521 1.882l-1.535 7.163a2.333 2.333 0 0 1-2.281 1.844h-9.435a2.333 2.333 0 0 1-2.307-1.983L6.693 9.256c-.04-.354-.34-.7-.696-.7h-.941a1.167 1.167 0 1 1 0-2.333z" fill="url(#composerCartPaint1)" />
            </g>
            <g filter="url(#composerCartFilter2)">
              <path d="m5.445 9.723 1.879 7.214a2.333 2.333 0 0 0 2.258 1.746H19.66a2.333 2.333 0 0 0 2.232-1.655l2.22-7.305" stroke="#FD5C01" strokeWidth="1.556" strokeLinejoin="round" />
            </g>
            <g opacity=".9" filter="url(#composerCartFilter3)">
              <path fillRule="evenodd" clipRule="evenodd" d="M10.306 11.279c0-.322.261-.584.584-.584h8.166a.583.583 0 0 1 0 1.167H10.89a.583.583 0 0 1-.583-.583z" fill="url(#composerCartPaint2)" />
            </g>
            <g opacity=".9" filter="url(#composerCartFilter4)">
              <path fillRule="evenodd" clipRule="evenodd" d="M10.695 14.388c0-.322.261-.583.583-.583h7.389a.583.583 0 1 1 0 1.166h-7.389a.583.583 0 0 1-.583-.583z" fill="url(#composerCartPaint3)" />
            </g>
          </g>
          <g filter="url(#composerCartFilter5)">
            <path fillRule="evenodd" clipRule="evenodd" d="M9.528 22.558a1.361 1.361 0 1 0 0-2.722 1.361 1.361 0 0 0 0 2.722z" fill="url(#composerCartPaint4)" />
          </g>
          <g filter="url(#composerCartFilter6)">
            <path fillRule="evenodd" clipRule="evenodd" d="M19.25 22.558a1.361 1.361 0 1 0 0-2.722 1.361 1.361 0 0 0 0 2.722z" fill="url(#composerCartPaint5)" />
          </g>
        </g>
        <defs>
          <filter id="composerCartFilter0" x="2.334" y="5.445" width="22.576" height="19.447" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha" />
            <feOffset dy=".778" />
            <feGaussianBlur stdDeviation=".778" />
            <feColorMatrix values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0.1 0" />
            <feBlend in2="BackgroundImageFix" result="effect1_dropShadow" />
            <feBlend in="SourceGraphic" in2="effect1_dropShadow" result="shape" />
          </filter>
          <filter id="composerCartFilter1" x="3.889" y="6.223" width="19.465" height="12.444" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feBlend in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha" />
            <feOffset dy=".389" />
            <feGaussianBlur stdDeviation=".778" />
            <feComposite in2="hardAlpha" operator="arithmetic" k2="-1" k3="1" />
            <feColorMatrix values="0 0 0 0 1 0 0 0 0 0.755343 0 0 0 0 0.229783 0 0 0 1 0" />
            <feBlend in2="shape" result="effect1_innerShadow" />
          </filter>
          <filter id="composerCartFilter2" x="1.581" y="6.385" width="26.386" height="16.187" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feBlend in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
            <feGaussianBlur stdDeviation="1.556" result="effect1_foregroundBlur" />
          </filter>
          <filter id="composerCartFilter3" x="9.528" y="10.306" width="10.889" height="2.724" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha" />
            <feOffset dy=".389" />
            <feGaussianBlur stdDeviation=".389" />
            <feColorMatrix values="0 0 0 0 0.758333 0 0 0 0 0.2275 0 0 0 0 0 0 0 0 0.296247 0" />
            <feBlend in2="BackgroundImageFix" result="effect1_dropShadow" />
            <feBlend in="SourceGraphic" in2="effect1_dropShadow" result="shape" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha2" />
            <feOffset dy="-.389" />
            <feGaussianBlur stdDeviation=".194" />
            <feComposite in2="hardAlpha2" operator="arithmetic" k2="-1" k3="1" />
            <feColorMatrix values="0 0 0 0 1 0 0 0 0 0.658586 0 0 0 0 0.42763 0 0 0 1 0" />
            <feBlend in2="shape" result="effect2_innerShadow" />
          </filter>
          <filter id="composerCartFilter4" x="9.917" y="13.416" width="10.111" height="2.724" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha" />
            <feOffset dy=".389" />
            <feGaussianBlur stdDeviation=".389" />
            <feColorMatrix values="0 0 0 0 0.758333 0 0 0 0 0.2275 0 0 0 0 0 0 0 0 0.296247 0" />
            <feBlend in2="BackgroundImageFix" result="effect1_dropShadow" />
            <feBlend in="SourceGraphic" in2="effect1_dropShadow" result="shape" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha2" />
            <feOffset dy="-.389" />
            <feGaussianBlur stdDeviation=".194" />
            <feComposite in2="hardAlpha2" operator="arithmetic" k2="-1" k3="1" />
            <feColorMatrix values="0 0 0 0 1 0 0 0 0 0.658586 0 0 0 0 0.42763 0 0 0 1 0" />
            <feBlend in2="shape" result="effect2_innerShadow" />
          </filter>
          <filter id="composerCartFilter5" x="8.167" y="19.836" width="2.722" height="2.878" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feBlend in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha" />
            <feOffset dy=".156" />
            <feGaussianBlur stdDeviation=".389" />
            <feComposite in2="hardAlpha" operator="arithmetic" k2="-1" k3="1" />
            <feColorMatrix values="0 0 0 0 1 0 0 0 0 0.755343 0 0 0 0 0.229783 0 0 0 1 0" />
            <feBlend in2="shape" result="effect1_innerShadow" />
          </filter>
          <filter id="composerCartFilter6" x="17.889" y="19.836" width="2.722" height="2.878" filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feFlood floodOpacity="0" result="BackgroundImageFix" />
            <feBlend in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
            <feColorMatrix in="SourceAlpha" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha" />
            <feOffset dy=".156" />
            <feGaussianBlur stdDeviation=".389" />
            <feComposite in2="hardAlpha" operator="arithmetic" k2="-1" k3="1" />
            <feColorMatrix values="0 0 0 0 1 0 0 0 0 0.755343 0 0 0 0 0.229783 0 0 0 1 0" />
            <feBlend in2="shape" result="effect1_innerShadow" />
          </filter>
          <linearGradient id="composerCartPaint0" x1="-3.841" y1="10.294" x2="6.267" y2="26.657" gradientUnits="userSpaceOnUse">
            <stop stopColor="#FFB100" />
            <stop offset="1" stopColor="#FC8901" />
          </linearGradient>
          <linearGradient id="composerCartPaint1" x1="-3.841" y1="10.294" x2="6.267" y2="26.657" gradientUnits="userSpaceOnUse">
            <stop stopColor="#FFB100" />
            <stop offset="1" stopColor="#FC8901" />
          </linearGradient>
          <linearGradient id="composerCartPaint2" x1="15.795" y1="12.277" x2="15.97" y2="10.61" gradientUnits="userSpaceOnUse">
            <stop stopColor="#FFF9E0" />
            <stop offset="1" stopColor="#F4E0B9" />
          </linearGradient>
          <linearGradient id="composerCartPaint3" x1="15.726" y1="15.387" x2="15.917" y2="13.723" gradientUnits="userSpaceOnUse">
            <stop stopColor="#FFF9E0" />
            <stop offset="1" stopColor="#F4E0B9" />
          </linearGradient>
          <linearGradient id="composerCartPaint4" x1="7.004" y1="20.944" x2="9.22" y2="23.666" gradientUnits="userSpaceOnUse">
            <stop stopColor="#FFB100" />
            <stop offset="1" stopColor="#FC8901" />
          </linearGradient>
          <linearGradient id="composerCartPaint5" x1="16.727" y1="20.944" x2="18.943" y2="23.666" gradientUnits="userSpaceOnUse">
            <stop stopColor="#FFB100" />
            <stop offset="1" stopColor="#FC8901" />
          </linearGradient>
        </defs>
      </svg>
    );
  }
  if (name === 'gift') {
    return <img className="composerImageIcon composerGiftIcon" src="/douyin-assets/gifts/gift-icon.svg" alt="" aria-hidden="true" loading="lazy" />;
  }
  if (name === 'more') {
    return <img className="composerImageIcon composerMoreIcon" src="/douyin-assets/icons/share-white-full.png" alt="" aria-hidden="true" loading="lazy" />;
  }
  return <img className="composerImageIcon composerHeartIcon" src="/douyin-assets/gifts/xiao-xin-xin.png" alt="" aria-hidden="true" loading="lazy" />;
}

function LiveComposer({
  controller,
  onOpenComments,
  onOpenAuction,
  onOpenGift,
  onOpenMore,
}: {
  controller: LiveRoomController;
  onOpenComments: () => void;
  onOpenAuction: () => void;
  onOpenGift: () => void;
  onOpenMore: () => void;
}) {
  const sendFacadeNotice = (message: string) => controller.actions.showNotice(message);

  return (
    <footer className="liveComposer" aria-label="直播互动输入">
      <button type="button" className="commentInput" onClick={onOpenComments}>说点什么...</button>
      <button type="button" className="composerIconButton" aria-label="商品橱窗" onClick={onOpenAuction}>
        <ComposerIcon name="product" />
      </button>
      <button type="button" className="composerIconButton" aria-label="点赞直播" onClick={() => sendFacadeNotice('已为直播间增加热度')}>
        <ComposerIcon name="heart" />
      </button>
      <button type="button" className="composerIconButton" aria-label="礼物" onClick={onOpenGift}>
        <ComposerIcon name="gift" />
      </button>
      <button type="button" className="composerIconButton" aria-label="更多操作" onClick={onOpenMore}>
        <ComposerIcon name="more" />
      </button>
    </footer>
  );
}

function commentsForRoom(controller: LiveRoomController) {
  const bidComments = controller.room.recentBids.slice(0, 8).map((bid, index) => ({
    id: bid.id || `${bid.userId}-${index}`,
    avatar: (bid.nickname || bid.userId || '拍').slice(0, 1),
    name: bid.nickname || `拍友${bid.userId.slice(-4)}`,
    text: bid.accepted === false
      ? businessErrorMessage(bid.rejectReason, { lot: controller.currentLot || undefined }) || '刚刚互动未成功，准备再试一次'
      : `关注了这件商品，挺有意思`,
    meta: index < 2 ? '刚刚' : `${index + 1}分钟前`,
    count: Math.max(3, 168 - index * 17),
  }));

  if (bidComments.length) return bidComments;

  return [
    {
      id: 'anchor-welcome',
      avatar: controller.anchorName.slice(0, 1),
      name: controller.anchorName,
      text: controller.currentLot ? `正在看 ${controller.currentLot.title}，喜欢可以点右侧商品` : '欢迎来到直播间，商品上架后会在底部出现',
      meta: '置顶',
      count: 256,
    },
    { id: 'buyer-1', avatar: '拍', name: '拍友7281', text: '这场节奏可以，等开拍', meta: '1分钟前', count: 88 },
    { id: 'buyer-2', avatar: '藏', name: '收藏新手', text: '能看到商品细节图吗', meta: '2分钟前', count: 42 },
  ];
}

function DouyinCommentsSheet({ controller, onClose }: { controller: LiveRoomController; onClose: () => void }) {
  const [likedIds, setLikedIds] = useState<Set<string>>(new Set());
  const comments = commentsForRoom(controller);
  const totalCount = comments.length + Math.max(controller.room.recentBids.length, 128);
  const toggleLike = (id: string) => {
    setLikedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  return (
    <div className="dyCommentOverlay" onClick={onClose}>
      <section className="dyCommentSheet" role="dialog" aria-modal="true" aria-label="直播评论" onClick={(e) => e.stopPropagation()}>
        <header className="dyCommentHeader">
          <div className="dyCommentHeaderLeft">
            <button type="button" className="dyCommentCloseBtn" onClick={onClose} aria-label="关闭评论">‹</button>
            <span className="dyCommentTitle">{shortCount(totalCount)}条评论</span>
          </div>
          <div className="dyCommentHeaderRight">
            <button type="button" aria-label="全屏">⤢</button>
            <button type="button" onClick={onClose} aria-label="关闭">✕</button>
          </div>
        </header>

        <div className="dyCommentList">
          {comments.map((comment) => (
            <div className="dyCommentItem" key={comment.id}>
              <div className="dyCommentAvatar">
                <span className="dyCommentAvatarPlaceholder">{comment.avatar}</span>
              </div>
              <div className="dyCommentBody">
                <div className="dyCommentName">{comment.name}</div>
                <p className="dyCommentText">{comment.text}</p>
                <div className="dyCommentMeta">
                  <span className="dyCommentTime">{comment.meta}</span>
                  <button type="button" className="dyCommentReply">回复</button>
                </div>
              </div>
              <div className="dyCommentActions">
                <button
                  type="button"
                  className={`dyCommentLike${likedIds.has(comment.id) ? ' active' : ''}`}
                  onClick={() => toggleLike(comment.id)}
                  aria-label="点赞评论"
                >
                  <svg viewBox="0 0 48 48"><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="4" d="M15 8C8.925 8 4 12.925 4 19c0 11 13 21 20 23.326C31 40 44 30 44 19c0-6.075-4.925-11-11-11c-3.72 0-7.01 1.847-9 4.674A10.99 10.99 0 0 0 15 8" /></svg>
                  <span>{shortCount(comment.count + (likedIds.has(comment.id) ? 1 : 0))}</span>
                </button>
              </div>
            </div>
          ))}
          <div className="dyCommentNoMore">暂时没有更多了</div>
        </div>

        <div className="dyCommentToolbar">
          <input className="dyCommentInput" placeholder="善语结善缘，恶言伤人心" readOnly />
          <button type="button" className="dyCommentToolBtn" aria-label="表情">😊</button>
          <button type="button" className="dyCommentToolBtn" aria-label="@提及">@</button>
        </div>
      </section>
    </div>
  );
}

function DouyinShareSheet({ controller, onClose }: { controller: LiveRoomController; onClose: () => void }) {
  const copyShareLink = () => {
    void navigator.clipboard?.writeText(window.location.href).catch(() => undefined);
    controller.actions.showNotice('直播间链接已复制');
    onClose();
  };

  return (
    <div className="douyinSheetMask dark" onClick={onClose}>
      <section className="douyinBottomSheet shareSheet" role="dialog" aria-modal="true" aria-label="分享直播间" onClick={(event) => event.stopPropagation()}>
        <header className="shareHeader">
          <b>分享给朋友</b>
          <button type="button" className="sheetClose" onClick={onClose} aria-label="关闭分享">×</button>
        </header>
        <div className="shareFriendRow">
          {SHARE_FRIENDS.map((friend) => (
            <button type="button" className="shareFriend" key={friend}>
              <span>{friend.slice(0, 1)}</span>
              <b>{friend}</b>
            </button>
          ))}
          <button type="button" className="shareFriend moreFriend">
            <span>›</span>
            <b>更多朋友</b>
          </button>
        </div>
        <div className="shareOptionRow">
          {SHARE_OPTIONS.map((option) => (
            <button type="button" className="shareOption" key={option} onClick={option === '复制链接' ? copyShareLink : undefined}>
              <span>{option.slice(0, 1)}</span>
              <b>{option}</b>
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}

function DouyinGiftSheet({
  controller,
  onClose,
  onSendGift,
}: {
  controller: LiveRoomController;
  onClose: () => void;
  onSendGift: (name: string) => void;
}) {
  const sendGift = (name: string) => {
    onSendGift(name);
    onClose();
  };

  return (
    <div className="douyinSheetMask dark" onClick={onClose}>
      <section className="douyinBottomSheet giftSheet" role="dialog" aria-modal="true" aria-label="礼物" onClick={(event) => event.stopPropagation()}>
        <header className="shareHeader">
          <b>礼物</b>
          <button type="button" className="sheetClose" onClick={onClose} aria-label="关闭礼物">×</button>
        </header>
        <div className="giftBalance">
          <span>抖币余额 0</span>
          <button type="button" onClick={() => controller.actions.showNotice('充值功能暂未开放')}>充值</button>
        </div>
        <div className="giftGrid">
          {GIFT_OPTIONS.map((gift) => (
            <button type="button" onClick={() => sendGift(gift.name)} key={gift.name}>
              <span><img src={gift.image} alt="" loading="lazy" /></span>
              <b>{gift.name}</b>
              <small>{gift.value} 抖币</small>
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}

function DouyinMoreSheet({
  controller,
  onClose,
  onOpenAuction,
  onOpenShare,
  onToggleClear,
}: {
  controller: LiveRoomController;
  onClose: () => void;
  onOpenAuction: () => void;
  onOpenShare: () => void;
  onToggleClear: () => void;
}) {
  const runAction = (action: string) => {
    if (action === '清屏') {
      onClose();
      onToggleClear();
      return;
    }
    if (action === '订单') {
      onClose();
      window.location.assign('/shop/orders?from=live-more');
      return;
    }
    if (action === '直播公告') {
      controller.actions.showNotice(controller.currentLot ? `正在竞拍 ${controller.currentLot.title}` : '欢迎来到直播间');
      onClose();
      return;
    }
    controller.actions.showNotice(`${action}暂未开放`);
    onClose();
  };

  return (
    <div className="douyinSheetMask dark" onClick={onClose}>
      <section className="douyinBottomSheet moreSheet" role="dialog" aria-modal="true" aria-label="更多操作" onClick={(event) => event.stopPropagation()}>
        <header className="shareHeader">
          <b>更多</b>
          <button type="button" className="sheetClose" onClick={onClose} aria-label="关闭更多">×</button>
        </header>
        <div className="moreActionGrid">
          <button type="button" onClick={() => { onClose(); onOpenAuction(); }}>
            <span>商</span>
            <b>商品橱窗</b>
          </button>
          <button type="button" onClick={() => { onClose(); onOpenShare(); }}>
            <span>享</span>
            <b>分享直播</b>
          </button>
          {MORE_ACTIONS.map((action) => (
            <button type="button" onClick={() => runAction(action)} key={action}>
              <span>{action.slice(0, 1)}</span>
              <b>{action}</b>
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}

function LiveRoomSwitchPreview({
  room,
  direction,
}: {
  room?: RoomSwitchPreview;
  direction: 'previous' | 'next';
}) {
  const roomName = room?.name || '直播间';
  const previewSource = livePreviewSourceFor(`${room?.id || roomName}:${direction}`);

  return (
    <section className={`liveRoomSwipePage liveRoomSwitchPreview ${direction}`} aria-hidden="true">
      <div className="liveRoomPreviewVideo">
        <video className="liveRoomPreviewMedia" src={previewSource} autoPlay muted loop playsInline preload="metadata" />
      </div>
      <div className="liveRoomPreviewTop">
        <span className="liveRoomPreviewAvatar">
          <AvatarMedia src={liveAvatarFor(`${room?.id || roomName}:preview`)} name={roomName} />
        </span>
        <span>
          <b>{roomName}</b>
          <small>直播中</small>
        </span>
      </div>
      <div className="liveRoomPreviewCenter">
        <span>LIVE</span>
        <strong>{roomName}</strong>
      </div>
    </section>
  );
}

type LiveRoomViewProps = {
  controller: LiveRoomController;
  hasRoomSwipeTargets?: boolean;
  previousRoom?: RoomSwitchPreview;
  nextRoom?: RoomSwitchPreview;
  onSwipeRoom?: (direction: RoomSwipeDirection) => void;
  onCloseRoom?: () => void;
};

export function LiveRoomView(props: LiveRoomViewProps) {
  return <LiveRoomViewContent key={props.controller.roomId} {...props} />;
}

function LiveRoomViewContent({
  controller,
  hasRoomSwipeTargets = false,
  previousRoom,
  nextRoom,
  onSwipeRoom,
  onCloseRoom,
}: LiveRoomViewProps) {
  const [activeSheet, setActiveSheet] = useState<'comments' | 'share' | 'gift' | 'more' | null>(null);
  const [clearScreen, setClearScreen] = useState(false);
  const [giftBurst, setGiftBurst] = useState<GiftBurst | null>(null);
  const [leaderboardCollapsed, setLeaderboardCollapsed] = useState(false);
  const [closedProductCardLotId, setClosedProductCardLotId] = useState<string | null>(null);
  const [detailLotId, setDetailLotId] = useState<string | null>(null);
  const [handledOverlayResetKey, setHandledOverlayResetKey] = useState('');
  const roomSwipeStart = useRef<{ x: number; y: number; at: number } | null>(null);
  const roomWheelGesture = useRef({ x: 0, y: 0, lastAt: 0, lockedUntil: 0 });
  const roomSwipeSettleDirection = useRef<RoomSwipeDirection | 0>(0);
  const roomSwipeSettleTimer = useRef<number | null>(null);
  const [roomSwipeOffset, setRoomSwipeOffset] = useState(0);
  const [roomSwipePhase, setRoomSwipePhase] = useState<RoomSwipePhase>('idle');
  const {
    room,
    error,
    roomName,
    anchorName,
    currentLot,
    meId,
    wsState,
    notices,
    auctionPanel,
    resultLot,
    visibleResultOrder,
    payOrder,
    depositPrompt,
    actions,
  } = controller;
  const overlayResetKey = resultLot?.id
    ? `result:${resultLot.id}`
    : depositPrompt?.lot.id
      ? `deposit:${depositPrompt.lot.id}`
      : '';
  if (overlayResetKey !== handledOverlayResetKey) {
    setHandledOverlayResetKey(overlayResetKey);
    if (overlayResetKey) {
      setActiveSheet(null);
      setClearScreen(false);
      setDetailLotId(null);
    }
  }
  const openCurrentAuction = () => actions.openAuctionPanel('current');
  const liveSource = room.snapshot?.liveSourceUrl || livePreviewSourceFor(room.roomId || roomName);
  const openLotDetail = (lot: Lot) => {
    setActiveSheet(null);
    setClearScreen(false);
    actions.closeAuctionPanel();
    setDetailLotId(lot.id);
    void actions.refreshRoomLots().catch(() => undefined);
  };
  const openCurrentProductDetail = () => {
    if (!currentLot) {
      actions.showNotice('当前暂无商品，等待主播上架');
      return;
    }
    openLotDetail(currentLot);
  };
  const closeActiveSheet = () => setActiveSheet(null);
  const sendGift = (name: string) => {
    setGiftBurst({ id: Date.now(), name });
    actions.showNotice(`已送出 ${name}`);
    window.setTimeout(() => setGiftBurst(null), 2300);
  };
  const hasResultModal = Boolean(resultLot);
  const detailLotCandidates = [currentLot, ...auctionPanel.lots].filter((lot): lot is Lot => Boolean(lot));
  const detailLot = detailLotId ? detailLotCandidates.find((lot) => lot.id === detailLotId) || null : null;
  const detailVisible = Boolean(detailLot);
  const roomSwipeLocked = Boolean(activeSheet || auctionPanel.open || resultLot || payOrder || depositPrompt || detailVisible);

  useEffect(() => () => {
    if (roomSwipeSettleTimer.current) window.clearTimeout(roomSwipeSettleTimer.current);
  }, []);

  function finishRoomSwipeSettle() {
    if (roomSwipeSettleTimer.current) window.clearTimeout(roomSwipeSettleTimer.current);
    roomSwipeSettleTimer.current = null;
    const direction = roomSwipeSettleDirection.current;
    roomSwipeSettleDirection.current = 0;
    if (direction && onSwipeRoom) {
      onSwipeRoom(direction);
      window.setTimeout(() => {
        setRoomSwipeOffset(0);
        setRoomSwipePhase('idle');
      }, 80);
      return;
    }
    setRoomSwipeOffset(0);
    setRoomSwipePhase('idle');
  }

  function settleRoomSwipe(direction: RoomSwipeDirection | 0) {
    if (roomSwipeSettleTimer.current) window.clearTimeout(roomSwipeSettleTimer.current);
    if (!direction && Math.abs(roomSwipeOffset) < 0.5) {
      roomSwipeSettleTimer.current = null;
      roomSwipeSettleDirection.current = 0;
      setRoomSwipeOffset(0);
      setRoomSwipePhase('idle');
      return;
    }
    roomSwipeSettleDirection.current = direction;
    setRoomSwipePhase('settling');
    setRoomSwipeOffset(direction === 1 ? -liveRoomViewportHeight() : direction === -1 ? liveRoomViewportHeight() : 0);
    roomSwipeSettleTimer.current = window.setTimeout(finishRoomSwipeSettle, LIVE_ROOM_SETTLE_MS + 80);
  }

  function handleRoomTouchStart(event: TouchEvent<HTMLElement>) {
    if (!hasRoomSwipeTargets || roomSwipeLocked || roomSwipePhase === 'settling' || shouldIgnoreRoomSwipe(event.target)) {
      roomSwipeStart.current = null;
      return;
    }

    const touch = event.touches[0];
    if (!touch) return;
    roomSwipeStart.current = { x: touch.clientX, y: touch.clientY, at: Date.now() };
    roomSwipeSettleDirection.current = 0;
    setRoomSwipePhase('dragging');
    setRoomSwipeOffset(0);
  }

  function handleRoomTouchMove(event: TouchEvent<HTMLElement>) {
    const start = roomSwipeStart.current;
    if (!start || roomSwipeLocked || shouldIgnoreRoomSwipe(event.target)) return;

    const touch = event.touches[0];
    if (!touch) return;
    const dx = touch.clientX - start.x;
    const dy = touch.clientY - start.y;
    if (Math.abs(dy) < 6 && Math.abs(dx) < 6) return;
    if (Math.abs(dy) <= Math.abs(dx) * 1.04) return;

    event.preventDefault();
    setRoomSwipeOffset(rubberBandRoomSwipeOffset(dy));
  }

  function handleRoomTouchEnd(event: TouchEvent<HTMLElement>) {
    const start = roomSwipeStart.current;
    roomSwipeStart.current = null;
    if (!start) return;
    if (!hasRoomSwipeTargets || !onSwipeRoom || roomSwipeLocked || shouldIgnoreRoomSwipe(event.target)) {
      settleRoomSwipe(0);
      return;
    }

    const touch = event.changedTouches[0];
    const dx = (touch?.clientX ?? start.x) - start.x;
    const dy = (touch?.clientY ?? start.y) - start.y;
    const elapsed = Date.now() - start.at;
    if (!roomSwipeDistance(dy, elapsed) || Math.abs(dy) <= Math.abs(dx) * 1.12) {
      settleRoomSwipe(0);
      return;
    }

    event.preventDefault();
    settleRoomSwipe(dy < 0 ? 1 : -1);
  }

  function handleRoomWheel(event: WheelEvent<HTMLElement>) {
    if (!hasRoomSwipeTargets || !onSwipeRoom || roomSwipeLocked || roomSwipePhase === 'settling' || shouldIgnoreRoomSwipe(event.target)) return;

    const now = Date.now();
    const gesture = roomWheelGesture.current;
    if (now < gesture.lockedUntil) return;
    if (now - gesture.lastAt > LIVE_ROOM_WHEEL_STALE_MS) {
      gesture.x = 0;
      gesture.y = 0;
    }

    gesture.x += event.deltaX;
    gesture.y += event.deltaY;
    gesture.lastAt = now;

    const absX = Math.abs(gesture.x);
    const absY = Math.abs(gesture.y);
    if (Math.max(absX, absY) < LIVE_ROOM_WHEEL_SWIPE_DISTANCE) return;

    if (absY > absX * 1.08) {
      event.preventDefault();
      settleRoomSwipe(gesture.y > 0 ? 1 : -1);
      roomWheelGesture.current = { x: 0, y: 0, lastAt: now, lockedUntil: now + LIVE_ROOM_WHEEL_LOCK_MS };
      return;
    }

    roomWheelGesture.current = { x: 0, y: 0, lastAt: now, lockedUntil: now + 160 };
  }

  const swipeStageClassName = [
    'liveRoomSwipeStage',
    roomSwipePhase === 'settling' ? 'settling' : '',
    roomSwipePhase === 'dragging' ? 'dragging' : '',
  ].filter(Boolean).join(' ');
  const swipeStageStyle = {
    transform: `translate3d(0, calc(-100dvh + ${roomSwipeOffset}px), 0)`,
  } as CSSProperties;

  return (
    <main
      className={`mobileShell douyinShell liveRoomSwipeShell ${auctionPanel.open && !hasResultModal ? 'drawerVisible' : ''} ${clearScreen ? 'isClearScreen' : ''} ${detailVisible ? 'productDetailVisible' : ''}`}
      onTouchStart={handleRoomTouchStart}
      onTouchMove={handleRoomTouchMove}
      onTouchEnd={handleRoomTouchEnd}
      onTouchCancel={() => {
        roomSwipeStart.current = null;
        if (roomSwipePhase === 'dragging') settleRoomSwipe(0);
      }}
      onWheel={handleRoomWheel}
    >
      <div
        className={swipeStageClassName}
        style={swipeStageStyle}
        onTransitionEnd={(event) => {
          if (event.currentTarget === event.target && roomSwipePhase === 'settling') finishRoomSwipeSettle();
        }}
      >
        <LiveRoomSwitchPreview room={previousRoom} direction="previous" />

        <section className={`mobileShell douyinShell liveRoomSwipePage liveRoomSwipeCurrent ${auctionPanel.open && !hasResultModal ? 'drawerVisible' : ''} ${clearScreen ? 'isClearScreen' : ''} ${detailVisible ? 'productDetailVisible' : ''}`}>
          <LivePlayer
            poster={currentLot?.imageUrl}
            anchorName={anchorName}
            onlineCount={room.snapshot?.onlineCount}
            wsState={wsState}
            roomName={roomName}
            source={liveSource}
            liveStartedAtUnixMs={room.snapshot?.liveStartedAtUnixMs}
            serverTimeUnixMs={room.serverTimeUnixMs}
            serverTimeReceivedAtUnixMs={room.serverTimeReceivedAtUnixMs}
          />

          {clearScreen ? (
            <button type="button" className="exitClearScreen" onClick={() => setClearScreen(false)}>退出清屏</button>
          ) : null}
          <LiveRoomChrome controller={controller} onCloseRoom={onCloseRoom} />
          <LiveRoomEffectsLayer controller={controller} giftBurst={giftBurst} />
          <LiveBidLeaderboard
            controller={controller}
            onOpenAuction={openCurrentAuction}
            collapsed={leaderboardCollapsed}
            onToggleCollapsed={() => setLeaderboardCollapsed((value) => !value)}
          />
          {currentLot && closedProductCardLotId !== currentLot.id ? (
            <LiveProductFloatCard
              controller={controller}
              onOpenDetail={openCurrentProductDetail}
              onClose={() => setClosedProductCardLotId(currentLot.id)}
            />
          ) : null}
          {wsState === '已断开' ? <div className="liveConnectionWarn">实时连接中断，正在恢复</div> : null}
          {error ? <div className="liveConnectionWarn error">{error}</div> : null}
          {!hasResultModal ? <AuctionDrawer controller={controller} onOpenLotDetail={openLotDetail} /> : null}
          <AuctionNoticeLayer notices={notices} />
          <LiveComposer
            controller={controller}
            onOpenComments={() => setActiveSheet('comments')}
            onOpenAuction={openCurrentAuction}
            onOpenGift={() => setActiveSheet('gift')}
            onOpenMore={() => setActiveSheet('more')}
          />
          {activeSheet === 'comments' ? <DouyinCommentsSheet controller={controller} onClose={closeActiveSheet} /> : null}
          {activeSheet === 'share' ? <DouyinShareSheet controller={controller} onClose={closeActiveSheet} /> : null}
          {activeSheet === 'gift' ? <DouyinGiftSheet controller={controller} onClose={closeActiveSheet} onSendGift={sendGift} /> : null}
          {activeSheet === 'more' ? (
            <DouyinMoreSheet
              controller={controller}
              onClose={closeActiveSheet}
              onOpenAuction={openCurrentAuction}
              onOpenShare={() => setActiveSheet('share')}
              onToggleClear={() => setClearScreen(true)}
            />
          ) : null}
          {resultLot ? (
            <ResultModal
              lot={resultLot}
              meId={meId}
              order={visibleResultOrder}
              onClose={actions.closeResult}
              onNext={actions.nextLot}
              onPay={actions.setPayOrder}
            />
          ) : null}

          {payOrder ? (
            <MockPayModal
              order={payOrder}
              onStartPayment={actions.markPaymentStarted}
              onPaid={actions.handlePaymentPaid}
              onAuthRequired={actions.requireBuyerAuth}
              onClose={() => actions.setPayOrder(null)}
            />
          ) : null}
        </section>

        <LiveRoomSwitchPreview room={nextRoom} direction="next" />
      </div>
      {detailLot ? (
        <LiveProductDetailOverlay
          controller={controller}
          lot={detailLot}
          liveSource={liveSource}
          onClose={() => {
            setDetailLotId(null);
            if (controller.bidAuthPanelOpen) actions.closeBuyerAuthPanel();
            actions.closeAuctionPanel();
          }}
          onSelectLot={openLotDetail}
        />
      ) : null}
      {depositPrompt ? (
        <DepositPayModal
          lot={depositPrompt.lot}
          onConfirm={actions.confirmDepositPayment}
          onAuthRequired={actions.requireBuyerAuth}
          onClose={actions.closeDepositPrompt}
        />
      ) : null}
    </main>
  );
}
