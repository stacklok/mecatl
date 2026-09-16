import {useEffect, useRef} from 'react';
import type {RefObject} from 'react';

/**
 * Entrance reveals. Stamps `data-js` on the root (which is the only thing that
 * enables the hidden state in CSS) and `data-revealed` on each `[data-reveal]`
 * descendant when it enters the viewport. Anything already on screen at mount
 * is revealed immediately so above-the-fold content never blinks.
 */
export function useRevealRoot<T extends HTMLElement>(): RefObject<T | null> {
  const ref = useRef<T | null>(null);
  useEffect(() => {
    const root = ref.current;
    if (!root) return undefined;
    const els = Array.from(root.querySelectorAll<HTMLElement>('[data-reveal]'));
    if (typeof IntersectionObserver === 'undefined') {
      els.forEach((el) => el.setAttribute('data-revealed', ''));
      return undefined;
    }
    const vh = window.innerHeight;
    const pending: HTMLElement[] = [];
    els.forEach((el) => {
      if (el.getBoundingClientRect().top < vh) el.setAttribute('data-revealed', '');
      else pending.push(el);
    });
    root.setAttribute('data-js', '');
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            (e.target as HTMLElement).setAttribute('data-revealed', '');
            io.unobserve(e.target);
          }
        }
      },
      {rootMargin: '0px 0px -8% 0px', threshold: 0.05},
    );
    pending.forEach((el) => io.observe(el));
    return () => {
      io.disconnect();
      root.removeAttribute('data-js');
    };
  }, []);
  return ref;
}

/**
 * Typographic parallax.
 *
 * Every `[data-plx="<speed>"]` layer is translated on the Y axis relative to the
 * centre of its closest `[data-plx-section]`:
 *
 *   y = (viewportCentre − sectionCentre) × speed
 *
 * so layers stay close to their own content and are at rest when the section
 * is centred. Positive speeds lag behind the scroll (read as "further back");
 * negative speeds run ahead of it (read as "closer").
 *
 * One passive scroll listener schedules one rAF; the frame reads scrollY once
 * and writes translate3d to the layers of sections near the viewport only
 * (IntersectionObserver, ±50% margin). Section bounds are measured on mount,
 * resize, font load and page load, and refreshed from the observer entries.
 * The loop pauses while the tab is hidden and is disabled entirely under
 * prefers-reduced-motion or below 768px — layers are then reset to rest.
 */
export function useParallax<T extends HTMLElement>(): RefObject<T | null> {
  const ref = useRef<T | null>(null);
  useEffect(() => {
    const root = ref.current;
    if (
      !root ||
      typeof window === 'undefined' ||
      typeof window.matchMedia !== 'function' ||
      typeof IntersectionObserver === 'undefined'
    ) {
      return undefined;
    }

    type Layer = {el: HTMLElement; speed: number};
    type Section = {el: HTMLElement; layers: Layer[]; top: number; height: number; active: boolean};

    const sectionEls = Array.from(root.querySelectorAll<HTMLElement>('[data-plx-section]'));
    const byEl = new Map<HTMLElement, Section>();
    sectionEls.forEach((el) => byEl.set(el, {el, layers: [], top: 0, height: 0, active: false}));
    root.querySelectorAll<HTMLElement>('[data-plx]').forEach((el) => {
      const speed = parseFloat(el.dataset.plx ?? '0');
      const host = el.closest<HTMLElement>('[data-plx-section]');
      const section = host ? byEl.get(host) : undefined;
      if (section && speed !== 0 && !Number.isNaN(speed)) section.layers.push({el, speed});
    });
    const sections = Array.from(byEl.values()).filter((s) => s.layers.length > 0);
    if (sections.length === 0) return undefined;

    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
    const wide = window.matchMedia('(min-width: 768px)');

    let running = false;
    let frame = 0;
    let vh = window.innerHeight;
    let io: IntersectionObserver | null = null;

    const measure = () => {
      vh = window.innerHeight;
      const sy = window.scrollY;
      for (const s of sections) {
        const r = s.el.getBoundingClientRect();
        s.top = r.top + sy;
        s.height = r.height;
      }
    };

    const paint = () => {
      frame = 0;
      const sy = window.scrollY;
      const mid = vh / 2;
      for (const s of sections) {
        if (!s.active) continue;
        const p = mid - (s.top - sy + s.height / 2);
        for (const l of s.layers) {
          l.el.style.transform = `translate3d(0,${(p * l.speed).toFixed(1)}px,0)`;
        }
      }
    };

    const schedule = () => {
      if (!frame && document.visibilityState === 'visible') frame = window.requestAnimationFrame(paint);
    };
    const onResize = () => {
      measure();
      schedule();
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') {
        if (frame) window.cancelAnimationFrame(frame);
        frame = 0;
      } else {
        onResize();
      }
    };

    const start = () => {
      if (running) return;
      running = true;
      measure();
      io = new IntersectionObserver(
        (entries) => {
          const sy = window.scrollY;
          for (const e of entries) {
            const s = byEl.get(e.target as HTMLElement);
            if (!s) continue;
            s.active = e.isIntersecting;
            if (e.isIntersecting) {
              s.top = e.boundingClientRect.top + sy;
              s.height = e.boundingClientRect.height;
            }
          }
          schedule();
        },
        {rootMargin: '50% 0px 50% 0px', threshold: 0},
      );
      sections.forEach((s) => io?.observe(s.el));
      window.addEventListener('scroll', schedule, {passive: true});
      window.addEventListener('resize', onResize);
      document.addEventListener('visibilitychange', onVisibility);
      root.setAttribute('data-plx-on', '');
      schedule();
    };

    const stop = () => {
      if (!running) return;
      running = false;
      io?.disconnect();
      io = null;
      window.removeEventListener('scroll', schedule);
      window.removeEventListener('resize', onResize);
      document.removeEventListener('visibilitychange', onVisibility);
      if (frame) window.cancelAnimationFrame(frame);
      frame = 0;
      for (const s of sections) {
        s.active = false;
        for (const l of s.layers) l.el.style.transform = '';
      }
      root.removeAttribute('data-plx-on');
    };

    const sync = () => {
      if (!reduce.matches && wide.matches) start();
      else stop();
    };
    sync();
    reduce.addEventListener('change', sync);
    wide.addEventListener('change', sync);

    // Web fonts and images change section heights after first paint.
    const remeasure = () => {
      if (running) onResize();
    };
    if (document.fonts && document.fonts.ready) void document.fonts.ready.then(remeasure);
    window.addEventListener('load', remeasure);
    const settle = window.setTimeout(remeasure, 1200);

    return () => {
      window.clearTimeout(settle);
      window.removeEventListener('load', remeasure);
      reduce.removeEventListener('change', sync);
      wide.removeEventListener('change', sync);
      stop();
    };
  }, []);
  return ref;
}
