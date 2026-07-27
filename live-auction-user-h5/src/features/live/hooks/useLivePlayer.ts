const TOS_LIVE_DEMO_PLAYLIST = [
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/6931271799195831566.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/7326744032997166387.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/7161000281575148800.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/6882368275695586568.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/6993228049399549198.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/7260749400622894336.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/7280132304427666722.mp4',
  'https://liveauction.tos-cn-beijing.volces.com/douyin-h5/videos/7110263965858549003.mp4',
];

function configuredLiveSources() {
  const configuredList = import.meta.env.VITE_DEMO_LIVE_URLS as string | undefined;
  const configuredSingle = import.meta.env.VITE_DEMO_LIVE_URL as string | undefined;
  const list = configuredList
    ?.split(',')
    .map((url) => url.trim())
    .filter(Boolean);

  if (list?.length) return list;
  if (configuredSingle?.trim()) return [configuredSingle.trim()];
  return TOS_LIVE_DEMO_PLAYLIST;
}

export function resolveLivePlaylist() {
  const playlist = configuredLiveSources();
  return playlist.length ? playlist : ['/demo-live.mp4'];
}

export function resolveLiveSource() {
  const playlist = resolveLivePlaylist();
  return playlist[0] || '/demo-live.mp4';
}

export function resolveInitialLiveSource() {
  return resolveLiveSource();
}

export function resolveNextLiveSource(currentSource: string) {
  const playlist = resolveLivePlaylist();
  if (playlist.length <= 1) return playlist[0] || '/demo-live.mp4';
  const index = playlist.indexOf(currentSource);
  return playlist[(index + 1 + playlist.length) % playlist.length] || playlist[0];
}

export function isHls(url: string) { return /\.m3u8($|\?)/i.test(url); }
