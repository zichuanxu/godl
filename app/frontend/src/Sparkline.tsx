/** Sparkline draws the recent total download speed. */
export default function Sparkline({ values, width = 160, height = 28 }: { values: number[]; width?: number; height?: number }) {
  const max = Math.max(1, ...values);
  const step = values.length > 1 ? width / (values.length - 1) : width;
  const points = values.map((v, i) => `${(i * step).toFixed(1)},${(height - 1 - (v / max) * (height - 2)).toFixed(1)}`).join(" ");
  return (
    <svg className="sparkline" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-label="Download speed history">
      {values.length > 1 && <polyline points={`0,${height} ${points} ${width},${height}`} className="area" />}
      {values.length > 1 && <polyline points={points} className="line" />}
    </svg>
  );
}
