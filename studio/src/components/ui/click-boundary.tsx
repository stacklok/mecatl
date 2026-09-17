export function ClickBoundary({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: ClickBoundary intentionally stops click propagation within a larger interactive surface
    <div
      className={className}
      role="presentation"
      onClick={(e) => e.stopPropagation()}
    >
      {children}
    </div>
  );
}
