import { Plus } from "lucide-react";
import { useId, useState } from "react";

import type { FamilyGrowthPoint } from "../api.generated";
import { deleteRecord, type FamilyBaby, type FamilyGrowth, familyError, getRecord } from "./api";
import type { Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { bandShort, bandText, bandTone, decimal, shortDate } from "./format";
import { ItemMenu } from "./parts";

type Indicator = "height" | "weight" | "head";
type Measured = FamilyGrowth["measurements"][number];

const indicators: Array<{
  id: Indicator;
  label: string;
  unit: string;
  digits: number;
  value: (m: Measured) => number | undefined;
}> = [
  { id: "height", label: "身长", unit: "cm", digits: 1, value: (m) => m.heightCm },
  { id: "weight", label: "体重", unit: "kg", digits: 2, value: (m) => m.weightKg },
  { id: "head", label: "头围", unit: "cm", digits: 1, value: (m) => m.headCm },
];

// Growth is 成长: the latest of each measure, one measure's curve against
// WS/T 423—2022, and every measurement to correct. The channel reads the
// growth once for this board and the timeline; null means it failed.
export function Growth({
  baby,
  growth,
  onAdd,
  onEdit,
  onChanged,
}: {
  baby: FamilyBaby;
  growth?: FamilyGrowth | null;
  onAdd: () => void;
  onEdit: (draft: Draft) => void;
  onChanged: () => void;
}) {
  const family = useFamily();
  const [shown, setShown] = useState<Indicator>("height");

  const edit = async (recordId: string) => {
    try {
      onEdit({ kind: "measurement", record: await getRecord(recordId) });
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };
  const remove = async (recordId: string) => {
    const ok = await family.confirm({
      title: "删除这次测量？",
      message: "删除后 15 天内可以在时间线底部的“最近删除”中恢复。",
      confirm: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await deleteRecord(recordId);
      onChanged();
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };

  if (growth === null) return <p className={styles.fcFailure}>暂时读不到成长记录，请稍后再试。</p>;
  if (!growth) return <span className="loader" aria-label="正在读取成长记录" />;
  const ageNow = baby.age.totalMonths + baby.age.days / 30.44;
  const labelOf = (id: Indicator) =>
    id === "height" && ageNow >= 24 ? "身高" : indicators.find((item) => item.id === id)!.label;
  const indicator = indicators.find((item) => item.id === shown)!;
  const newestFirst = [...growth.measurements].reverse();
  return (
    <div className={styles.fcGrowth}>
      <header className={styles.fcBoardHead}>
        <div>
          <h2>成长曲线</h2>
        </div>
        <button type="button" className={styles.fcSecondary} onClick={onAdd}>
          <Plus size={14} /> 记录身高体重
        </button>
      </header>

      <div className={`${styles.fcFacts} ${styles.fcFactTabs}`} role="group" aria-label="指标">
        {indicators.map((item) => {
          const latest = growth.latest[item.id];
          return (
            <button
              key={item.id}
              type="button"
              aria-pressed={shown === item.id}
              className={shown === item.id ? styles.active : undefined}
              onClick={() => setShown(item.id)}
            >
              <span>{labelOf(item.id)}</span>
              {latest ? (
                <>
                  <strong>
                    {decimal(latest.value, item.digits)}
                    <small>{item.unit}</small>
                  </strong>
                  <em
                    className={bandTone(latest.lower, latest.upper) === "watch" ? styles.watch : undefined}
                    title={`${shortDate(latest.date)}测量，${bandText(latest.lower, latest.upper)}`}
                  >
                    {bandShort(latest) || "超出标准年龄"}
                  </em>
                </>
              ) : (
                <strong className={styles.fcFactEmpty}>—</strong>
              )}
            </button>
          );
        })}
      </div>

      <figure className={styles.fcChartFrame}>
        <Chart
          curve={growth.curves[indicator.id]}
          percentiles={growth.percentiles}
          points={growth.measurements
            .map((m) => ({ x: m.ageMonths, y: indicator.value(m) }))
            .filter((point): point is { x: number; y: number } => point.y !== undefined)}
          ageNow={ageNow}
          label={`${labelOf(indicator.id)}曲线，单位 ${indicator.unit}`}
        />
        <figcaption className={styles.fcLegend}>
          <span className={styles.outer}>{spread(growth.percentiles, 0)}</span>
          <span className={styles.inner}>
            {spread(growth.percentiles, Math.floor(growth.percentiles.length / 2) - 1)}
          </span>
          <span className={styles.median}>中位数</span>
          <span className={styles.baby}>{baby.nickname || baby.name}</span>
        </figcaption>
      </figure>

      <section className={styles.fcBoardSection}>
        <h3>全部测量</h3>
        {newestFirst.length === 0 ? (
          <p className={styles.fcNote}>还没有测量记录。</p>
        ) : (
          <table className={styles.fcTable}>
            <thead>
              <tr>
                <th>日期</th>
                <th>月龄</th>
                <th>{labelOf("height")}</th>
                <th>体重</th>
                <th>头围</th>
                <th aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {newestFirst.map((item) => (
                <tr key={item.recordId}>
                  <td>{shortDate(item.date)}</td>
                  <td>{decimal(item.ageMonths, 1)} 月</td>
                  <td>{item.heightCm !== undefined ? `${decimal(item.heightCm, 1)} cm` : "—"}</td>
                  <td>{item.weightKg !== undefined ? `${decimal(item.weightKg, 2)} kg` : "—"}</td>
                  <td>{item.headCm !== undefined ? `${decimal(item.headCm, 1)} cm` : "—"}</td>
                  <td className={styles.fcRowActions}>
                    <ItemMenu
                      label={`${shortDate(item.date)}的测量`}
                      items={[
                        { label: "修改", run: () => void edit(item.recordId) },
                        { label: "删除", danger: true, run: () => void remove(item.recordId) },
                      ]}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
      <p className={styles.fcSource}>
        参考 {growth.standard}（{growth.sex === "female" ? "女孩" : "男孩"}），仅供参考，以儿童保健医生的评估为准。
      </p>
    </div>
  );
}

// Chart draws the standard's percentile bands, the median and the baby's
// measurements over age in months.
function Chart({
  curve,
  percentiles,
  points,
  ageNow,
  label,
}: {
  curve: FamilyGrowthPoint[];
  percentiles: string[];
  points: Array<{ x: number; y: number }>;
  ageNow: number;
  label: string;
}) {
  const clip = `fc-chart-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const width = 640;
  const height = 280;
  const pad = { left: 40, right: 44, top: 14, bottom: 30 };
  const curveEnd = curve.at(-1)?.ageMonths ?? 0;
  const lastPoint = Math.max(0, ...points.map((point) => point.x));
  const xMax = Math.max(6, Math.ceil(Math.max(ageNow, lastPoint) + 2));
  const shown = curve.filter((point) => point.ageMonths <= xMax + 3);
  const values = [
    ...shown.flatMap((point) => [point.values[0], point.values.at(-1)!]),
    ...points.map((point) => point.y),
  ];
  const yMin = Math.floor(Math.min(...values) * 0.97);
  const yMax = Math.ceil(Math.max(...values) * 1.02);
  const x = (months: number) => pad.left + (months / xMax) * (width - pad.left - pad.right);
  const y = (value: number) =>
    height - pad.bottom - ((value - yMin) / (yMax - yMin || 1)) * (height - pad.top - pad.bottom);
  const line = (index: number) =>
    shown
      .map((point, i) => `${i ? "L" : "M"}${x(point.ageMonths).toFixed(1)} ${y(point.values[index]).toFixed(1)}`)
      .join(" ");
  const band = (low: number, high: number) =>
    `${line(low)} ${[...shown]
      .reverse()
      .map((point) => `L${x(point.ageMonths).toFixed(1)} ${y(point.values[high]).toFixed(1)}`)
      .join(" ")} Z`;
  const middle = Math.floor(percentiles.length / 2);
  const step = xMax <= 12 ? 1 : xMax <= 24 ? 3 : xMax <= 48 ? 6 : 12;
  const xTicks = Array.from({ length: Math.floor(xMax / step) + 1 }, (_, i) => i * step);
  const yStep = niceStep((yMax - yMin) / 5);
  const yTicks: number[] = [];
  for (let value = Math.ceil(yMin / yStep) * yStep; value <= yMax; value += yStep) yTicks.push(value);
  const sorted = [...points].sort((a, b) => a.x - b.x);
  // The percentiles are named where the curves leave the chart, or where the
  // standard ends.
  const edge = Math.min(xMax, curveEnd);
  const after = shown.findIndex((point) => point.ageMonths >= edge);
  const right = shown[after];
  const left = shown[Math.max(0, after - 1)];
  const share =
    right && right.ageMonths !== left.ageMonths ? (edge - left.ageMonths) / (right.ageMonths - left.ageMonths) : 0;
  const atEdge = right ? left.values.map((low, index) => low + share * (right.values[index] - low)) : undefined;
  // The outer band spans the first and last percentiles; the inner one, the
  // pair either side of the median.
  const bands = shown.length > 1 ? [0, Math.max(0, middle - 1)].filter((i, n, all) => all.indexOf(i) === n) : [];
  return (
    <svg className={styles.fcChart} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label}>
      <defs>
        <clipPath id={clip}>
          <rect x={pad.left} y={pad.top} width={width - pad.left - pad.right} height={height - pad.top - pad.bottom} />
        </clipPath>
      </defs>
      {yTicks.map((value) => (
        <g key={value}>
          <line className={styles.fcGrid} x1={pad.left} x2={width - pad.right} y1={y(value)} y2={y(value)} />
          <text className={styles.fcAxis} x={pad.left - 8} y={y(value) + 4} textAnchor="end">
            {value}
          </text>
        </g>
      ))}
      {xTicks.map((months) => (
        <text key={months} className={styles.fcAxis} x={x(months)} y={height - 9} textAnchor="middle">
          {months === 0 ? "出生" : months % 12 === 0 ? `${months / 12}岁` : `${months}`}
        </text>
      ))}
      <g clipPath={`url(#${clip})`}>
        {bands.map((i, n) => (
          <path
            key={i}
            className={`${styles.fcBandFill} ${n === 0 ? styles.outer : styles.inner}`}
            d={band(i, percentiles.length - 1 - i)}
          />
        ))}
        {shown.length > 1 && <path className={styles.fcMedian} d={line(middle)} />}
        {curveEnd < xMax && (
          <line className={styles.fcCurveEnd} x1={x(curveEnd)} x2={x(curveEnd)} y1={pad.top} y2={height - pad.bottom} />
        )}
        <line className={styles.fcToday} x1={x(ageNow)} x2={x(ageNow)} y1={pad.top} y2={height - pad.bottom} />
        {sorted.length > 1 && (
          <path
            className={styles.fcBabyLine}
            d={sorted.map((point, i) => `${i ? "L" : "M"}${x(point.x).toFixed(1)} ${y(point.y).toFixed(1)}`).join(" ")}
          />
        )}
        {sorted.map((point) => (
          <circle key={`${point.x}-${point.y}`} className={styles.fcBabyPoint} cx={x(point.x)} cy={y(point.y)} r={4} />
        ))}
      </g>
      {atEdge &&
        [0, middle, percentiles.length - 1].map((index) => (
          <text key={index} className={styles.fcAxisLabel} x={x(edge) + 6} y={y(atEdge[index]) + 4}>
            {percentiles[index]}
          </text>
        ))}
    </svg>
  );
}

// spread names the band between a percentile and its mirror: “P3—P97”.
function spread(percentiles: string[], index: number): string {
  return `${percentiles[index]}—${percentiles[percentiles.length - 1 - index]}`;
}

function niceStep(raw: number): number {
  const power = 10 ** Math.floor(Math.log10(raw || 1));
  const fraction = raw / power;
  return (fraction <= 1 ? 1 : fraction <= 2 ? 2 : fraction <= 5 ? 5 : 10) * power;
}
