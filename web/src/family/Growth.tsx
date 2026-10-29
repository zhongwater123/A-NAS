import { Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useId, useState } from "react";

import type { FamilyAssessment, FamilyGrowthPoint } from "../api.generated";
import { deleteRecord, type FamilyBaby, type FamilyGrowth, familyError, getGrowth, getRecord } from "./api";
import type { Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { bandText, bandTone, decimal, shortDate } from "./format";

type Indicator = "height" | "weight" | "head";

const indicators: Array<{
  id: Indicator;
  label: string;
  unit: string;
  digits: number;
  value: (m: Measured) => number | undefined;
}> = [
  { id: "height", label: "身长 / 身高", unit: "cm", digits: 1, value: (m) => m.heightCm },
  { id: "weight", label: "体重", unit: "kg", digits: 2, value: (m) => m.weightKg },
  { id: "head", label: "头围", unit: "cm", digits: 1, value: (m) => m.headCm },
];

type Measured = FamilyGrowth["measurements"][number];

// Growth is 成长记录: each measure on its curve against WS/T 423—2022, and
// every measurement in a list to correct.
export function Growth({
  baby,
  version,
  onAdd,
  onEdit,
  onChanged,
}: {
  baby: FamilyBaby;
  version: number;
  onAdd: () => void;
  onEdit: (draft: Draft) => void;
  onChanged: () => void;
}) {
  const family = useFamily();
  const [growth, setGrowth] = useState<FamilyGrowth>();
  const [failure, setFailure] = useState("");

  useEffect(() => {
    let live = true;
    getGrowth(baby.id).then(
      (next) => {
        if (live) setGrowth(next);
      },
      (caught: unknown) => {
        if (live) setFailure(familyError(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  const edit = async (recordId: string) => {
    try {
      const record = await getRecord(recordId);
      onEdit({ kind: "measurement", record });
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };
  const remove = async (recordId: string) => {
    const ok = await family.confirm({
      title: "删除这次测量？",
      message: "删除后 15 天内可以在动态的“最近删除”中恢复。",
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

  if (failure) return <p className={styles.fcFailure}>{failure}</p>;
  if (!growth) return <span className="loader" aria-label="正在读取成长记录" />;
  const ageNow = baby.age.totalMonths + baby.age.days / 30.4;
  const newestFirst = [...growth.measurements].reverse();
  return (
    <div className={styles.fcGrowth}>
      <div className={styles.fcBoardBar}>
        <span>
          参考 {growth.standard}，{growth.sex === "female" ? "女孩" : "男孩"}。仅供参考，以儿童保健医生的评估为准。
        </span>
        <button type="button" className={styles.fcPrimarySmall} onClick={onAdd}>
          <Plus size={14} /> 记录身高体重
        </button>
      </div>
      <div className={styles.fcCharts}>
        {indicators.map((indicator) => (
          <GrowthCard
            key={indicator.id}
            indicator={indicator}
            growth={growth}
            ageNow={ageNow}
            latest={growth.latest[indicator.id]}
          />
        ))}
      </div>
      <section className={styles.fcSection}>
        <h3>全部测量</h3>
        {newestFirst.length === 0 ? (
          <p className={styles.fcEmptyLine}>还没有测量记录。</p>
        ) : (
          <table className={styles.fcTable}>
            <thead>
              <tr>
                <th>日期</th>
                <th>月龄</th>
                <th>身长 / 身高</th>
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
                    <button
                      type="button"
                      className={styles.fcIconButton}
                      aria-label={`修改 ${item.date} 的测量`}
                      onClick={() => void edit(item.recordId)}
                    >
                      <Pencil size={14} />
                    </button>
                    <button
                      type="button"
                      className={styles.fcIconButton}
                      aria-label={`删除 ${item.date} 的测量`}
                      onClick={() => void remove(item.recordId)}
                    >
                      <Trash2 size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

function GrowthCard({
  indicator,
  growth,
  ageNow,
  latest,
}: {
  indicator: (typeof indicators)[number];
  growth: FamilyGrowth;
  ageNow: number;
  latest?: FamilyAssessment;
}) {
  const points = growth.measurements
    .map((m) => ({ x: m.ageMonths, y: indicator.value(m) }))
    .filter((point): point is { x: number; y: number } => point.y !== undefined);
  const label =
    indicator.id === "height" && ageNow >= 24 ? "身高" : indicator.id === "height" ? "身长" : indicator.label;
  return (
    <section className={styles.fcChartCard} aria-label={label}>
      <header>
        <h3>{label}</h3>
        {latest ? (
          <>
            <strong className={styles.fcChartValue}>
              {decimal(latest.value, indicator.digits)}
              <small>{indicator.unit}</small>
            </strong>
            <span className={`${styles.fcBand} ${styles[bandTone(latest.lower, latest.upper)] ?? ""}`}>
              {bandText(latest.lower, latest.upper)}
            </span>
            <small className={styles.fcChartDate}>{shortDate(latest.date)}测量</small>
          </>
        ) : (
          <span className={styles.fcChartEmpty}>还没有记录</span>
        )}
      </header>
      <Chart
        curve={growth.curves[indicator.id]}
        percentiles={growth.percentiles}
        points={points}
        ageNow={ageNow}
        unit={indicator.unit}
      />
    </section>
  );
}

// Chart draws the standard's percentile bands, the median and the baby's
// measurements over age in months.
function Chart({
  curve,
  percentiles,
  points,
  ageNow,
  unit,
}: {
  curve: FamilyGrowthPoint[];
  percentiles: string[];
  points: Array<{ x: number; y: number }>;
  ageNow: number;
  unit: string;
}) {
  const clip = `fc-chart-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const width = 360;
  const height = 210;
  const pad = { left: 36, right: 12, top: 10, bottom: 26 };
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
  const step = xMax <= 12 ? 2 : xMax <= 24 ? 3 : xMax <= 48 ? 6 : 12;
  const xTicks = Array.from({ length: Math.floor(xMax / step) + 1 }, (_, i) => i * step);
  const yStep = niceStep((yMax - yMin) / 4);
  const yTicks: number[] = [];
  for (let value = Math.ceil(yMin / yStep) * yStep; value <= yMax; value += yStep) yTicks.push(value);
  const sorted = [...points].sort((a, b) => a.x - b.x);
  return (
    <svg className={styles.fcChart} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`生长曲线，单位 ${unit}`}>
      <defs>
        <clipPath id={clip}>
          <rect x={pad.left} y={pad.top} width={width - pad.left - pad.right} height={height - pad.top - pad.bottom} />
        </clipPath>
      </defs>
      {yTicks.map((value) => (
        <g key={value}>
          <line className={styles.fcGrid} x1={pad.left} x2={width - pad.right} y1={y(value)} y2={y(value)} />
          <text className={styles.fcAxis} x={pad.left - 6} y={y(value) + 3.5} textAnchor="end">
            {value}
          </text>
        </g>
      ))}
      {xTicks.map((months) => (
        <text key={months} className={styles.fcAxis} x={x(months)} y={height - 8} textAnchor="middle">
          {months === 0 ? "出生" : months % 12 === 0 ? `${months / 12}岁` : `${months}月`}
        </text>
      ))}
      <g clipPath={`url(#${clip})`}>
        {shown.length > 1 &&
          Array.from({ length: middle }, (_, i) => (
            <path
              key={i}
              className={`${styles.fcBandFill} ${styles[`band${i}`] ?? ""}`}
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
          <circle
            key={`${point.x}-${point.y}`}
            className={styles.fcBabyPoint}
            cx={x(point.x)}
            cy={y(point.y)}
            r={3.6}
          />
        ))}
      </g>
      {shown.length > 1 && (
        <text
          className={styles.fcAxisLabel}
          x={width - pad.right - 2}
          y={y(shown.at(-1)!.values[middle]) - 4}
          textAnchor="end"
        >
          {percentiles[middle]}
        </text>
      )}
    </svg>
  );
}

function niceStep(raw: number): number {
  const power = 10 ** Math.floor(Math.log10(raw || 1));
  const fraction = raw / power;
  return (fraction <= 1 ? 1 : fraction <= 2 ? 2 : fraction <= 5 ? 5 : 10) * power;
}
