/**
 * The mark of dex: two stacked cards, from the name (index cards). It holds one card behind the
 * other with the front edge in ink and the back one in the accent, so the mark carries the one hue
 * the rest of the interface keeps for the live state.
 */
export function DexMark({ size = 18 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 18 18"
      fill="none"
      aria-hidden
      focusable="false"
      className="shrink-0"
    >
      <rect
        x="4.75"
        y="2.75"
        width="10.5"
        height="10.5"
        rx="2.25"
        stroke="var(--dex-accent)"
        strokeWidth="1.4"
      />
      <rect
        x="2.75"
        y="4.75"
        width="10.5"
        height="10.5"
        rx="2.25"
        fill="var(--surface)"
        stroke="var(--ink)"
        strokeWidth="1.4"
      />
      <path d="M6.1 10h3.8" stroke="var(--ink)" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}
