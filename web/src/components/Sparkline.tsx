// A small trend line with no axes: shape only, the numbers live beside it.
export function Sparkline({ values, width = 120, height = 28 }: { values: number[]; width?: number; height?: number }) {
  const max = Math.max(...values, 0);
  if (values.length < 2 || max <= 0) {
    return <svg className="spark" width={width} height={height} aria-hidden="true"><line x1="0" x2={width} y1={height - 1} y2={height - 1} className="spark-base" /></svg>;
  }
  const pad = 2;
  const x = (i: number) => (i / (values.length - 1)) * width;
  const y = (v: number) => height - pad - (v / max) * (height - 2 * pad);
  const line = values.map((v, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join("");
  return (
    <svg className="spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
      <path d={`${line}L${width},${height}L0,${height}Z`} className="spark-area" />
      <path d={line} className="spark-line" />
    </svg>
  );
}
