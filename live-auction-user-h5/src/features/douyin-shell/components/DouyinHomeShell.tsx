import { useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent, type ReactNode, type TouchEvent, type WheelEvent } from 'react';
import { listPublicRooms } from '../../auction/api/auctionApi';
import { resolveLivePlaylist } from '../../live/hooks/useLivePlayer';
import type { Room } from '../../../shared/api/types';
import { navigateTo, SPA_NAVIGATE_EVENT } from '../../../shared/navigation';
import { DouyinCommentSheet } from '../../../shared/ui/DouyinCommentSheet';
import { DouyinTabBar, type DouyinTab } from '../../../shared/ui/DouyinTabBar';
import { DouyinLoading } from '../../../shared/ui/DouyinLoading';
import { UserPanel } from '../../profile/components/UserPanel';
import type { CommentTarget, DouyinVideoRecord, FeedItem, HomeSheet } from '../../feed/model/feedTypes';
import {
  DOUYIN_LIVE_CHANNEL_INDEX,
  liveRoomIdFromHref,
  readHomeReturnState,
  writeHomeReturnState,
} from '../model/homeReturnState';

const LIVE_CHANNEL_INDEX = DOUYIN_LIVE_CHANNEL_INDEX;
const CHANNELS = ['热点', '长视频', '关注', '直播', '推荐'];
const TOP_NAV_CHANNELS = ['热点', '长视频', '关注', '直播', '商城', '推荐'];
const DOUYIN_VIDEO_SOURCE = '/data/douyin-feed.json';
const DOUYIN_IMAGE_BASE = 'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/images/';
const DATA_FETCH_OPTIONS: RequestInit = { cache: 'no-store' };
const DOUYIN_COMMENT_VIDEO_IDS = [
  '6686589698707590411',
  '6826943630775831812',
  '6882368275695586568',
  '6923214072347512068',
  '7000587983069957383',
  '7005490661592026405',
  '7110263965858549003',
  '7128686458763889956',
  '7161000281575148800',
  '7194815099381484860',
  '7260749400622894336',
  '7267478481213181238',
  '7270431418822446370',
  '7293100687989148943',
  '7295697246132227343',
  '7321200290739326262',
];
const SIDE_RECENTS = ['青柠汽水', '山海收藏家', '奶油小熊', '林间晚风', 'LiveAuction', 'Yexieer'];
const SIDE_FEATURES = ['我的钱包', '券包', '小程序', '观看历史', '内容偏好', '离线模式', '设置', '稍后再看'];
const SHARE_OPTIONS = ['转发', '私信朋友', '微信', '朋友圈', 'QQ', '复制链接', '举报', '不感兴趣'];

const FEED_ITEMS: FeedItem[] = [
  {
    id: 'feed-hot-1',
    awemeId: '7260749400622894336',
    kind: 'video',
    author: '我是香秀',
    title: '你说爱像云 要自在漂浮才美丽',
    music: '热门原声 · 我是香秀',
    location: '推荐',
    likes: '12.8w',
    comments: '2.2w',
    collects: '2.4w',
    shares: '1.2w',
    tone: 'rose',
    coverUrl: `${DOUYIN_IMAGE_BASE}jwWCPZVTIA4IKM-8WipLF.png`,
  },
  {
    id: 'feed-live-1',
    kind: 'live',
    author: '严选直播间',
    title: '今晚 8 点严选好物开箱，先看细节再进直播间',
    music: '直播中 · 严选专场',
    location: '深圳',
    likes: '8.6w',
    comments: '1.1w',
    collects: '9.8k',
    shares: '3.2k',
    tone: 'cyan',
    liveLabel: 'LIVE · 999w人气',
    liveHref: '/home/live',
  },
  {
    id: 'feed-hot-2',
    awemeId: '6686589698707590411',
    kind: 'video',
    author: '周子然JingYi',
    title: '门有点矮哟～',
    music: '热门原声 · 周子然JingYi',
    location: '推荐',
    likes: '9.7w',
    comments: '1.9w',
    collects: '1.8w',
    shares: '888',
    tone: 'amber',
    coverUrl: `${DOUYIN_IMAGE_BASE}_T0vQPZKXrNC6ulECmMes.png`,
  },
  {
    id: 'feed-hot-3',
    awemeId: '6826943630775831812',
    kind: 'video',
    author: '拍场观察员',
    title: '今天的严选好物先看一眼，近景、材质、成交节奏都刷到',
    music: 'LiveAuction 原声 · 备拍现场',
    likes: '9.2w',
    comments: '463',
    collects: '1.1w',
    shares: '1392',
    tone: 'violet',
  },
  {
    id: 'feed-live-2',
    kind: 'live',
    author: '广州表哥',
    title: '同城直播开场，边聊边看今日热门好物',
    music: '直播中 · 同城好物',
    location: '广州',
    likes: '6.1w',
    comments: '824',
    collects: '6400',
    shares: '2.1k',
    tone: 'dark',
    liveLabel: 'LIVE · 红包',
    liveHref: '/home/live',
  },
];

const VIDEO_FEED_ITEMS = FEED_ITEMS.filter((item) => item.kind === 'video');
const HORIZONTAL_SWIPE_DISTANCE = 44;
const VERTICAL_SWIPE_DISTANCE = 44;
const QUICK_SWIPE_MS = 280;
const WHEEL_SWIPE_DISTANCE = 72;
const WHEEL_LOCK_MS = 330;
const WHEEL_STALE_MS = 180;
const CHANNEL_VIDEO_WINDOW = 32;
const FEED_TONES: FeedItem['tone'][] = ['rose', 'cyan', 'amber', 'violet', 'dark'];
const DY_HEART_PATH = 'M453.036 88.712C493.774 30.664 560.66 0 634.251 0C774.403 0 890.972 121.59 890.972 266.137C890.972 266.155 890.972 266.173 890.972 266.191C890.981 266.171 890.991 266.151 891 266.131C891 270.252 890.93 273.372 890.878 275.668C890.806 278.819 890.77 280.416 891 280.916C890.469 311.319 885.222 336.438 875.899 369.628C870.609 375.588 865.694 386.812 860.798 399.199C853.074 411.197 850.151 417.009 845.697 428.77C841.152 436.399 836.324 444.063 831.245 451.744C793.796 508.544 743.708 565.074 693.667 615.252C615.336 694.277 535.544 760.058 500.832 788.675C491.247 796.576 485.1 801.644 483.368 803.375C471.067 815.678 458.765 815.986 446.463 815.994C446.139 815.998 445.814 816 445.486 816C420.25 816 407.632 803.381 395.014 790.763C394.051 789.8 391.601 787.779 387.858 784.783C349.625 756.6 263.586 687.786 182.742 604.783C121.066 542.02 61.622 470.007 29.092 399.588C16.474 374.351 0.731 314.264 0 280.922C0.269 280.655 0.227 279.049 0.144 275.844C0.083 273.498 0 270.297 0 266.137C0 121.524 116.502 0 256.721 0C330.179 0 397.131 30.664 453.036 88.712z';

function clampIndex(value: number, length: number) {
  return Math.max(0, Math.min(length - 1, value));
}

function wrapIndex(value: number, length: number) {
  if (length <= 0) return 0;
  return ((value % length) + length) % length;
}

function isMeEntryPath() {
  return location.pathname === '/me' || location.pathname.startsWith('/m/profile') || new URLSearchParams(location.search).get('tab') === 'me';
}

function isLiveEntryPath() {
  return location.pathname === '/home/live';
}

function initialBaseIndex() {
  return isMeEntryPath() ? 2 : 1;
}

function initialChannelIndex() {
  return isLiveEntryPath() ? LIVE_CHANNEL_INDEX : 4;
}

function shuffleItems<T>(items: T[]) {
  const shuffled = items.slice();
  for (let index = shuffled.length - 1; index > 0; index -= 1) {
    const randomIndex = Math.floor(Math.random() * (index + 1));
    [shuffled[index], shuffled[randomIndex]] = [shuffled[randomIndex], shuffled[index]];
  }
  return shuffled;
}

function swipeDistance(distance: number, elapsed: number, baseDistance: number) {
  return Math.abs(distance) > (elapsed < QUICK_SWIPE_MS ? baseDistance * 0.72 : baseDistance);
}

function shouldLetWheelScroll(target: EventTarget | null) {
  if (!(target instanceof Element)) return false;
  return Boolean(target.closest([
    '.dyHomeReplicaSheet',
    '.dyCommentSheet',
    '.dyBottomSheet',
    '.dyHomeReplicaSidebar',
    '.dyHomeReplicaUserPanel',
    'input',
    'textarea',
    'select',
  ].join(',')));
}

function normalizeRemoteAsset(url?: string) {
  if (!url) return '';
  if (/^https?:\/\//i.test(url)) return url;
  if (url.startsWith('/douyin-assets/') || url.startsWith('/data/')) return url;
  if (url.startsWith('/images/')) return `${DOUYIN_IMAGE_BASE}${url.slice('/images/'.length)}`;
  if (url.startsWith('/')) return url;
  return `${DOUYIN_IMAGE_BASE}${url}`;
}

function normalizeRemoteVideoUrl(url?: string) {
  const assetUrl = normalizeRemoteAsset(url);
  if (!assetUrl) return '';
  if (import.meta.env.DEV && /^https:\/\/www\.douyin\.com\/aweme\/v1\/play\//.test(assetUrl)) {
    return `/__douyin_video_proxy?url=${encodeURIComponent(assetUrl)}`;
  }
  return assetUrl;
}

function firstUrl(urlList?: string[]) {
  return Array.isArray(urlList) ? urlList.find(Boolean) || '' : '';
}

function authorAvatarUrl(video: DouyinVideoRecord) {
  return normalizeRemoteAsset(
    firstUrl(video.author?.avatar_300x300?.url_list)
      || firstUrl(video.author?.avatar_168x168?.url_list)
      || firstUrl(video.author?.avatar_thumb?.url_list)
      || firstUrl(video.author?.cover_url?.[0]?.url_list)
      || firstUrl(video.video?.cover?.url_list),
  );
}

function isVideoPlaybackUrl(url: string) {
  return !/\.mp3(?:$|\?)|\/ies-music\//i.test(url);
}

function formatRemoteCount(value?: number, fallback = '0') {
  if (typeof value !== 'number' || !Number.isFinite(value)) return fallback;
  if (value >= 100000000) return `${(value / 100000000).toFixed(value >= 1000000000 ? 0 : 1)}亿`;
  if (value >= 10000) return `${(value / 10000).toFixed(value >= 100000 ? 0 : 1)}w`;
  return value.toLocaleString('zh-CN');
}

function publisherHref(item: FeedItem) {
  return item.publisherHref || `/user?awemeId=${encodeURIComponent(item.awemeId || item.id)}`;
}

function chooseVideoFitFromSize(
  mediaWidth?: number,
  mediaHeight?: number,
  frameWidth = window.innerWidth,
  frameHeight = window.innerHeight - 58,
): 'cover' | 'contain' {
  if (!mediaWidth || !mediaHeight || !frameWidth || !frameHeight) return 'cover';
  const frameAspect = frameWidth / Math.max(1, frameHeight);
  const videoAspect = mediaWidth / mediaHeight;
  const coverCropRatio = videoAspect > frameAspect
    ? 1 - (frameAspect / videoAspect)
    : 1 - (videoAspect / frameAspect);
  return coverCropRatio <= 0.12 ? 'cover' : 'contain';
}

function remoteVideoToFeedItem(video: DouyinVideoRecord, index: number): FeedItem | null {
  const videoUrls = (video.video?.play_addr?.url_list || [])
    .filter(isVideoPlaybackUrl)
    .map(normalizeRemoteVideoUrl)
    .filter(Boolean);
  if (!videoUrls.length) return null;
  const videoUrl = videoUrls[0];
  const coverUrl = normalizeRemoteAsset(video.video?.cover?.url_list?.[0]);
  const avatarUrl = authorAvatarUrl(video);
  const musicTitle = video.music?.title || '原创音乐';
  const musicAuthor = video.music?.author ? ` · ${video.music.author}` : '';
  return {
    id: `dy-${video.aweme_id || index}`,
    awemeId: video.aweme_id,
    kind: 'video',
    author: video.author?.nickname || `抖音用户${index + 1}`,
    avatarUrl,
    title: video.desc || '刷到一条真实抖音素材',
    music: `${musicTitle}${musicAuthor}`,
    location: index % 3 === 0 ? '推荐' : undefined,
    likes: formatRemoteCount(video.statistics?.digg_count, '8.8w'),
    comments: formatRemoteCount(video.statistics?.comment_count, '999'),
    collects: formatRemoteCount(video.statistics?.collect_count, '1.2w'),
    shares: formatRemoteCount(video.statistics?.share_count, '2.1k'),
    tone: FEED_TONES[index % FEED_TONES.length],
    videoUrl,
    videoUrls,
    coverUrl,
    publisherHref: `/user?awemeId=${encodeURIComponent(video.aweme_id || String(index))}`,
    sourceLabel: '抖音素材',
    mediaWidth: video.video?.width,
    mediaHeight: video.video?.height,
  };
}

async function fetchDouyinFeedItemsFrom(source: string) {
  const response = await fetch(
    source,
    source.startsWith('/') ? DATA_FETCH_OPTIONS : { ...DATA_FETCH_OPTIONS, mode: 'cors' },
  );
  if (!response.ok) throw new Error(`load douyin feed failed: ${response.status}`);
  const rows = await response.json() as DouyinVideoRecord[];
  const items = rows
    .map(remoteVideoToFeedItem)
    .filter((item): item is FeedItem => Boolean(item));
  const commentVideoIdSet = new Set(DOUYIN_COMMENT_VIDEO_IDS);
  return [
    ...items.filter((item) => item.awemeId && commentVideoIdSet.has(item.awemeId)),
    ...items.filter((item) => !item.awemeId || !commentVideoIdSet.has(item.awemeId)),
  ];
}

async function fetchDouyinFeedItems() {
  return shuffleItems(await fetchDouyinFeedItemsFrom(DOUYIN_VIDEO_SOURCE));
}

function takeLoop<T>(items: T[], count: number, start = 0) {
  if (!items.length) return [];
  const size = Math.min(count, items.length);
  return Array.from({ length: size }, (_, index) => items[(start + index) % items.length]);
}

function liveRoomFeedItems(rooms: Room[], liveSources: string[]): FeedItem[] {
  if (!rooms.length) return [];
  const tones: FeedItem['tone'][] = ['cyan', 'dark', 'violet', 'rose'];
  return rooms.map((room, index) => {
    const name = room.name || `直播间${index + 1}`;
    const videoUrls = liveSources.length ? takeLoop(liveSources, liveSources.length, index) : undefined;
    return {
      id: `room-${room.id}`,
      kind: 'live',
      author: name,
      title: `${name} 正在直播，进入直播间看今日精选好物`,
      music: `直播中 · ${name}`,
      location: room.platform || 'LiveAuction',
      likes: `${index + 8}.${(index + 3) % 10}w`,
      comments: `${index + 1}.${(index + 2) % 10}w`,
      collects: `${index + 6}.${index}k`,
      shares: `${index + 2}.${index + 1}k`,
      tone: tones[index % tones.length] || 'cyan',
      liveLabel: `LIVE · ${name}`,
      liveHref: `/m/room/${encodeURIComponent(room.id)}`,
      videoUrl: videoUrls?.[0],
      videoUrls,
    };
  });
}

function mixFeedItems(liveItems: FeedItem[], videoItems: FeedItem[]) {
  const videos = takeLoop(videoItems, CHANNEL_VIDEO_WINDOW);
  const lives = liveItems;
  return [
    videos[0],
    lives[0],
    videos[1],
    videos[2],
    lives[1],
    ...videos.slice(3, CHANNEL_VIDEO_WINDOW),
  ].filter((item): item is FeedItem => Boolean(item));
}

function channelItems(channelIndex: number, liveItems: FeedItem[], videoItems: FeedItem[]) {
  const videos = videoItems.length ? videoItems : VIDEO_FEED_ITEMS;
  const lives = liveItems;
  const mixed = mixFeedItems(lives, videos);
  if (channelIndex === 0) return mixed.slice(2).concat(mixed.slice(0, 2));
  if (channelIndex === 1) return takeLoop(videos, CHANNEL_VIDEO_WINDOW, 5).concat(lives.slice(0, 1));
  if (channelIndex === 2) return mixed.slice().reverse();
  if (channelIndex === LIVE_CHANNEL_INDEX) return lives;
  return mixed;
}

function Avatar({ name, src }: { name: string; src?: string }) {
  return (
    <span className="dyHomeReplicaAvatar">
      {src ? <img src={src} alt="" loading="lazy" referrerPolicy="no-referrer" /> : name.slice(0, 1)}
    </span>
  );
}

function HomeIcon({ name }: { name: 'menu' | 'search' | 'heart' | 'comment' | 'star' | 'share' | 'music' }) {
  if (name === 'menu') {
    return (
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 7h16M4 12h16M4 17h16" />
      </svg>
    );
  }
  if (name === 'search') {
    return (
      <svg viewBox="0 0 512 512" aria-hidden="true">
        <path d="M456.69 421.39 362.6 327.3a173.8 173.8 0 0 0 34.84-104.58C397.44 126.38 319.06 48 222.72 48S48 126.38 48 222.72s78.38 174.72 174.72 174.72A173.8 173.8 0 0 0 327.3 362.6l94.09 94.09a25 25 0 0 0 35.3-35.3M97.92 222.72a124.8 124.8 0 1 1 124.8 124.8 124.95 124.95 0 0 1-124.8-124.8" />
      </svg>
    );
  }
  if (name === 'heart') {
    return (
      <svg viewBox="0 0 891 816" aria-hidden="true">
        <path d={DY_HEART_PATH} />
      </svg>
    );
  }
  if (name === 'comment') {
    return (
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M21.25 8.18a9.8 9.8 0 0 0-2.16-3.25 10 10 0 0 0-14.15 0 9.8 9.8 0 0 0-2.17 3.25A10 10 0 0 0 2.01 12a9.7 9.7 0 0 0 .74 3.77l-.5 3.65a1.95 1.95 0 0 0 1.29 2.26c.297.098.613.122.92.07l3.65-.54a9.8 9.8 0 0 0 3.88.79 10 10 0 0 0 9.24-13.82zM7.73 13.61a1.61 1.61 0 1 1 .001-3.22 1.61 1.61 0 0 1 0 3.22m4.28 0a1.61 1.61 0 1 1 .001-3.22 1.61 1.61 0 0 1 0 3.22m4.28 0a1.61 1.61 0 1 1 .001-3.22 1.61 1.61 0 0 1 0 3.22" />
      </svg>
    );
  }
  if (name === 'star') {
    return (
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="m12 17.27 4.15 2.51c.76.46 1.69-.22 1.49-1.08l-1.1-4.72 3.67-3.18c.67-.58.31-1.68-.57-1.75l-4.83-.41-1.89-4.46c-.34-.81-1.5-.81-1.84 0L9.19 8.63l-4.83.41c-.88.07-1.24 1.17-.57 1.75l3.67 3.18-1.1 4.72c-.2.86.73 1.54 1.49 1.08z" />
      </svg>
    );
  }
  if (name === 'share') {
    return <img className="dyHomeReplicaShareImage" src="/douyin-assets/icons/share-white-full.png" alt="" />;
  }
  if (name === 'music') {
    return (
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M9 18.2a3 3 0 1 1-2.4-2.94V6.1l10.7-2.3v10.9a3 3 0 1 1-1.9-2.8V7.2L8.5 8.68v7.6c.32.5.5 1.1.5 1.92Z" />
      </svg>
    );
  }
  return null;
}

function LikeBurstIcon() {
  return (
    <span className="dyHomeReplicaLikeBurst" aria-hidden="true">
      <i className="shockRing" />
      <i className="heartAura"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartCore"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartEcho echo0"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartEcho echo1"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartEcho echo2"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartEcho echo3"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartEcho echo4"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="heartEcho echo5"><svg viewBox="0 0 891 816"><path d={DY_HEART_PATH} /></svg></i>
      <i className="sparkDot spark0" />
      <i className="sparkDot spark1" />
      <i className="sparkDot spark2" />
      <i className="sparkDot spark3" />
      <i className="sparkDot spark4" />
      <i className="sparkDot spark5" />
    </span>
  );
}

function chooseVideoFit(video: HTMLVideoElement) {
  const slideRect = video.closest('.dyHomeReplicaFeedSlide')?.getBoundingClientRect();
  if (!slideRect || !video.videoWidth || !video.videoHeight) return 'contain';
  return chooseVideoFitFromSize(video.videoWidth, video.videoHeight, slideRect.width, Math.max(1, slideRect.height - 58));
}

function SideCard({ title, action, children }: { title: string; action?: string; children: ReactNode }) {
  return (
    <section className="dyHomeReplicaSideCard">
      <header>
        <b>{title}</b>
        {action ? <button type="button">{action} ›</button> : <span />}
      </header>
      {children}
    </section>
  );
}

function Sidebar({ onClose }: { onClose: () => void }) {
  return (
    <aside className="dyHomeReplicaSidebar">
      <header className="dyHomeReplicaSideHeader">
        <b>下午好</b>
        <a href="/scan"><span>▣</span>扫一扫</a>
      </header>
      <SideCard title="常用小程序" action="全部">
        <div className="dyHomeReplicaMiniGrid">
          {['今日头条', '西瓜视频'].map((item) => (
            <button type="button" key={item}>
              <span>{item.slice(0, 1)}</span>
              <b>{item}</b>
            </button>
          ))}
        </div>
      </SideCard>
      <SideCard title="最近常看" action="全部">
        <div className="dyHomeReplicaRecentGrid">
          {SIDE_RECENTS.map((item) => (
            <a href="/message/chat" key={item}>
              <Avatar name={item} />
              <b>{item}</b>
            </a>
          ))}
        </div>
      </SideCard>
      <SideCard title="常用功能">
        <div className="dyHomeReplicaFeatureGrid">
          {SIDE_FEATURES.map((item) => (
            <a href={item === '设置' ? '/me/right-menu/setting' : item === '观看历史' ? '/me/right-menu/look-history' : '/me'} key={item}>
              <span>{item.slice(0, 1)}</span>
              <b>{item}</b>
            </a>
          ))}
        </div>
      </SideCard>
      <button type="button" className="dyHomeReplicaSideMaskButton" onClick={onClose}>回到首页</button>
    </aside>
  );
}

function IndicatorHome({
  activeChannel,
  onOpenSidebar,
  onSetChannel,
}: {
  activeChannel: number;
  onOpenSidebar: () => void;
  onSetChannel: (index: number) => void;
}) {
  return (
    <header className="dyHomeReplicaIndicator">
      <button type="button" aria-label="打开侧边栏" onClick={onOpenSidebar}><HomeIcon name="menu" /></button>
      <nav aria-label="首页频道">
        {TOP_NAV_CHANNELS.map((channel) => {
          const feedIndex = CHANNELS.indexOf(channel);
          const isShop = channel === '商城';
          return (
            <button
              type="button"
              className={!isShop && activeChannel === feedIndex ? 'active' : ''}
              onClick={() => {
                if (isShop) navigateTo('/shop');
                else if (feedIndex >= 0) onSetChannel(feedIndex);
              }}
              key={channel}
            >
              {channel}
            </button>
          );
        })}
      </nav>
      <a href="/home/search" aria-label="搜索"><HomeIcon name="search" /></a>
    </header>
  );
}

function ActionRail({
  item,
  liked,
  onComment,
  onLike,
  onShare,
}: {
  item: FeedItem;
  liked: boolean;
  onComment: () => void;
  onLike: () => void;
  onShare: () => void;
}) {
  const [followed, setFollowed] = useState(false);
  const [collected, setCollected] = useState(false);

  return (
    <aside className="dyHomeReplicaActionRail">
      <div className="dyHomeReplicaAuthorCtn">
        <a href={publisherHref(item)} className="dyHomeReplicaAuthor" aria-label={`进入 ${item.author} 的主页`}>
          <Avatar name={item.author} src={item.avatarUrl} />
        </a>
        <span
          className={`dyHomeReplicaAuthorOptions${followed ? ' attention' : ''}`}
          role="button"
          tabIndex={0}
          aria-label={followed ? '已关注' : '关注'}
          onClick={() => setFollowed((value) => !value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' || event.key === ' ') {
              event.preventDefault();
              setFollowed((value) => !value);
            }
          }}
        >
          <img className="no" src="/douyin-assets/icons/add-light.png" alt="" />
          <img className="yes" src="/douyin-assets/icons/ok-red.png" alt="" />
        </span>
      </div>
      <button type="button" className={liked ? 'active' : ''} onClick={onLike}>
        <span className="dyHomeReplicaToolbarIcon"><HomeIcon name="heart" /></span>
        <small>{item.likes}</small>
      </button>
      <button type="button" onClick={onComment}>
        <span className="dyHomeReplicaToolbarIcon"><HomeIcon name="comment" /></span>
        <small>{item.comments}</small>
      </button>
      <button type="button" className={collected ? 'active collect' : 'collect'} onClick={() => setCollected((value) => !value)}>
        <span className="dyHomeReplicaToolbarIcon"><HomeIcon name="star" /></span>
        <small>{item.collects}</small>
      </button>
      <button type="button" onClick={onShare}>
        <span className="dyHomeReplicaToolbarIcon"><HomeIcon name="share" /></span>
        <small>{item.shares}</small>
      </button>
      <a href="/home/music" className="dyHomeReplicaMusicDisc"><HomeIcon name="music" /></a>
    </aside>
  );
}

function FeedSlide({
  item,
  active,
  shouldLoad,
  liked,
  paused,
  progressKey,
  onDoubleTapLike,
  onLike,
  onToggle,
  onEnterLive,
  onOpenSheet,
}: {
  item: FeedItem;
  active: boolean;
  shouldLoad: boolean;
  liked: boolean;
  paused: boolean;
  progressKey: string;
  onDoubleTapLike: () => void;
  onLike: () => void;
  onToggle: () => void;
  onEnterLive: (href: string) => void;
  onOpenSheet: (sheet: HomeSheet) => void;
}) {
  const [likeBurst, setLikeBurst] = useState(0);
  const [videoSourceIndex, setVideoSourceIndex] = useState(0);
  const [mediaFailed, setMediaFailed] = useState(false);
  const [buffering, setBuffering] = useState(false);
  const [playbackProgress, setPlaybackProgress] = useState({ src: '', value: 0 });
  const [videoFitState, setVideoFitState] = useState<{ src: string; fit: 'cover' | 'contain' }>({ src: '', fit: 'cover' });
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const tapTimer = useRef<number | undefined>(undefined);
  const longPressTimer = useRef<number | undefined>(undefined);
  const longPressFired = useRef(false);
  const fallbackVideoSources = resolveLivePlaylist();
  const videoSources = (
    item.videoUrls?.length
      ? item.videoUrls
      : item.videoUrl
        ? [item.videoUrl]
        : fallbackVideoSources
  ).filter(Boolean);
  const videoSrc = videoSources[videoSourceIndex] || '';
  const sourceVideoFit = chooseVideoFitFromSize(item.mediaWidth, item.mediaHeight);
  const videoFit = videoFitState.src === videoSrc ? videoFitState.fit : sourceVideoFit;
  const shouldAttachVideo = active && shouldLoad;

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    if (!active || !shouldAttachVideo) {
      video.pause();
      if (!active) {
        try {
          video.currentTime = 0;
        } catch {
          // Ignore browsers that reject resetting an unloaded media element.
        }
      }
      video.removeAttribute('src');
      video.load();
      return;
    }
    if (mediaFailed || !videoSrc) return;
    if (active && !paused) {
      if (video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) setBuffering(true);
      void video.play().catch(() => undefined);
    } else {
      video.pause();
      if (!active) video.currentTime = 0;
    }
  }, [active, paused, mediaFailed, shouldAttachVideo, videoSrc]);

  useEffect(() => {
    return () => {
      if (tapTimer.current) window.clearTimeout(tapTimer.current);
      if (longPressTimer.current) window.clearTimeout(longPressTimer.current);
    };
  }, []);

  function clearLongPress() {
    if (longPressTimer.current) window.clearTimeout(longPressTimer.current);
    longPressTimer.current = undefined;
  }

  function handlePrimaryTap() {
    if (item.kind === 'live') {
      onEnterLive(item.liveHref || '/home/live');
      return;
    }
    if (longPressFired.current) {
      longPressFired.current = false;
      return;
    }
    if (tapTimer.current) {
      window.clearTimeout(tapTimer.current);
      tapTimer.current = undefined;
      onDoubleTapLike();
      setLikeBurst((value) => value + 1);
      return;
    }
    tapTimer.current = window.setTimeout(() => {
      tapTimer.current = undefined;
      onToggle();
    }, 210);
  }

  function handlePointerDown(event: PointerEvent) {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    longPressFired.current = false;
    clearLongPress();
    longPressTimer.current = window.setTimeout(() => {
      longPressFired.current = true;
      onOpenSheet('more');
    }, 520);
  }

  function handlePointerUp() {
    clearLongPress();
  }

  function updatePlaybackProgress(video: HTMLVideoElement) {
    const duration = video.duration;
    if (!Number.isFinite(duration) || duration <= 0) {
      setPlaybackProgress({ src: videoSrc, value: 0 });
      return;
    }
    setPlaybackProgress({ src: videoSrc, value: Math.max(0, Math.min(100, (video.currentTime / duration) * 100)) });
  }

  const visibleProgress = active && playbackProgress.src === videoSrc ? playbackProgress.value : 0;

  return (
    <section className={`dyHomeReplicaFeedSlide dyHomeReplicaTone-${item.tone} dyHomeReplicaFit-${videoFit}`}>
      {!mediaFailed && videoSrc ? (
        <video
          ref={videoRef}
          key={videoSrc}
          src={shouldAttachVideo ? videoSrc : undefined}
          poster={item.coverUrl}
          muted
          loop
          playsInline
          preload={shouldAttachVideo ? 'auto' : 'none'}
          onLoadStart={() => setBuffering(true)}
          onLoadedMetadata={(event) => {
            setVideoFitState({ src: videoSrc, fit: chooseVideoFit(event.currentTarget) });
            updatePlaybackProgress(event.currentTarget);
          }}
          onDurationChange={(event) => updatePlaybackProgress(event.currentTarget)}
          onTimeUpdate={(event) => updatePlaybackProgress(event.currentTarget)}
          onCanPlay={() => setBuffering(false)}
          onPlaying={() => setBuffering(false)}
          onWaiting={() => active && setBuffering(true)}
          onStalled={() => active && setBuffering(true)}
          onClick={handlePrimaryTap}
          onError={() => {
            if (videoSourceIndex < videoSources.length - 1) {
              setVideoSourceIndex((value) => value + 1);
              setBuffering(true);
              setPlaybackProgress({ src: videoSrc, value: 0 });
              return;
            }
            setMediaFailed(true);
            setBuffering(false);
            setPlaybackProgress({ src: videoSrc, value: 0 });
          }}
          onPointerDown={handlePointerDown}
          onPointerLeave={handlePointerUp}
          onPointerUp={handlePointerUp}
          onPointerCancel={handlePointerUp}
        />
      ) : null}
      <div
        className={`dyHomeReplicaVideoFallback${item.coverUrl ? ' hasCover' : ''}`}
        style={item.coverUrl ? { '--dy-cover': `url("${item.coverUrl}")` } as CSSProperties : undefined}
        aria-hidden="true"
        onClick={handlePrimaryTap}
        onPointerDown={handlePointerDown}
        onPointerLeave={handlePointerUp}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerUp}
      >
        <span>{item.sourceLabel || (item.kind === 'live' ? 'LIVE' : 'DOUYIN')}</span>
      </div>
      {active && shouldLoad && !paused && buffering && !mediaFailed ? (
        <div className="dyHomeReplicaVideoLoading" aria-hidden="true">
          <DouyinLoading />
        </div>
      ) : null}
      <div className="dyHomeReplicaProgress" aria-hidden="true">
        {active && videoSrc && !mediaFailed ? <i key={progressKey} style={{ width: `${visibleProgress}%` }} /> : null}
      </div>
      {likeBurst ? <LikeBurstIcon key={likeBurst} /> : null}
      {paused && active ? <button type="button" className="dyHomeReplicaPlayMark" onClick={onToggle}>▶</button> : null}
      {item.kind === 'live' ? (
        <a
          className="dyHomeReplicaLiveEnter"
          href={item.liveHref || '/home/live'}
          aria-label="进入直播间"
          onClick={(event) => {
            event.preventDefault();
            onEnterLive(item.liveHref || '/home/live');
          }}
        >
          点击进入直播间
        </a>
      ) : null}
      <section className="dyHomeReplicaCaption">
        {item.kind === 'live' ? <span className="dyHomeReplicaLiveTag">直播中</span> : null}
        <a className="dyHomeReplicaCaptionAuthor" href={publisherHref(item)}>@{item.author}</a>
        <p>{item.title}</p>
      </section>
      {item.kind === 'video' ? (
        <ActionRail
          item={item}
          liked={liked}
          onLike={onLike}
          onComment={() => onOpenSheet('comment')}
          onShare={() => onOpenSheet('share')}
        />
      ) : null}
    </section>
  );
}

function SheetFrame({
  title,
  label,
  className = '',
  onClose,
  children,
  footer,
}: {
  title: string;
  label: string;
  className?: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const sheetTouchStart = useRef({ x: 0, y: 0 });

  function handleSheetTouchStart(event: TouchEvent) {
    event.stopPropagation();
    const touch = event.touches[0];
    sheetTouchStart.current = { x: touch?.clientX ?? 0, y: touch?.clientY ?? 0 };
  }

  function handleSheetTouchEnd(event: TouchEvent) {
    event.stopPropagation();
    const touch = event.changedTouches[0];
    const dx = (touch?.clientX ?? sheetTouchStart.current.x) - sheetTouchStart.current.x;
    const dy = (touch?.clientY ?? sheetTouchStart.current.y) - sheetTouchStart.current.y;
    if (dy > 54 && Math.abs(dy) > Math.abs(dx) * 1.2) onClose();
  }

  return (
    <section
      className={`dyHomeReplicaSheet ${className}`.trim()}
      role="dialog"
      aria-modal="true"
      aria-label={label}
      onTouchStart={handleSheetTouchStart}
      onTouchEnd={handleSheetTouchEnd}
    >
      <span className="dyHomeReplicaSheetHandle" aria-hidden="true" />
      <header><b>{title}</b><button type="button" onClick={onClose}>×</button></header>
      {children}
      {footer}
    </section>
  );
}

function ShareSheet({ onClose, onFriend }: { onClose: () => void; onFriend: () => void }) {
  return (
    <SheetFrame title="分享到" label="分享" className="dyHomeReplicaShareSheet" onClose={onClose}>
      <div>
        {SHARE_OPTIONS.map((item) => (
          <button type="button" onClick={item === '私信朋友' ? onFriend : item === '举报' ? () => window.location.assign('/home/report') : undefined} key={item}>
            <span>{item.slice(0, 1)}</span>
            <b>{item}</b>
          </button>
        ))}
      </div>
    </SheetFrame>
  );
}

function MoreSheet({ onClose }: { onClose: () => void }) {
  return (
    <SheetFrame title="更多操作" label="更多" className="dyHomeReplicaMoreSheet" onClose={onClose}>
      <div>
        {['倍速播放', '自动连播', '清屏', '不感兴趣', '内容偏好', '投诉'].map((item) => <button type="button" key={item}>{item}</button>)}
      </div>
    </SheetFrame>
  );
}

function FriendSheet({ onClose }: { onClose: () => void }) {
  return (
    <SheetFrame
      title="私信给"
      label="分享给朋友"
      className="dyHomeReplicaFriendSheet"
      onClose={onClose}
      footer={<footer><button type="button">发送</button></footer>}
    >
      <label><span>⌕</span><input placeholder="搜索" /></label>
      <div>
        {SIDE_RECENTS.map((name) => (
          <button type="button" key={name}>
            <Avatar name={name} />
            <span><b>{name}</b><small>最近互动</small></span>
            <i />
          </button>
        ))}
      </div>
    </SheetFrame>
  );
}

export function DouyinHomeShell() {
  const restoredHomeState = useMemo(() => (isMeEntryPath() ? null : readHomeReturnState()), []);
  const [baseIndex, setBaseIndex] = useState(() => clampIndex(restoredHomeState?.baseIndex ?? initialBaseIndex(), 3));
  const [channelIndex, setChannelIndex] = useState(() => clampIndex(restoredHomeState?.channelIndex ?? initialChannelIndex(), CHANNELS.length));
  const [itemIndexes, setItemIndexes] = useState(() => {
    const saved = restoredHomeState?.itemIndexes || [];
    return CHANNELS.map((_, index) => Math.max(0, saved[index] || 0));
  });
  const [pausedId, setPausedId] = useState('');
  const [likedIds, setLikedIds] = useState<Set<string>>(() => new Set());
  const [sheet, setSheet] = useState<HomeSheet>(null);
  const [commentTarget, setCommentTarget] = useState<CommentTarget | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [publicRooms, setPublicRooms] = useState<Room[]>([]);
  const [publicRoomsLoaded, setPublicRoomsLoaded] = useState(false);
  const [remoteVideoItems, setRemoteVideoItems] = useState<FeedItem[]>([]);
  const touchStart = useRef({ x: 0, y: 0, at: 0 });
  const wheelGesture = useRef({ x: 0, y: 0, lastAt: 0, lockedUntil: 0 });
  const fallbackVideoItems = useMemo(() => shuffleItems(VIDEO_FEED_ITEMS), []);
  const liveDemoSources = useMemo(() => shuffleItems(resolveLivePlaylist()), []);
  const videoItems = remoteVideoItems.length ? remoteVideoItems : fallbackVideoItems;
  const liveItems = useMemo(() => liveRoomFeedItems(publicRooms, liveDemoSources), [liveDemoSources, publicRooms]);
  const channelFeeds = useMemo(() => CHANNELS.map((_, index) => channelItems(index, liveItems, videoItems)), [liveItems, videoItems]);
  const items = channelFeeds[channelIndex] || FEED_ITEMS;
  const itemIndex = clampIndex(itemIndexes[channelIndex] || 0, items.length);

  useEffect(() => {
    let disposed = false;
    void listPublicRooms().catch(() => []).then((rooms) => {
      if (disposed) return;
      const nextRooms = shuffleItems(rooms);
      setPublicRooms(nextRooms);
      const targetLiveRoomId = restoredHomeState?.targetLiveRoomId;
      if (!targetLiveRoomId) return;
      const targetIndex = liveRoomFeedItems(nextRooms, liveDemoSources)
        .findIndex((item) => liveRoomIdFromHref(item.liveHref) === targetLiveRoomId);
      if (targetIndex < 0) return;
      setBaseIndex(1);
      setChannelIndex(LIVE_CHANNEL_INDEX);
      setItemIndexes((current) => {
        const next = current.slice();
        next[LIVE_CHANNEL_INDEX] = targetIndex;
        return next;
      });
      setPausedId('');
    }).finally(() => {
      if (!disposed) setPublicRoomsLoaded(true);
    });
    void fetchDouyinFeedItems().then((items) => {
      if (!disposed) setRemoteVideoItems(items);
    }).catch(() => {
      if (!disposed) setRemoteVideoItems([]);
    });
    return () => {
      disposed = true;
    };
  }, [liveDemoSources, restoredHomeState]);

  useEffect(() => {
    const syncEntryPath = () => {
      if (isMeEntryPath()) setBaseIndex(2);
      else if (isLiveEntryPath()) {
        setBaseIndex(1);
        setChannelIndex(LIVE_CHANNEL_INDEX);
      } else if (location.pathname === '/' || location.pathname === '/home') setBaseIndex(1);
    };
    window.addEventListener('popstate', syncEntryPath);
    window.addEventListener(SPA_NAVIGATE_EVENT, syncEntryPath);
    syncEntryPath();
    return () => {
      window.removeEventListener('popstate', syncEntryPath);
      window.removeEventListener(SPA_NAVIGATE_EVENT, syncEntryPath);
    };
  }, []);

  function setCurrentItemIndex(next: number | ((current: number) => number)) {
    setItemIndexes((current) => {
      const copy = current.slice();
      const value = typeof next === 'function' ? next(copy[channelIndex] || 0) : next;
      copy[channelIndex] = wrapIndex(value, items.length);
      return copy;
    });
  }

  function handleTouchStart(event: TouchEvent) {
    const touch = event.touches[0];
    touchStart.current = { x: touch?.clientX ?? 0, y: touch?.clientY ?? 0, at: Date.now() };
  }

  function handleTouchEnd(event: TouchEvent) {
    if (sheet) return;
    const touch = event.changedTouches[0];
    const dx = (touch?.clientX ?? touchStart.current.x) - touchStart.current.x;
    const dy = (touch?.clientY ?? touchStart.current.y) - touchStart.current.y;
    const elapsed = Date.now() - touchStart.current.at;
    if (swipeDistance(dx, elapsed, HORIZONTAL_SWIPE_DISTANCE) && Math.abs(dx) > Math.abs(dy) * 1.22) {
      if (baseIndex !== 1) {
        setBaseIndex((current) => clampIndex(current + (dx < 0 ? 1 : -1), 3));
        return;
      }
      if (dx < 0) {
        if (channelIndex < CHANNELS.length - 1) setChannelIndex((current) => current + 1);
        else setBaseIndex(2);
      } else if (channelIndex > 0) {
        setChannelIndex((current) => current - 1);
      } else {
        setBaseIndex(0);
      }
      setPausedId('');
      return;
    }
    if (baseIndex === 1 && swipeDistance(dy, elapsed, VERTICAL_SWIPE_DISTANCE) && Math.abs(dy) > Math.abs(dx) * 1.08) {
      if (dy > 72 && itemIndex === 0) {
        setRefreshing(true);
        setPublicRoomsLoaded(false);
        void Promise.allSettled([listPublicRooms(), fetchDouyinFeedItems()])
          .then(([roomsResult, videosResult]) => {
            if (roomsResult.status === 'fulfilled') setPublicRooms(roomsResult.value);
            if (videosResult.status === 'fulfilled') setRemoteVideoItems(videosResult.value);
          })
          .finally(() => window.setTimeout(() => {
            setPublicRoomsLoaded(true);
            setRefreshing(false);
          }, 450));
        return;
      }
      setCurrentItemIndex((current) => current + (dy < 0 ? 1 : -1));
      setPausedId('');
    }
  }

  function handleWheel(event: WheelEvent<HTMLElement>) {
    if (sheet || baseIndex !== 1 || shouldLetWheelScroll(event.target)) return;
    event.preventDefault();

    const now = Date.now();
    const gesture = wheelGesture.current;
    if (now < gesture.lockedUntil) return;
    if (now - gesture.lastAt > WHEEL_STALE_MS) {
      gesture.x = 0;
      gesture.y = 0;
    }

    gesture.x += event.deltaX;
    gesture.y += event.deltaY;
    gesture.lastAt = now;

    const absX = Math.abs(gesture.x);
    const absY = Math.abs(gesture.y);
    if (Math.max(absX, absY) < WHEEL_SWIPE_DISTANCE) return;

    if (absY >= absX * 1.08) {
      setCurrentItemIndex((current) => current + (gesture.y > 0 ? 1 : -1));
    } else if (absX >= absY * 1.08) {
      if (gesture.x > 0) {
        if (channelIndex < CHANNELS.length - 1) setChannelIndex((current) => current + 1);
        else setBaseIndex(2);
      } else if (channelIndex > 0) {
        setChannelIndex((current) => current - 1);
      } else {
        setBaseIndex(0);
      }
    }

    setPausedId('');
    wheelGesture.current = { x: 0, y: 0, lastAt: now, lockedUntil: now + WHEEL_LOCK_MS };
  }

  function selectChannel(index: number) {
    setChannelIndex(index);
    setPausedId('');
  }

  function toggleLike(itemId: string) {
    setLikedIds((current) => {
      const next = new Set(current);
      if (next.has(itemId)) next.delete(itemId);
      else next.add(itemId);
      return next;
    });
  }

  function likeItem(itemId: string) {
    setLikedIds((current) => {
      if (current.has(itemId)) return current;
      const next = new Set(current);
      next.add(itemId);
      return next;
    });
  }

  function openSheetForItem(nextSheet: HomeSheet, item: FeedItem) {
    if (nextSheet === 'comment') {
      setCommentTarget({ videoId: item.awemeId || item.id, comments: item.comments });
    }
    setSheet(nextSheet);
  }

  function enterLiveRoom(href: string) {
    writeHomeReturnState({
      baseIndex: 1,
      channelIndex,
      itemIndexes,
      targetLiveRoomId: liveRoomIdFromHref(href) || undefined,
    });
    navigateTo(href);
  }

  function handleFooterTab(tab: Exclude<DouyinTab, 'publish'>, href: string) {
    if (tab === 'home') {
      setBaseIndex(1);
      navigateTo('/home');
      return;
    }
    if (tab === 'me') {
      setBaseIndex(2);
      navigateTo('/me');
      return;
    }
    navigateTo(href);
  }

  const activeItem = items[itemIndex] || items[0];
  const horizontalOffset = baseIndex * (100 / 3);
  const shellClassName = [
    'mobileShell dyHomeReplicaShell',
    sheet ? 'isSheetOpen' : '',
    sheet === 'comment' ? 'isCommentSheetOpen' : '',
  ].filter(Boolean).join(' ');

  return (
    <main className={shellClassName} onTouchStart={handleTouchStart} onTouchEnd={handleTouchEnd} onWheel={handleWheel}>
      <section className="dyHomeReplicaHorizontal" style={{ transform: `translateX(-${horizontalOffset}%)` }}>
        <Sidebar onClose={() => setBaseIndex(1)} />
        <section className="dyHomeReplicaMain">
          <IndicatorHome
            activeChannel={channelIndex}
            onOpenSidebar={() => setBaseIndex(0)}
            onSetChannel={selectChannel}
          />
          <div className={`dyHomeReplicaRefreshNotice${refreshing ? ' active' : ''}`}>{refreshing ? '刷新中' : '下拉刷新内容'}</div>
          <section className="dyHomeReplicaChannelTrack" style={{ transform: `translateX(-${channelIndex * 100}%)` }}>
            {channelFeeds.map((feed, feedIndex) => {
              const paneItemIndex = clampIndex(itemIndexes[feedIndex] || 0, feed.length);
              const liveFeedLoading = feedIndex === LIVE_CHANNEL_INDEX && !feed.length && !publicRoomsLoaded;
              return (
                <section className="dyHomeReplicaChannelPane" key={CHANNELS[feedIndex]}>
                  {feed.length ? (
                    <section className="dyHomeReplicaVertical" style={{ transform: `translateY(-${paneItemIndex * 100}%)` }}>
                      {feed.map((item, index) => (
                        <FeedSlide
                          item={item}
                          active={baseIndex === 1 && feedIndex === channelIndex && index === itemIndex}
                          shouldLoad={baseIndex === 1 && feedIndex === channelIndex && Math.abs(index - paneItemIndex) <= 2}
                          liked={likedIds.has(item.id)}
                          paused={pausedId === item.id}
                          progressKey={`${feedIndex}-${index}-${item.id}-${pausedId === item.id ? 'paused' : 'playing'}`}
                          onDoubleTapLike={() => likeItem(item.id)}
                          onLike={() => toggleLike(item.id)}
                          onToggle={() => setPausedId((current) => (current === item.id ? '' : item.id))}
                          onEnterLive={enterLiveRoom}
                          onOpenSheet={(nextSheet) => openSheetForItem(nextSheet, item)}
                          key={`${feedIndex}-${index}-${item.id}`}
                        />
                      ))}
                    </section>
                  ) : liveFeedLoading ? (
                    <section className="dyHomeReplicaLiveEmpty">
                      <DouyinLoading mode="inline" />
                      <b>直播加载中</b>
                    </section>
                  ) : feedIndex === LIVE_CHANNEL_INDEX ? (
                    <section className="dyHomeReplicaLiveEmpty">
                      <b>目前暂无直播</b>
                      <span>有商家主账号开拍后，直播间会出现在这里。</span>
                      <button
                        type="button"
                        onClick={() => {
                          setRefreshing(true);
                          setPublicRoomsLoaded(false);
                          void listPublicRooms()
                            .then((rooms) => setPublicRooms(rooms))
                            .catch(() => setPublicRooms([]))
                            .finally(() => window.setTimeout(() => {
                              setPublicRoomsLoaded(true);
                              setRefreshing(false);
                            }, 300));
                        }}
                      >
                        刷新
                      </button>
                    </section>
                  ) : null}
                </section>
              );
            })}
          </section>
        </section>
        <UserPanel />
      </section>
      <DouyinTabBar active={baseIndex === 2 ? 'me' : 'home'} onTab={handleFooterTab} />
      {sheet === 'comment' && (commentTarget || activeItem) ? (
        <DouyinCommentSheet
          videoId={commentTarget?.videoId || activeItem.awemeId || activeItem.id}
          onClose={() => setSheet(null)}
        />
      ) : null}
      {sheet === 'share' && activeItem ? <ShareSheet onClose={() => setSheet(null)} onFriend={() => setSheet('friend')} /> : null}
      {sheet === 'more' ? <MoreSheet onClose={() => setSheet(null)} /> : null}
      {sheet === 'friend' ? <FriendSheet onClose={() => setSheet(null)} /> : null}
    </main>
  );
}
