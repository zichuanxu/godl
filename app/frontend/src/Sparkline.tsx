/** Sparkline draws the recent total download speed; it stretches to its box. */
export default function Sparkline({ values }: { values: number[] }) {
  const width = 100;
  const height = 32;
  const max = Math.max(1, ...values);
  const step = values.length > 1 ? width / (values.length - 1) : width;
  const points = values.map((v, i) => `${(i * step).toFixed(2)},${(height - 1 - (v / max) * (height - 4)).toFixed(2)}`).join(" ");
  return (
    <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id="spark-fill" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor="var(--accent)" stopOpacity="0.28" />
          <stop offset="1" stopColor="var(--accent)" stopOpacity="0" />
        </linearGradient>
      </defs>
      {values.length > 1 && <polygon points={`0,${height} ${points} ${width},${height}`} fill="url(#spark-fill)" />}
      {values.length > 1 && <polyline points={points} className="line" vectorEffect="non-scaling-stroke" />}
    </svg>
  );
}
