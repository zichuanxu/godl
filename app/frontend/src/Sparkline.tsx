/** Sparkline draws the recent total download speed as a smooth curve that
 * stretches to its box. */
export default function Sparkline({ values }: { values: number[] }) {
  const width = 100;
  const height = 36;
  // The curve starts at the first real sample, so a fresh start is not a long
  // flat line.
  const seen = values.findIndex((v) => v > 0);
  const first = Math.max(0, seen - 1);
  if (seen < 0 || values.length - first < 2) return <svg className="sparkline" aria-hidden="true" />;
  const max = Math.max(1, ...values) * 1.15; // headroom above the peak
  const shown = values.slice(first);
  const step = width / (shown.length - 1); // what has been seen so far fills the width
  const pts = shown.map((v, i) => [i * step, height - 1 - (v / max) * (height - 2)]);
  // Catmull-Rom through the samples, as cubic Béziers.
  let line = `M${pts[0][0]},${pts[0][1]}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const [p0, p1, p2, p3] = [pts[i - 1] ?? pts[i], pts[i], pts[i + 1], pts[i + 2] ?? pts[i + 1]];
    const c1 = [p1[0] + (p2[0] - p0[0]) / 6, Math.min(height - 1, p1[1] + (p2[1] - p0[1]) / 6)];
    const c2 = [p2[0] - (p3[0] - p1[0]) / 6, Math.min(height - 1, p2[1] - (p3[1] - p1[1]) / 6)];
    line += ` C${c1[0].toFixed(2)},${c1[1].toFixed(2)} ${c2[0].toFixed(2)},${c2[1].toFixed(2)} ${p2[0].toFixed(2)},${p2[1].toFixed(2)}`;
  }
  return (
    <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id="spark-fill" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor="var(--accent)" stopOpacity="0.3" />
          <stop offset="1" stopColor="var(--accent)" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={`${line} L${width},${height} L${pts[0][0]},${height} Z`} fill="url(#spark-fill)" />
      <path d={line} className="line" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
