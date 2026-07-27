import { useCallback, useEffect, useRef, useState } from 'react';

export function usePrefersReducedMotion() {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const query = window.matchMedia('(prefers-reduced-motion: reduce)');
    const update = () => setReduced(query.matches);
    update();
    query.addEventListener('change', update);
    return () => query.removeEventListener('change', update);
  }, []);
  return reduced;
}

export function useInView<T extends HTMLElement>(threshold = 0.2, rootMargin = '0px') {
  const ref = useRef<T | null>(null);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const node = ref.current;
    if (!node || !('IntersectionObserver' in window)) {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver(([entry]) => {
      if (entry.isIntersecting) setVisible(true);
    }, { rootMargin, threshold });
    observer.observe(node);
    return () => observer.disconnect();
  }, [rootMargin, threshold]);
  return [ref, visible] as const;
}

export function useScrollReveal<T extends HTMLElement>(triggerRatio = 0.72) {
  const [node, setNode] = useState<T | null>(null);
  const [visible, setVisible] = useState(false);
  const ref = useCallback((nextNode: T | null) => {
    setNode(nextNode);
    if (!nextNode) setVisible(false);
  }, []);

  useEffect(() => {
    if (!node || visible) return;

    const scrollTarget = findScrollParent(node);
    let animationFrame = 0;
    let revealed = false;

    const cleanup = () => {
      scrollTarget.removeEventListener('scroll', scheduleCheck);
      window.removeEventListener('resize', scheduleCheck);
      if (animationFrame) cancelAnimationFrame(animationFrame);
    };

    const revealIfReady = () => {
      if (revealed) return;
      const containerRect = scrollTarget === window ? { top: 0, height: window.innerHeight } : (scrollTarget as HTMLElement).getBoundingClientRect();
      const rect = node.getBoundingClientRect();
      const triggerY = containerRect.top + containerRect.height * triggerRatio;
      const visibleFloor = containerRect.top + containerRect.height * 0.08;
      if (rect.top <= triggerY && rect.bottom >= visibleFloor) {
        revealed = true;
        setVisible(true);
        cleanup();
      }
    };

    function scheduleCheck() {
      if (animationFrame) cancelAnimationFrame(animationFrame);
      animationFrame = requestAnimationFrame(revealIfReady);
    }

    scrollTarget.addEventListener('scroll', scheduleCheck, { passive: true });
    window.addEventListener('resize', scheduleCheck);
    return cleanup;
  }, [node, triggerRatio, visible]);

  return [ref, visible] as const;
}

function findScrollParent(node: HTMLElement): HTMLElement | Window {
  let parent = node.parentElement;
  while (parent && parent !== document.body) {
    const style = window.getComputedStyle(parent);
    const overflowY = `${style.overflowY} ${style.overflow}`;
    if (/(auto|scroll|overlay)/.test(overflowY) && parent.scrollHeight > parent.clientHeight) return parent;
    parent = parent.parentElement;
  }
  return window;
}
