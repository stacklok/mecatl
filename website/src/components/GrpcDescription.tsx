import {useEffect, useId, useRef, useState} from 'react';
import type {ReactNode} from 'react';

type Props = {
  children: ReactNode;
  name: string;
};

export default function GrpcDescription({children, name}: Props) {
  const measureRef = useRef<HTMLSpanElement>(null);
  const contentId = useId();
  const [overflows, setOverflows] = useState(false);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    const measure = measureRef.current;
    if (!measure) return undefined;

    const update = () => {
      const range = document.createRange();
      range.selectNodeContents(measure);
      const lines = new Set(
        Array.from(range.getClientRects())
          .filter((rect) => rect.width > 0)
          .map((rect) => Math.round(rect.top)),
      );
      const isLong = lines.size > 3;
      setOverflows(isLong);
      if (!isLong) setExpanded(false);
    };

    update();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', update);
      return () => window.removeEventListener('resize', update);
    }

    const observer = new ResizeObserver(update);
    observer.observe(measure);
    return () => observer.disconnect();
  }, [children]);

  return (
    <span className="grpc-description">
      <span aria-hidden="true" className="grpc-description-measure" ref={measureRef}>
        {children}
      </span>
      <span id={contentId} className={overflows && !expanded ? 'grpc-description-preview' : undefined}>
        {children}
      </span>
      {overflows && (
        <button
          aria-controls={contentId}
          aria-expanded={expanded}
          aria-label={`${expanded ? 'Read less' : 'Read more'} about ${name}`}
          className="grpc-description-toggle"
          onClick={() => setExpanded((value) => !value)}
          type="button"
        >
          {expanded ? 'Read less' : 'Read more'}
        </button>
      )}
    </span>
  );
}
