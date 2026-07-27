import { useCallback, useEffect, useRef, useState } from 'react';
import { getServerNowMs } from '../../../shared/lib/time';
import { isHls, resolveLiveSource } from '../hooks/useLivePlayer';
import { LiveOverlay } from './LiveOverlay';

type Props = {
  poster?: string;
  anchorName?: string;
  onlineCount?: number;
  wsState: string;
  roomName: string;
  source?: string;
  liveStartedAtUnixMs?: number | string;
  serverTimeUnixMs?: number | string;
  serverTimeReceivedAtUnixMs?: number;
};

export function LivePlayer({
  poster,
  anchorName,
  onlineCount,
  wsState,
  roomName,
  source: controlledSource,
  liveStartedAtUnixMs,
  serverTimeUnixMs,
  serverTimeReceivedAtUnixMs,
}: Props) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [internalSource] = useState(resolveLiveSource);
  const source = controlledSource || internalSource;
  const [message, setMessage] = useState('');

  const playVideo = () => {
    const video = videoRef.current;
    if (!video) return;
    video.muted = true;
    video.volume = 0;
    video.setAttribute('muted', '');
    video.setAttribute('playsinline', '');
    void video.play().catch(() => undefined);
  };

  const syncPlaybackPosition = useCallback(() => {
    const video = videoRef.current;
    const liveStartedAt = Number(liveStartedAtUnixMs || 0);
    if (!video || !Number.isFinite(liveStartedAt) || liveStartedAt <= 0) return;
    if (!Number.isFinite(video.duration) || video.duration <= 1) return;
    const serverNow = getServerNowMs(serverTimeUnixMs, serverTimeReceivedAtUnixMs);
    const elapsedSeconds = Math.max(0, (serverNow - liveStartedAt) / 1000);
    const targetTime = elapsedSeconds % video.duration;
    if (Math.abs(video.currentTime - targetTime) > 2.5) {
      video.currentTime = targetTime;
    }
  }, [liveStartedAtUnixMs, serverTimeReceivedAtUnixMs, serverTimeUnixMs]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return undefined;
    let disposed = false;
    const retryTimers: number[] = [];

    setMessage('直播画面加载中');
    video.pause();
    video.removeAttribute('src');

    if (isHls(source) && !video.canPlayType('application/vnd.apple.mpegurl')) {
      setMessage('当前浏览器不支持 HLS 直播源');
      return () => {
        disposed = true;
        retryTimers.forEach((timer) => window.clearTimeout(timer));
      };
    }

    video.src = source;
    video.load();

    const tryPlay = () => {
      if (!disposed) {
        syncPlaybackPosition();
        playVideo();
      }
    };
    retryTimers.push(
      window.setTimeout(tryPlay, 0),
      window.setTimeout(tryPlay, 250),
      window.setTimeout(tryPlay, 800),
      window.setTimeout(tryPlay, 1500),
    );

    return () => {
      disposed = true;
      retryTimers.forEach((timer) => window.clearTimeout(timer));
      video.removeAttribute('src');
      video.load();
    };
  }, [source, syncPlaybackPosition]);

  useEffect(() => {
    const timer = window.setInterval(syncPlaybackPosition, 15000);
    return () => window.clearInterval(timer);
  }, [syncPlaybackPosition]);

  return (
    <section className="livePlayerShell">
      <video
        ref={videoRef}
        className="nativeLiveVideo"
        poster={poster}
        autoPlay
        muted
        playsInline
        preload="auto"
        loop={!isHls(source)}
        onCanPlay={() => {
          setMessage('');
          syncPlaybackPosition();
          playVideo();
        }}
        onLoadedMetadata={syncPlaybackPosition}
        onPlaying={() => setMessage('')}
        onWaiting={() => setMessage('直播画面加载中')}
        onError={() => setMessage('直播画面加载失败')}
      />
      {message ? <div className="playerMessage">{message}</div> : null}
      <LiveOverlay anchorName={anchorName} onlineCount={onlineCount} wsState={wsState} roomName={roomName} />
    </section>
  );
}
