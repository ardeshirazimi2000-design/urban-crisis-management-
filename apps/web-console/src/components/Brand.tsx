/** Product identity: «همراه پاد — آماده در هر لحظه». The mark has a light-on-dark variant for the dark theme. */
export const BRAND_NAME = "همراه پاد";
export const BRAND_SLOGAN = "آماده در هر لحظه";

export function Brand({ size = "sm", compact = false }: { size?: "sm" | "lg"; compact?: boolean }) {
  return (
    <div className={`brand brand-${size}${compact ? " brand-compact" : ""}`} title={compact ? `${BRAND_NAME} — ${BRAND_SLOGAN}` : undefined}>
      <picture>
        <source srcSet="/brand/mark-dark.png" media="(prefers-color-scheme: dark)" />
        <img src="/brand/mark.png" alt="" className="brand-mark" />
      </picture>
      {!compact && <div className="brand-text">
        <span className="brand-name">{BRAND_NAME}</span>
        <span className="brand-slogan">{BRAND_SLOGAN}</span>
      </div>}
    </div>
  );
}
