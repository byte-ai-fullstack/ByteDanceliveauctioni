import { useEffect, useRef, useState } from 'react';
import { ArrowRight, BarChart3, Gavel, Heart, ShieldCheck, ShoppingBag, Store, Users } from 'lucide-react';
import { readLocalJson } from '../../shared/auth/authStorage';

type SoftParticle = {
  x: number;
  y: number;
  baseX: number;
  baseY: number;
  vx: number;
  vy: number;
  size: number;
  opacity: number;
  rotate: number;
  vr: number;
  kind: 'dot' | 'sparkle' | 'petal' | 'coin' | 'arc';
  color: string;
};

type PointerDisturbance = {
  id: number;
  x: number;
  y: number;
  text: string;
  rotate: number;
  size: number;
};

type TextParticle = {
  x: number;
  y: number;
  tx: number;
  ty: number;
  vx: number;
  vy: number;
  size: number;
  color: string;
};

type CountUpParts = {
  prefix: string;
  target: number;
  suffix: string;
};

const leftRightBlankZones = [
  { left: [1, 7], top: [12, 76] },
  { left: [88, 96], top: [12, 76] },
];

const productSafeZones = [
  { left: [-4, 3], top: [10, 76] },
  { left: [92, 98], top: [10, 76] },
];

function randomBetween([min, max]: number[]) {
  return min + Math.random() * (max - min);
}

const statNumberFormatter = new Intl.NumberFormat('en-US');

function parseCountUpValue(value: string): CountUpParts | null {
  const match = value.match(/^([^0-9]*)([\d,]+)(.*)$/);
  if (!match) return null;
  const target = Number(match[2].replace(/,/g, ''));
  if (!Number.isFinite(target)) return null;
  return {
    prefix: match[1],
    target,
    suffix: match[3],
  };
}

function formatCountUpValue(parts: CountUpParts, amount: number) {
  return `${parts.prefix}${statNumberFormatter.format(Math.round(amount))}${parts.suffix}`;
}

function easeOutCubic(progress: number) {
  return 1 - Math.pow(1 - progress, 3);
}

function CountUpStatValue({
  value,
  duration = 2200,
  delay = 0,
}: {
  value: string;
  duration?: number;
  delay?: number;
}) {
  const parts = parseCountUpValue(value);
  const reduceMotion = typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (!parts || reduceMotion) {
    return <strong aria-label={value}>{value}</strong>;
  }

  return (
    <AnimatedCountUpStatValue
      key={`${value}:${duration}:${delay}`}
      value={value}
      parts={parts}
      duration={duration}
      delay={delay}
    />
  );
}

function AnimatedCountUpStatValue({
  value,
  parts,
  duration,
  delay,
}: {
  value: string;
  parts: CountUpParts;
  duration: number;
  delay: number;
}) {
  const ref = useRef<HTMLElement | null>(null);
  const { prefix, suffix, target } = parts;
  const [displayValue, setDisplayValue] = useState(() => formatCountUpValue(parts, 0));

  useEffect(() => {
    const currentParts = { prefix, suffix, target };
    let animationFrame = 0;
    let delayTimer = 0;
    let observer: IntersectionObserver | null = null;
    let started = false;

    const animate = (startTime: number) => {
      const tick = (now: number) => {
        const progress = Math.min((now - startTime) / duration, 1);
        setDisplayValue(formatCountUpValue(currentParts, target * easeOutCubic(progress)));
        if (progress < 1) {
          animationFrame = window.requestAnimationFrame(tick);
          return;
        }
        setDisplayValue(value);
      };
      animationFrame = window.requestAnimationFrame(tick);
    };

    const start = () => {
      if (started) return;
      started = true;
      delayTimer = window.setTimeout(() => animate(performance.now()), delay);
    };

    const node = ref.current;
    if (node && 'IntersectionObserver' in window) {
      observer = new IntersectionObserver((entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          observer?.disconnect();
          start();
        }
      }, { threshold: 0.45 });
      observer.observe(node);
    } else {
      start();
    }

    return () => {
      observer?.disconnect();
      window.clearTimeout(delayTimer);
      window.cancelAnimationFrame(animationFrame);
    };
  }, [delay, duration, prefix, suffix, target, value]);

  return <strong ref={ref} aria-label={value}>{displayValue}</strong>;
}

type PlacedPoint = { left: number; top: number };

function pointDistance(a: PlacedPoint, b: PlacedPoint) {
  const dx = a.left - b.left;
  const dy = a.top - b.top;
  return Math.sqrt(dx * dx + dy * dy);
}

function pickNonOverlappingPoint(
  zones: Array<{ left: number[]; top: number[] }>,
  existing: PlacedPoint[],
  minDistance = 18,
) {
  const fallback = { left: randomBetween(zones[0].left), top: randomBetween(zones[0].top) };
  let best = fallback;
  let bestDistance = -1;

  for (let attempt = 0; attempt < 28; attempt += 1) {
    const zone = zones[Math.floor(Math.random() * zones.length)];
    const point = { left: randomBetween(zone.left), top: randomBetween(zone.top) };
    const nearest = existing.length ? Math.min(...existing.map((item) => pointDistance(point, item))) : Infinity;
    if (nearest > bestDistance) {
      best = point;
      bestDistance = nearest;
    }
    if (nearest >= minDistance) return point;
  }

  return best;
}

const particlePalette = {
  dot: ['#8EC8E8', '#F4A6C1', '#BDAAF6', '#FFFFFF'],
  sparkle: ['#F8D98E', '#FFFFFF', '#E4C7FF'],
  petal: ['#F7A8C8', '#F6C6D6'],
  coin: ['#F6C66A', '#F8D98E'],
  arc: ['rgba(190, 170, 245, 0.25)'],
};

const pick = <T,>(items: T[], index: number) => items[index % items.length];

function SoftParticleCanvas() {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    let animation = 0;
    let frame = 0;
    let particles: SoftParticle[] = [];
    const pointer = { x: 0, y: 0, active: false };

    const build = () => {
      const rect = canvas.getBoundingClientRect();
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.floor(rect.width * dpr);
      canvas.height = Math.floor(rect.height * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const kinds: SoftParticle['kind'][] = ['dot', 'dot', 'dot', 'sparkle', 'petal', 'coin', 'arc'];
      const count = rect.width < 720 ? 90 : 130;
      particles = Array.from({ length: count }, (_, index) => {
        const kind = pick(kinds, index + Math.floor(Math.random() * kinds.length));
        const x = Math.random() * rect.width;
        const y = Math.random() * rect.height;
        return {
          x,
          y,
          baseX: x,
          baseY: y,
          vx: (Math.random() - 0.5) * 0.16,
          vy: -0.05 - Math.random() * 0.16,
          size: kind === 'arc' ? 18 + Math.random() * 34 : kind === 'sparkle' ? 5 + Math.random() * 8 : 4 + Math.random() * 14,
          opacity: 0.18 + Math.random() * 0.37,
          rotate: Math.random() * Math.PI * 2,
          vr: (Math.random() - 0.5) * 0.012,
          kind,
          color: pick(particlePalette[kind], index),
        };
      });
    };

    const drawSparkle = (p: SoftParticle) => {
      ctx.save();
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rotate);
      ctx.fillStyle = p.color;
      ctx.beginPath();
      for (let i = 0; i < 8; i += 1) {
        const radius = i % 2 === 0 ? p.size : p.size * 0.32;
        const angle = (Math.PI * 2 * i) / 8;
        ctx.lineTo(Math.cos(angle) * radius, Math.sin(angle) * radius);
      }
      ctx.closePath();
      ctx.fill();
      ctx.restore();
    };

    const drawPetal = (p: SoftParticle) => {
      ctx.save();
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rotate);
      ctx.fillStyle = p.color;
      ctx.beginPath();
      ctx.ellipse(0, 0, p.size * 0.78, p.size * 0.36, 0.7, 0, Math.PI * 2);
      ctx.fill();
      ctx.restore();
    };

    const drawCoin = (p: SoftParticle) => {
      ctx.save();
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rotate);
      ctx.fillStyle = p.color;
      ctx.strokeStyle = 'rgba(255,255,255,0.52)';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.ellipse(0, 0, p.size * 0.52, p.size * 0.42, 0, 0, Math.PI * 2);
      ctx.fill();
      ctx.stroke();
      ctx.fillStyle = 'rgba(120, 96, 45, 0.26)';
      ctx.font = `${Math.max(8, p.size * 0.62)}px serif`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillText('¥', 0, 0.5);
      ctx.restore();
    };

    const drawArc = (p: SoftParticle) => {
      ctx.save();
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rotate);
      ctx.strokeStyle = p.color;
      ctx.lineWidth = 1.1;
      ctx.lineCap = 'round';
      ctx.beginPath();
      ctx.arc(0, 0, p.size, Math.PI * 0.08, Math.PI * 0.7);
      ctx.stroke();
      ctx.restore();
    };

    const draw = () => {
      const rect = canvas.getBoundingClientRect();
      frame += 1;
      ctx.clearRect(0, 0, rect.width, rect.height);

      for (const p of particles) {
        if (!reduceMotion) {
          p.baseX += p.vx;
          p.baseY += p.vy;
          p.rotate += p.vr;
          if (p.baseY < -30) p.baseY = rect.height + 30;
          if (p.baseX < -30) p.baseX = rect.width + 30;
          if (p.baseX > rect.width + 30) p.baseX = -30;
        }
        const waveX = reduceMotion ? 0 : Math.sin(frame * 0.008 + p.baseY * 0.01) * 4;
        const waveY = reduceMotion ? 0 : Math.cos(frame * 0.007 + p.baseX * 0.01) * 3;
        const parallaxX = pointer.active ? ((pointer.x / Math.max(rect.width, 1)) - 0.5) * 12 : 0;
        const parallaxY = pointer.active ? ((pointer.y / Math.max(rect.height, 1)) - 0.5) * 10 : 0;
        p.x = p.baseX + waveX + parallaxX;
        p.y = p.baseY + waveY + parallaxY;

        ctx.globalAlpha = p.opacity;
        if (p.kind === 'dot') {
          ctx.fillStyle = p.color;
          ctx.beginPath();
          ctx.arc(p.x, p.y, p.size * 0.42, 0, Math.PI * 2);
          ctx.fill();
        } else if (p.kind === 'sparkle') {
          drawSparkle(p);
        } else if (p.kind === 'petal') {
          drawPetal(p);
        } else if (p.kind === 'coin') {
          drawCoin(p);
        } else {
          drawArc(p);
        }
      }
      ctx.globalAlpha = 1;
      if (!reduceMotion) animation = requestAnimationFrame(draw);
    };

    const move = (event: PointerEvent) => {
      const rect = canvas.getBoundingClientRect();
      pointer.x = event.clientX - rect.left;
      pointer.y = event.clientY - rect.top;
      pointer.active = true;
    };
    const leave = () => {
      pointer.active = false;
    };
    const resize = () => {
      build();
      if (reduceMotion) draw();
    };

    build();
    draw();
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerleave', leave);
    window.addEventListener('blur', leave);
    window.addEventListener('resize', resize);
    return () => {
      cancelAnimationFrame(animation);
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerleave', leave);
      window.removeEventListener('blur', leave);
      window.removeEventListener('resize', resize);
    };
  }, []);

  return <canvas ref={canvasRef} className="softParticleCanvas" aria-hidden="true" />;
}

const disturbanceTexts = ['Bid +¥50', '+¥100', '¥520', '✦', '✿', '¥', '+15s', '成交'];

function pickDisturbanceText(index: number) {
  return disturbanceTexts[index % disturbanceTexts.length];
}

function MouseDisturbanceLayer() {
  const [bursts, setBursts] = useState<PointerDisturbance[]>([]);
  const seedRef = useRef(1);
  const lastRef = useRef(0);
  const quietTimerRef = useRef<number | null>(null);

  useEffect(() => {
    const page = document.querySelector<HTMLElement>('.softHomePage');
    const move = (event: PointerEvent) => {
      const x = event.clientX;
      const y = event.clientY;
      page?.style.setProperty('--mouse-x', `${x}px`);
      page?.style.setProperty('--mouse-y', `${y}px`);
      page?.classList.add('isPointerDisturbing');
      if (quietTimerRef.current) window.clearTimeout(quietTimerRef.current);
      quietTimerRef.current = window.setTimeout(() => {
        page?.classList.remove('isPointerDisturbing');
      }, 520);

      const now = performance.now();
      if (now - lastRef.current < 90) return;
      lastRef.current = now;
      const id = seedRef.current;
      seedRef.current += 1;
      setBursts((current) => [
        ...current.slice(-18),
        {
          id,
          x: x + (Math.random() - 0.5) * 34,
          y: y + (Math.random() - 0.5) * 34,
          text: pickDisturbanceText(id + Math.floor(Math.random() * disturbanceTexts.length)),
          rotate: -18 + Math.random() * 36,
          size: 0.82 + Math.random() * 0.45,
        },
      ]);
    };
    const leave = () => page?.classList.remove('isPointerDisturbing');
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerleave', leave);
    window.addEventListener('blur', leave);
    return () => {
      if (quietTimerRef.current) window.clearTimeout(quietTimerRef.current);
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerleave', leave);
      window.removeEventListener('blur', leave);
    };
  }, []);

  return (
    <div className="mouseDisturbanceLayer" aria-hidden="true">
      {bursts.map((burst) => (
        <span
          key={burst.id}
          className="mouseDisturbanceBurst"
          style={{
            left: burst.x,
            top: burst.y,
            rotate: `${burst.rotate}deg`,
            scale: burst.size,
          }}
        >
          {burst.text}
        </span>
      ))}
    </div>
  );
}

const particleTitlePalette = ['#64D8F4', '#FFFFFF', '#F05A9A', '#BDAAF6', '#7EF2E5'];

function ParticleTitleCanvas({ text }: { text: string }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    let frame = 0;
    let animation = 0;
    let particles: TextParticle[] = [];
    const pointer = { x: -9999, y: -9999, active: false };
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    const build = () => {
      const rect = canvas.getBoundingClientRect();
      const parentRect = canvas.parentElement?.getBoundingClientRect();
      const width = Math.max(320, Math.floor(parentRect?.width || rect.width));
      const height = Math.max(120, Math.floor(parentRect?.height || rect.height));
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.floor(width * dpr);
      canvas.height = Math.floor(height * dpr);
      canvas.style.width = `${width}px`;
      canvas.style.height = `${height}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

      const offscreen = document.createElement('canvas');
      offscreen.width = width;
      offscreen.height = height;
      const offCtx = offscreen.getContext('2d');
      if (!offCtx) return;
      offCtx.clearRect(0, 0, width, height);
      offCtx.textAlign = 'center';
      offCtx.textBaseline = 'middle';
      offCtx.font = `500 ${Math.min(170, Math.max(112, width * 0.18))}px Georgia, 'Times New Roman', serif`;
      offCtx.fillStyle = '#ffffff';
      offCtx.fillText(text, width / 2, height / 2 + 2);

      const data = offCtx.getImageData(0, 0, width, height).data;
      const next: TextParticle[] = [];
      const step = width > 900 ? 3 : 4;
      for (let y = 0; y < height; y += step) {
        for (let x = 0; x < width; x += step) {
          const alpha = data[(y * width + x) * 4 + 3];
          if (alpha > 58 && (x + y) % (step * 2) < step + 2) {
            const color = particleTitlePalette[(x + y + next.length) % particleTitlePalette.length];
            next.push({
              x: width / 2 + (Math.random() - 0.5) * width * 0.9,
              y: height / 2 + (Math.random() - 0.5) * height * 0.9,
              tx: x,
              ty: y,
              vx: 0,
              vy: 0,
              size: 1.35 + Math.random() * 1.35,
              color,
            });
          }
        }
      }
      particles = next.slice(0, 3600);
    };

    const draw = () => {
      const rect = canvas.getBoundingClientRect();
      const width = rect.width;
      const height = rect.height;
      frame += 1;
      ctx.clearRect(0, 0, width, height);

      for (const p of particles) {
        const dx = p.tx - p.x;
        const dy = p.ty - p.y;
        p.vx += dx * (reduceMotion ? 0.18 : 0.028);
        p.vy += dy * (reduceMotion ? 0.18 : 0.028);

        if (pointer.active) {
          const mx = p.x - pointer.x;
          const my = p.y - pointer.y;
          const distanceSq = mx * mx + my * my;
          const radius = 92;
          if (distanceSq < radius * radius) {
            const distance = Math.sqrt(distanceSq) || 1;
            const force = (1 - distance / radius) * 9.5;
            p.vx += (mx / distance) * force;
            p.vy += (my / distance) * force;
          }
        }

        const wave = reduceMotion ? 0 : Math.sin(frame * 0.035 + p.tx * 0.025) * 0.16;
        p.vx += wave;
        p.vx *= reduceMotion ? 0.36 : 0.84;
        p.vy *= reduceMotion ? 0.36 : 0.84;
        p.x += p.vx;
        p.y += p.vy;

        ctx.globalAlpha = 0.86;
        ctx.fillStyle = p.color;
        ctx.shadowColor = p.color;
        ctx.shadowBlur = pointer.active ? 9 : 5;
        ctx.beginPath();
        ctx.arc(p.x, p.y, p.size, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.globalAlpha = 1;
      ctx.shadowBlur = 0;
      animation = requestAnimationFrame(draw);
    };

    const move = (event: PointerEvent) => {
      const rect = canvas.getBoundingClientRect();
      pointer.x = event.clientX - rect.left;
      pointer.y = event.clientY - rect.top;
      pointer.active = pointer.x >= 0 && pointer.y >= 0 && pointer.x <= rect.width && pointer.y <= rect.height;
    };
    const leave = () => { pointer.active = false; };
    const resize = () => build();

    build();
    draw();
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerleave', leave);
    window.addEventListener('blur', leave);
    window.addEventListener('resize', resize);
    return () => {
      cancelAnimationFrame(animation);
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerleave', leave);
      window.removeEventListener('blur', leave);
      window.removeEventListener('resize', resize);
    };
  }, [text]);

  return <canvas ref={canvasRef} className="particleTitleCanvas" aria-hidden="true" />;
}

function HandDrawnOrbit() {
  return (
    <svg className="handOrbitLayer" viewBox="0 0 1200 720" aria-hidden="true">
      <path className="orbitStroke orbitStrokeA" d="M112 452 C 312 242, 706 186, 1088 312" />
      <path className="orbitStroke orbitStrokeB" d="M96 526 C 368 644, 802 596, 1118 394" />
      <path className="orbitStroke orbitStrokeC" d="M214 210 C 424 112, 776 116, 1014 230" />
      <path className="orbitStroke orbitStrokeD" d="M176 598 C 416 398, 754 304, 1054 500" />
    </svg>
  );
}

function FloatingAuctionCard() {
  const [secondsLeft, setSecondsLeft] = useState(28);

  useEffect(() => {
    const timer = window.setInterval(() => {
      setSecondsLeft((current) => (current <= 0 ? 28 : current - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, []);

  const countdown = `00 : 00 : ${String(secondsLeft).padStart(2, '0')}`;

  return (
    <div className="floatBizCard liveAuctionFloat">
      <strong>LIVE AUCTION <em>LIVE</em></strong>
      <span>Current Bid</span>
      <b>¥ 520</b>
      <small className="auctionCountdown" aria-live="polite">{countdown}</small>
    </div>
  );
}

const bidBubbleSets = [
  ['Bid +¥50', 'Bid +¥100', 'Bid +¥200'],
  ['张三 +¥80', '李四 +¥120', '王五 +¥260'],
  ['Bid +¥150', '领先 ¥1,580', '追价 +¥300'],
  ['主播提醒', '最后 10 秒', '封顶成交'],
];

function BidBubbles() {
  const [round, setRound] = useState(0);

  useEffect(() => {
    const timer = window.setInterval(() => {
      setRound((current) => (current + 1) % bidBubbleSets.length);
    }, 1800);
    return () => window.clearInterval(timer);
  }, []);

  const bids = bidBubbleSets[round];

  return (
    <div className="bidBubbleStack" data-round={round}>
      {bids.map((bid, index) => <span key={`${round}-${bid}`} style={{ '--bubble-index': index } as React.CSSProperties}>{bid}</span>)}
    </div>
  );
}

type MiniBidBurst = {
  id: number;
  left: number;
  top: number;
  bids: string[];
  rotate: number;
};

const miniBidSafeZones = leftRightBlankZones;

const miniBidSets = [
  ['Bid +¥150', '领先 ¥1,580', '追价 +¥300'],
  ['Bid +¥80', '领先 ¥920', '追价 +¥120'],
  ['Bid +¥260', '领先 ¥2,180', '追价 +¥520'],
  ['张三 +¥100', '李四 +¥200', '王五 +¥300'],
];

const reactionSafeZones = leftRightBlankZones;

function createMiniBidBurst(id: number, existing: PlacedPoint[] = []): MiniBidBurst {
  const point = pickNonOverlappingPoint(miniBidSafeZones, existing, 18);
  return {
    id,
    left: point.left,
    top: point.top,
    bids: miniBidSets[id % miniBidSets.length],
    rotate: -8 + Math.random() * 16,
  };
}

function createMiniBidLayout(seed: number, count = 3) {
  const layout: MiniBidBurst[] = [];
  for (let index = 0; index < count; index += 1) {
    const group = createMiniBidBurst(seed + index, layout);
    layout.push({ ...group, id: index + 1 });
  }
  return layout;
}

function RandomMiniBidField() {
  const [groups, setGroups] = useState<MiniBidBurst[]>(() => [
    { id: 1, left: 1.5, top: 28, bids: miniBidSets[0], rotate: -3 },
    { id: 2, left: 89, top: 30, bids: miniBidSets[1], rotate: 4 },
    { id: 3, left: 2.5, top: 64, bids: miniBidSets[2], rotate: 2 },
  ]);

  useEffect(() => {
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduceMotion) return;
    let seed = 6;
    const timer = window.setInterval(() => {
      setGroups(createMiniBidLayout(seed, 3));
      seed += 3;
    }, 3600);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <div className="randomMiniBidField" aria-hidden="true">
      {groups.map((group) => (
        <div
          key={group.id}
          className="miniBidBurstStack"
          style={{ left: `${group.left}%`, top: `${group.top}%`, rotate: `${group.rotate}deg` }}
        >
          {group.bids.map((bid, index) => <span key={`${group.id}-${index}`} style={{ '--bubble-index': index } as React.CSSProperties}>{bid}</span>)}
        </div>
      ))}
    </div>
  );
}

function PinkShoppingBagIllustration() {
  return (
    <svg className="handSvg handBagSvg" viewBox="0 0 120 120" aria-hidden="true">
      <defs>
        <linearGradient id="bagBody" x1="18" y1="14" x2="96" y2="108" gradientUnits="userSpaceOnUse">
          <stop stopColor="#FFD9E7" />
          <stop offset="0.55" stopColor="#F7A8C8" />
          <stop offset="1" stopColor="#F4C6D8" />
        </linearGradient>
      </defs>
      <path className="svgSoftShadow" d="M28 47c5-10 54-14 64-2 5 7 7 39 1 48-7 10-57 12-66 1-7-9-5-35 1-47Z" />
      <path d="M27 44c7-11 55-13 66-1 6 7 7 41 0 50-8 10-57 12-67 0-7-9-6-39 1-49Z" fill="url(#bagBody)" stroke="rgba(255,255,255,.74)" strokeWidth="2.2" />
      <path d="M43 45c1-16 8-24 19-24 12 0 19 8 20 24" fill="none" stroke="rgba(114,94,137,.35)" strokeWidth="5" strokeLinecap="round" />
      <path d="M42 63c10 8 31 8 40 0" fill="none" stroke="rgba(255,255,255,.6)" strokeWidth="2.5" strokeLinecap="round" />
      <circle cx="41" cy="55" r="3" fill="rgba(114,94,137,.25)" />
      <circle cx="84" cy="55" r="3" fill="rgba(114,94,137,.25)" />
    </svg>
  );
}

function AuctionGavelIllustration() {
  return (
    <svg className="handSvg handGavelSvg" viewBox="0 0 180 160" aria-hidden="true">
      <g fill="none" strokeLinecap="round" strokeLinejoin="round">
        <path className="svgSoftShadow" d="M75 72l28-28 39 39-28 28-39-39Z" />
        <path d="M72 69l29-29 41 41-29 29-41-41Z" fill="rgba(245,166,196,.32)" stroke="rgba(193,167,248,.62)" strokeWidth="5" />
        <path d="M46 93l24-24 39 39-24 24-39-39Z" fill="rgba(255,255,255,.2)" stroke="rgba(193,167,248,.48)" strokeWidth="4" />
        <path d="M94 103l49 49" stroke="rgba(193,167,248,.58)" strokeWidth="11" />
        <path d="M131 140l25 25" stroke="rgba(245,166,196,.42)" strokeWidth="15" />
        <path d="M28 134c20 9 50 10 74 2" stroke="rgba(255,255,255,.42)" strokeWidth="3" />
      </g>
    </svg>
  );
}

function GiftStickerIllustration() {
  return (
    <svg className="handSvg handGiftSvg" viewBox="0 0 90 90" aria-hidden="true">
      <path className="svgSoftShadow" d="M20 36h50v34H20z" />
      <rect x="19" y="35" width="52" height="36" rx="12" fill="rgba(255,255,255,.42)" stroke="rgba(255,255,255,.72)" strokeWidth="2" />
      <path d="M16 31h58v16H16z" fill="rgba(246,198,106,.44)" stroke="rgba(255,255,255,.72)" strokeWidth="2" />
      <path d="M45 29v43" stroke="rgba(245,166,196,.72)" strokeWidth="5" strokeLinecap="round" />
      <path d="M31 29c-11-12 9-21 14 0M59 29c11-12-9-21-14 0" fill="none" stroke="rgba(245,166,196,.62)" strokeWidth="5" strokeLinecap="round" />
    </svg>
  );
}

function HandDrawnProductIcon({ kind }: { kind: 'art' | 'bag' | 'necklace' | 'ring' | 'perfume' }) {
  if (kind === 'bag') return <PinkShoppingBagIllustration />;
  if (kind === 'necklace') return (
    <svg className="handSvg handJewelrySvg" viewBox="0 0 100 100" aria-hidden="true">
      <path d="M24 24c6 30 17 45 28 45s22-15 28-45" fill="none" stroke="rgba(189,170,246,.58)" strokeWidth="4" strokeLinecap="round" />
      <circle cx="52" cy="70" r="11" fill="rgba(246,198,106,.45)" stroke="rgba(255,255,255,.75)" strokeWidth="2" />
      <path d="M47 68l5-8 5 8-5 8-5-8Z" fill="rgba(255,255,255,.58)" stroke="rgba(245,166,196,.42)" strokeWidth="1.6" />
      <circle cx="30" cy="30" r="3" fill="rgba(245,166,196,.38)" />
      <circle cx="74" cy="30" r="3" fill="rgba(245,166,196,.38)" />
    </svg>
  );
  if (kind === 'ring') return (
    <svg className="handSvg handRingSvg" viewBox="0 0 100 100" aria-hidden="true">
      <circle cx="50" cy="58" r="23" fill="rgba(255,255,255,.22)" stroke="rgba(246,198,106,.58)" strokeWidth="5" />
      <path d="M38 30l12-13 12 13-12 12-12-12Z" fill="rgba(189,170,246,.42)" stroke="rgba(255,255,255,.76)" strokeWidth="2" />
      <path d="M31 66c9 7 29 8 39 0" fill="none" stroke="rgba(255,255,255,.48)" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
  if (kind === 'perfume') return (
    <svg className="handSvg handPerfumeSvg" viewBox="0 0 100 100" aria-hidden="true">
      <rect x="39" y="17" width="22" height="14" rx="5" fill="rgba(189,170,246,.36)" stroke="rgba(255,255,255,.7)" strokeWidth="2" />
      <path d="M29 40c5-11 37-11 42 0 4 9 4 31-2 38-7 8-31 8-38 0-6-7-6-29-2-38Z" fill="rgba(142,200,232,.28)" stroke="rgba(255,255,255,.74)" strokeWidth="2.4" />
      <path d="M39 55h22" stroke="rgba(245,166,196,.48)" strokeWidth="3" strokeLinecap="round" />
      <circle cx="50" cy="65" r="6" fill="rgba(246,198,106,.38)" />
    </svg>
  );
  return (
    <svg className="handSvg handArtSvg" viewBox="0 0 100 100" aria-hidden="true">
      <rect x="18" y="18" width="64" height="64" rx="16" fill="rgba(255,255,255,.36)" stroke="rgba(255,255,255,.72)" strokeWidth="2.4" />
      <path d="M30 62l16-18 12 12 8-10 8 16H30Z" fill="rgba(142,200,232,.42)" stroke="rgba(88,141,173,.42)" strokeWidth="2" strokeLinejoin="round" />
      <circle cx="62" cy="36" r="7" fill="rgba(246,198,106,.46)" />
      <path d="M26 26c10-5 36-7 48 0" fill="none" stroke="rgba(245,166,196,.34)" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

function SakuraStarFragments() {
  return (
    <div className="sakuraStarLayer" aria-hidden="true">
      {Array.from({ length: 18 }, (_, index) => <span key={index} className={`fragment fragment-${index % 6}`} />)}
    </div>
  );
}

function ScatterStickerField() {
  const stickers = [
    { cls: 'stickerLive', text: 'LIVE' },
    { cls: 'stickerPetal', text: '' },
  ];
  return (
    <div className="scatterStickerField" aria-hidden="true">
      {stickers.map((item) => <span key={item.cls} className={`scatterSticker ${item.cls}`}>{item.text}</span>)}
    </div>
  );
}

type ReactionBurst = {
  id: number;
  kind: 'gift' | 'heart';
  left: number;
  top: number;
  size: number;
  delay: number;
};

function RandomReactionField() {
  const [bursts, setBursts] = useState<ReactionBurst[]>(() => [
    { id: 1, kind: 'heart', left: 89, top: 38, size: 42, delay: 0 },
    { id: 2, kind: 'gift', left: 5, top: 18, size: 44, delay: 0.4 },
  ]);

  useEffect(() => {
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduceMotion) return;
    let id = 3;
    const timer = window.setInterval(() => {
      setBursts((current) => {
        const visible = current.slice(-7);
        const point = pickNonOverlappingPoint(reactionSafeZones, visible, 12);
        const next: ReactionBurst = {
          id,
          kind: Math.random() > 0.52 ? 'heart' : 'gift',
          left: point.left,
          top: point.top,
          size: 30 + Math.random() * 24,
          delay: 0,
        };
        id += 1;
        return [...visible, next];
      });
    }, 950);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <div className="randomReactionField" aria-hidden="true">
      {bursts.map((burst) => (
        <span
          key={burst.id}
          className={`reactionBurst ${burst.kind === 'gift' ? 'isGiftBurst' : 'isHeartBurst'}`}
          style={{
            left: `${burst.left}%`,
            top: `${burst.top}%`,
            width: burst.size,
            height: burst.size,
            animationDelay: `${burst.delay}s`,
          }}
        >
          {burst.kind === 'gift' ? <GiftStickerIllustration /> : <Heart size={Math.round(burst.size * 0.56)} />}
        </span>
      ))}
    </div>
  );
}

type ProductBurst = {
  id: number;
  kind: 'bag' | 'necklace' | 'ring' | 'perfume';
  title: string;
  price: string;
  left: number;
  top: number;
  rotate: number;
  size: number;
};

const productBurstCatalog: Array<Pick<ProductBurst, 'kind' | 'title' | 'price'>> = [
  { kind: 'bag', title: 'Premium Bag', price: '¥299.00' },
  { kind: 'bag', title: 'Designer Bag', price: '¥499.00' },
  { kind: 'necklace', title: 'Pearl Necklace', price: '¥699.00' },
  { kind: 'ring', title: 'Crystal Ring', price: '¥399.00' },
  { kind: 'perfume', title: 'Dream Perfume', price: '¥269.00' },
];

function createProductBurst(id: number, existing: PlacedPoint[] = []): ProductBurst {
  const product = productBurstCatalog[id % productBurstCatalog.length];
  const point = pickNonOverlappingPoint(productSafeZones, existing, 20);
  return {
    id,
    ...product,
    left: point.left,
    top: point.top,
    rotate: -18 + Math.random() * 36,
    size: 82 + Math.random() * 14,
  };
}

function RandomProductField() {
  const [products, setProducts] = useState<ProductBurst[]>(() => [
    { id: 1, kind: 'perfume', title: 'Dream Perfume', price: '¥269.00', left: -2, top: 16, rotate: -14, size: 90 },
    { id: 2, kind: 'ring', title: 'Crystal Ring', price: '¥399.00', left: 92, top: 58, rotate: -10, size: 88 },
    { id: 3, kind: 'bag', title: 'Premium Bag', price: '¥299.00', left: -3, top: 58, rotate: 12, size: 92 },
    { id: 4, kind: 'necklace', title: 'Pearl Necklace', price: '¥699.00', left: 93, top: 16, rotate: 10, size: 86 },
  ]);

  useEffect(() => {
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduceMotion) return;
    let id = 5;
    const timer = window.setInterval(() => {
      setProducts((current) => {
        const visible = current.slice(-3);
        return [...visible, createProductBurst(id, visible)];
      });
      id += 1;
    }, 2200);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <div className="randomProductField" aria-hidden="true">
      {products.map((product) => (
        <article
          key={product.id}
          className="productBurstCard"
          style={{
            left: `${product.left}%`,
            top: `${product.top}%`,
            width: product.size,
            rotate: `${product.rotate}deg`,
          }}
        >
          <div className="productBurstThumb"><HandDrawnProductIcon kind={product.kind} /></div>
          <span>{product.title}</span>
          <b>{product.price}</b>
        </article>
      ))}
    </div>
  );
}

function RippleBidField() {
  const chips = [
    { cls: 'rippleBidA', kind: 'coin', label: '成交额', value: '¥ 1,250' },
    { cls: 'rippleBidB', kind: 'bid', label: 'BID', value: '+ ¥100' },
    { cls: 'rippleBidC', kind: 'bid', label: 'BID', value: '+ ¥260' },
    { cls: 'rippleBidD', kind: 'coin', label: '领先价', value: '¥ 1,580' },
    { cls: 'rippleBidE', kind: 'bid', label: '追价', value: '+ ¥520' },
    { cls: 'rippleBidF', kind: 'coin', label: '封顶价', value: '¥ 2,999' },
  ];

  return (
    <div className="rippleBidField" aria-hidden="true">
      {chips.map((chip) => (
        <span key={chip.cls} className={`rippleBidChip ${chip.cls} ${chip.kind === 'coin' ? 'isCoinChip' : 'isBidChip'}`}>
          <i>{chip.kind === 'coin' ? '¥' : 'BID'}</i>
          <b>{chip.label}</b>
          <strong>{chip.value}</strong>
        </span>
      ))}
    </div>
  );
}

const sideActivityItems = [
  { cls: 'sideActivityA', label: '自动延时', value: '+15s' },
  { cls: 'sideActivityB', label: '领先提醒', value: '¥1,580' },
  { cls: 'sideActivityC', label: '订单生成', value: '已成交' },
  { cls: 'sideActivityE', label: '封顶成交', value: '¥2,999' },
];

function SideActivityField() {
  return (
    <div className="sideActivityField" aria-hidden="true">
      {sideActivityItems.map((item) => (
        <span key={item.cls} className={`sideActivityChip ${item.cls}`}>
          <b>{item.label}</b>
          <strong>{item.value}</strong>
        </span>
      ))}
    </div>
  );
}

const featureItems = [
  { icon: <Gavel size={24} />, title: '直播拍卖', text: '实时竞价，公平透明' },
  { icon: <ShoppingBag size={24} />, title: '电商带货', text: '精选好物，轻松交易' },
  { icon: <BarChart3 size={24} />, title: '数据看板', text: '多维数据，洞察增长' },
  { icon: <ShieldCheck size={24} />, title: '安全便捷', text: '资金安全，操作便捷' },
];

const statItems = [
  { icon: <Users size={22} />, value: '50,000+', label: '活跃主播' },
  { icon: <Store size={22} />, value: '200,000+', label: '上架拍品' },
  { icon: <Heart size={22} />, value: '10,000,000+', label: '累计成交' },
];

const frozenLayoutSelectors = [
  '.liveAuctionFloat',
  '.bidBubbleStack',
  '.gavelSketch',
  '.scatterSticker',
  '.rippleBidChip',
].join(',');

const frozenLayoutStorageKey = 'liveauction.home.dragLayout.v1';

type FrozenLayoutItem = { left: number; top: number };
type FrozenLayoutMap = Record<string, FrozenLayoutItem>;

const defaultFrozenHomeLayout: FrozenLayoutMap = {
  liveAuctionFloat: { left: 2.2, top: 7.2 },
  bidBubbleStack: { left: 14.8, top: 8.8 },
  gavelSketch: { left: 42, top: 84.8 },
  stickerLive: { left: -59.23, top: -39.51 },
  stickerPetal: { left: -53.45, top: 113.77 },
  rippleBidA: { left: 9.5, top: 9.04 },
  rippleBidB: { left: -32.47, top: 26.86 },
  rippleBidC: { left: 126.11, top: 8.34 },
  rippleBidD: { left: -13.5, top: 63.35 },
  rippleBidE: { left: 101.09, top: 48.15 },
  rippleBidF: { left: 95.85, top: 89.83 },
};

function getFrozenLayoutKey(element: Element, index: number) {
  const preferred = [
    'liveAuctionFloat',
    'bidBubbleStack',
    'gavelSketch',
    'stickerLive',
    'stickerPetal',
    'rippleBidA',
    'rippleBidB',
    'rippleBidC',
    'rippleBidD',
    'rippleBidE',
    'rippleBidF',
  ];
  return preferred.find((className) => element.classList.contains(className)) ?? `drag-${index}`;
}

function readFrozenHomeLayout(): FrozenLayoutMap {
  const saved = readLocalJson<FrozenLayoutMap>(frozenLayoutStorageKey, defaultFrozenHomeLayout);
  return {
    ...saved,
    liveAuctionFloat: defaultFrozenHomeLayout.liveAuctionFloat,
    bidBubbleStack: defaultFrozenHomeLayout.bidBubbleStack,
  };
}

function applyFrozenHomePosition(element: HTMLElement, item: FrozenLayoutItem) {
  element.style.setProperty('left', `${item.left}%`, 'important');
  element.style.setProperty('top', `${item.top}%`, 'important');
  element.style.setProperty('right', 'auto', 'important');
  element.style.setProperty('bottom', 'auto', 'important');
  element.style.setProperty('transform', 'none', 'important');
  element.style.setProperty('translate', 'none', 'important');
  element.style.setProperty('position', 'absolute', 'important');
}

export function HomePage() {
  useEffect(() => {
    const stage = document.querySelector<HTMLElement>('.heroStage');
    if (!stage) return;

    const savedLayout = readFrozenHomeLayout();
    const elements = Array.from(stage.querySelectorAll<HTMLElement>(frozenLayoutSelectors));
    elements.forEach((element, index) => {
      const saved = savedLayout[getFrozenLayoutKey(element, index)];
      if (saved) applyFrozenHomePosition(element, saved);
    });
  }, []);

  return (
    <main className="marketingPage particleTheme softHomePage">
      <div className="softHomeBackdrop" aria-hidden="true" />
      <SoftParticleCanvas />
      <MouseDisturbanceLayer />
      <SakuraStarFragments />
      <HandDrawnOrbit />

      <section className="softHomeHero" aria-label="ByteDance LiveAuction 首页">
        <div className="heroStage">
          <FloatingAuctionCard />
          <BidBubbles />
          <RandomMiniBidField />
          <div className="gavelSketch" aria-hidden="true"><AuctionGavelIllustration /></div>
          <ScatterStickerField />
          <RippleBidField />
          <SideActivityField />
          <RandomReactionField />
          <RandomProductField />

          <div className="softHeroCard heroCard">
            <span className="outlineWord" aria-hidden="true">LiveAuction</span>
            <p className="softKicker softDisturbanceText" aria-label="ByteDance">
              {'ByteDance'.split('').map((letter, index) => <span key={`${letter}-${index}`} style={{ '--letter-index': index } as React.CSSProperties}>{letter}</span>)}
              <em>● LIVE</em>
            </p>
            <h1 className="softTitleDisturbance isParticleTitle" aria-label="LiveAuction">
              <ParticleTitleCanvas text="LiveAuction" />
            </h1>
            <p className="softSubtitle">连接主播与用户，开启高效直播拍卖与电商新体验</p>
            <div className="softFeatureGrid" aria-label="核心能力">
              {featureItems.map((item) => (
                <article key={item.title}>
                  <i>{item.icon}</i>
                  <strong>{item.title}</strong>
                  <span>{item.text}</span>
                </article>
              ))}
            </div>
            <div className="softHeroActions isSingleEntry" aria-label="后台入口">
              <a className="softLoginButton" href="/login?next=/host">
                进入后台 <span><ArrowRight size={16} /></span>
              </a>
            </div>
          </div>

          <div className="softStatsBar statsPill" aria-label="平台数据">
            {statItems.map((item, index) => (
              <div key={item.label}>
                <i>{item.icon}</i>
                <CountUpStatValue value={item.value} duration={2200 + index * 220} delay={260 + index * 120} />
                <span>{item.label}</span>
              </div>
            ))}
          </div>
        </div>
      </section>

    </main>
  );
}
