import {
  ChevronDown,
  ChevronLeft,
  Download,
  FolderOpen,
  Images,
  Info,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  Users,
} from "lucide-react";
import { type FormEvent, Fragment, useCallback, useEffect, useId, useState } from "react";

import { displayName } from "../people/people";
import { Dialog, EmptyState, Notice, Spinner } from "../ui/workspace";
import {
  batchStats,
  coverURL,
  createBatch,
  deleteBatch,
  exportURL,
  listAdminBatches,
  listRaters,
  listSources,
  type ScoringBatch,
  type ScoringRaterEntry,
  type ScoringRaterStats,
  scoringError,
  setRater,
  updateBatch,
} from "./api";
import {
  agreementGood,
  agreementPoor,
  fastShareAlert,
  formatCorrelation,
  formatCount,
  formatPercent,
  formatPoints,
  formatSeconds,
  formatWhen,
  repeatGood,
  repeatPoor,
  sameRunAlert,
  share,
  stateLabel,
} from "./model";
import styles from "./scoring.module.css";

type Toast = (text: string, tone?: "success" | "error") => void;

// While a batch imports, the list and the batch are read again this often.
const importRefreshMs = 3_000;

// BatchesPage lists the batches for administrators and opens one.
export function BatchesPage({ toast }: { toast: Toast }) {
  const [batches, setBatches] = useState<ScoringBatch[]>();
  const [failure, setFailure] = useState("");
  const [opened, setOpened] = useState<string>();
  const [creating, setCreating] = useState(false);
  const load = useCallback(
    () =>
      listAdminBatches().then(
        (items) => {
          setBatches(items);
          setFailure("");
        },
        (caught: unknown) => setFailure(scoringError(caught)),
      ),
    [],
  );
  useEffect(() => {
    let live = true;
    listAdminBatches().then(
      (items) => live && setBatches(items),
      (caught: unknown) => live && setFailure(scoringError(caught)),
    );
    return () => {
      live = false;
    };
  }, []);
  const importing = batches?.some((batch) => batch.state === "importing");
  useEffect(() => {
    if (!importing) return;
    const timer = window.setInterval(() => void load(), importRefreshMs);
    return () => window.clearInterval(timer);
  }, [importing, load]);

  const current = batches?.find((batch) => batch.id === opened);
  if (current) {
    return (
      <BatchDetail
        batch={current}
        toast={toast}
        onBack={() => setOpened(undefined)}
        onChanged={(next) => setBatches((list) => list?.map((batch) => (batch.id === next.id ? next : batch)))}
        onDeleted={() => {
          setOpened(undefined);
          void load();
        }}
      />
    );
  }
  return (
    <div className={styles.scPage}>
      <header className={styles.scPageHead}>
        <h2>批次</h2>
        <span className={styles.scGrow} />
        <button type="button" className={styles.scPrimary} onClick={() => setCreating(true)}>
          <Plus />
          新建批次
        </button>
      </header>
      {failure && <Notice tone="danger">{failure}</Notice>}
      {!batches && !failure && (
        <div className={styles.scCenter}>
          <Spinner />
        </div>
      )}
      {batches?.length === 0 && (
        <EmptyState icon={<Images />} title="还没有批次">
          在共享空间的“照片打分”文件夹中新建一个文件夹，放入照片，再新建批次。
        </EmptyState>
      )}
      {batches && batches.length > 0 && (
        <ul className={styles.scRows}>
          {batches.map((batch) => (
            <li key={batch.id}>
              <button type="button" className={styles.scRow} onClick={() => setOpened(batch.id)}>
                <RowCover batch={batch} />
                <span className={styles.scRowName}>
                  <strong>{batch.name}</strong>
                  <span>
                    照片打分 / {batch.source} · 每张 {batch.required} 人 · {formatWhen(batch.createdAt)}
                  </span>
                </span>
                <BatchProgress batch={batch} />
                <span className={`${styles.scState} ${styles[`state-${batch.state}`] ?? ""}`}>
                  {stateLabel[batch.state]}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {creating && (
        <NewBatchDialog
          onClose={() => setCreating(false)}
          onCreated={(batch) => {
            setCreating(false);
            setBatches((list) => [batch, ...(list ?? [])]);
            toast(`已新建“${batch.name}”，正在导入`);
          }}
        />
      )}
    </div>
  );
}

// RowCover is one photo of the batch. While a batch imports, a photo that
// arrives later may come first, so the address changes with the count.
function RowCover({ batch }: { batch: ScoringBatch }) {
  const [failed, setFailed] = useState<number>();
  if (batch.photos === 0 || failed === batch.photos) {
    return (
      <span className={styles.scRowThumb} aria-hidden="true">
        <Images />
      </span>
    );
  }
  return (
    <span className={styles.scRowThumb}>
      <img
        src={`${coverURL(batch.id)}?photos=${batch.photos}`}
        alt=""
        loading="lazy"
        onError={() => setFailed(batch.photos)}
      />
    </span>
  );
}

function BatchProgress({ batch }: { batch: ScoringBatch }) {
  if (batch.state === "importing") {
    const { files, imported, skipped } = batch.import;
    return (
      <span className={styles.scProgress}>
        <span>
          {files === 0 ? "正在读取文件夹" : `已处理 ${formatCount(imported + skipped)} / ${formatCount(files)} 个文件`}
        </span>
        <span className={styles.scBar} aria-hidden="true">
          <span style={{ inlineSize: `${share(imported + skipped, files) * 100}%` }} />
        </span>
      </span>
    );
  }
  const done = batch.photos > 0 && batch.complete >= batch.photos;
  return (
    <span className={styles.scProgress}>
      <span>
        已凑满 {formatCount(batch.complete)} / {formatCount(batch.photos)} 张
      </span>
      <span className={`${styles.scBar}${done ? ` ${styles.done}` : ""}`} aria-hidden="true">
        <span style={{ inlineSize: `${share(batch.complete, batch.photos) * 100}%` }} />
      </span>
    </span>
  );
}

// Ring is how much of the batch has all its raters, with the photos some
// have scored behind it.
function Ring({ batch }: { batch: ScoringBatch }) {
  const around = 2 * Math.PI * 50;
  const full = share(batch.complete, batch.photos);
  const touched = share(batch.complete + batch.started, batch.photos);
  return (
    <div className={styles.scRing}>
      <svg viewBox="0 0 120 120" aria-hidden="true">
        <circle cx="60" cy="60" r="50" stroke="rgba(242, 240, 236, 0.07)" />
        {touched > 0 && (
          <circle
            cx="60"
            cy="60"
            r="50"
            stroke="rgba(240, 180, 90, 0.32)"
            strokeDasharray={`${touched * around} ${around}`}
          />
        )}
        {full > 0 && (
          <circle
            cx="60"
            cy="60"
            r="50"
            stroke="var(--sc-accent)"
            strokeLinecap="round"
            strokeDasharray={`${full * around} ${around}`}
          />
        )}
      </svg>
      <div>
        <b>{formatPercent(full)}</b>
        <small>已凑满</small>
      </div>
    </div>
  );
}

function BatchDetail({
  batch,
  toast,
  onBack,
  onChanged,
  onDeleted,
}: {
  batch: ScoringBatch;
  toast: Toast;
  onBack: () => void;
  onChanged: (batch: ScoringBatch) => void;
  onDeleted: () => void;
}) {
  const [stats, setStats] = useState<ScoringRaterStats[]>();
  const [statsFailure, setStatsFailure] = useState("");
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [busy, setBusy] = useState(false);
  const loadStats = useCallback(
    () =>
      batchStats(batch.id).then(
        (items) => {
          setStats(items);
          setStatsFailure("");
        },
        (caught: unknown) => setStatsFailure(scoringError(caught)),
      ),
    [batch.id],
  );
  useEffect(() => {
    let live = true;
    batchStats(batch.id).then(
      (items) => live && setStats(items),
      (caught: unknown) => live && setStatsFailure(scoringError(caught)),
    );
    return () => {
      live = false;
    };
  }, [batch.id]);

  const changeState = async (state: "open" | "closed") => {
    setBusy(true);
    try {
      onChanged(await updateBatch(batch.id, { state }));
      toast(state === "closed" ? "批次已结束，不再发新照片" : "批次已重新开放");
    } catch (caught) {
      toast(scoringError(caught), "error");
    } finally {
      setBusy(false);
    }
  };
  const notStarted = Math.max(0, batch.photos - batch.complete - batch.started);
  const facts: [string, number, string?][] = [
    ["照片", batch.photos],
    ["已凑满", batch.complete, "var(--sc-accent)"],
    ["进行中", batch.started, "rgba(240, 180, 90, 0.4)"],
    ["未开始", notStarted, "rgba(242, 240, 236, 0.14)"],
    ["标记有问题", batch.flagged],
    ["参考照片", batch.anchors],
  ];
  return (
    <div className={styles.scPage}>
      <button type="button" className={styles.scBack} onClick={onBack}>
        <ChevronLeft />
        批次
      </button>
      <header className={styles.scPageHead}>
        <h2>{batch.name}</h2>
        <span className={`${styles.scState} ${styles[`state-${batch.state}`] ?? ""}`}>{stateLabel[batch.state]}</span>
        <span className={styles.scGrow} />
        {batch.state === "open" && (
          <button type="button" className={styles.scQuiet} disabled={busy} onClick={() => void changeState("closed")}>
            结束批次
          </button>
        )}
        {batch.state === "closed" && (
          <button type="button" className={styles.scQuiet} disabled={busy} onClick={() => void changeState("open")}>
            重新开放
          </button>
        )}
        <a className={styles.scPrimary} href={exportURL(batch.id)} download>
          <Download />
          导出
        </a>
      </header>
      {batch.state === "importing" && (
        <Notice tone="info">
          正在导入：
          {batch.import.files === 0
            ? "读取文件夹"
            : `已处理 ${formatCount(batch.import.imported + batch.import.skipped)} / ${formatCount(batch.import.files)} 个文件`}
          。导入完成后打分人才能开始。
        </Notice>
      )}
      <div className={styles.scHero}>
        <section className={styles.scPanel}>
          <header>
            <h3>进度</h3>
          </header>
          <div className={styles.scCoverage}>
            <Ring batch={batch} />
            <dl className={styles.scFacts}>
              {facts.map(([label, value, swatch]) => (
                <div key={label}>
                  <dt>
                    {swatch && <i style={{ background: swatch }} />}
                    {label}
                  </dt>
                  <dd>{formatCount(value)}</dd>
                </div>
              ))}
            </dl>
          </div>
          {batch.import.skipped > 0 && (
            <p className={styles.scMuted}>
              导入时跳过 {formatCount(batch.import.skipped)}{" "}
              个文件：格式不支持、无法解码，或参考照片的文件名不以分数开头。
            </p>
          )}
        </section>
        <section className={styles.scPanel}>
          <header>
            <h3>设置</h3>
            <span className={styles.scGrow} />
            <button type="button" className={styles.scLink} onClick={() => setEditing(true)}>
              <Pencil />
              修改
            </button>
          </header>
          <dl className={styles.scSettings}>
            <dt>照片文件夹</dt>
            <dd>照片打分 / {batch.source}</dd>
            <dt>每张</dt>
            <dd>{batch.required} 人打分</dd>
            <dt>打分标准</dt>
            <dd className={styles.scRubricClamp}>{batch.rubric || "没有写"}</dd>
          </dl>
        </section>
      </div>
      <section className={styles.scPanel}>
        <header>
          <h3>打分人</h3>
          <span className={styles.scGrow} />
          <button type="button" className={styles.scLink} onClick={() => void loadStats()}>
            <RefreshCw />
            刷新
          </button>
        </header>
        {statsFailure && <Notice tone="danger">{statsFailure}</Notice>}
        {!stats && !statsFailure && <Spinner small />}
        {stats?.length === 0 && <p className={styles.scMuted}>还没有人打分。</p>}
        {stats && stats.length > 0 && <StatsTable stats={stats} />}
      </section>
      {batch.complete + batch.started === 0 && batch.state !== "importing" && (
        <div>
          <button type="button" className={styles.scDanger} onClick={() => setDeleting(true)}>
            <Trash2 />
            删除批次
          </button>
        </div>
      )}
      {editing && (
        <EditBatchDialog
          batch={batch}
          onClose={() => setEditing(false)}
          onSaved={(next) => {
            setEditing(false);
            onChanged(next);
            toast("已保存");
          }}
        />
      )}
      {deleting && (
        <Dialog
          title={`删除“${batch.name}”？`}
          tone="danger"
          className={styles.scDialog}
          busy={busy}
          onClose={() => setDeleting(false)}
          footer={
            <>
              <button type="button" className={styles.scQuiet} disabled={busy} onClick={() => setDeleting(false)}>
                取消
              </button>
              <button
                type="button"
                className={styles.scDanger}
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await deleteBatch(batch.id);
                    toast(`已删除“${batch.name}”`);
                    onDeleted();
                  } catch (caught) {
                    toast(scoringError(caught), "error");
                    setDeleting(false);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                删除
              </button>
            </>
          }
        >
          <p className={styles.scDialogText}>删除批次和它的预览图，文件夹里的照片不受影响。</p>
        </Dialog>
      )}
    </div>
  );
}

// Initials sit on one of these, picked by the account, so a rater keeps a
// colour from page to page.
const tints = ["#d9c19a", "#a9c4b0", "#b9b3d6", "#d6a99c", "#a8bdd3", "#cfc6a0"];
function tintOf(id: string) {
  let sum = 0;
  for (const char of id) sum = (sum * 31 + (char.codePointAt(0) ?? 0)) % 997;
  return tints[sum % tints.length];
}

// Spread draws a rater's mean and one standard deviation either side on the
// same 0 to 5 scale the raters use.
function Spread({ row }: { row: ScoringRaterStats }) {
  if (row.scores === 0) return <span className={styles.scMuted}>—</span>;
  const at = (points: number) => `${(Math.min(5, Math.max(0, points)) / 5) * 100}%`;
  const low = Math.max(0, row.mean - row.standardDeviation);
  const high = Math.min(5, row.mean + row.standardDeviation);
  return (
    <div className={styles.scSpread}>
      <div className={styles.scAxis} aria-hidden="true">
        {[0, 1, 2, 3, 4, 5].map((point) => (
          <i key={point} style={{ insetInlineStart: at(point) }} />
        ))}
        <span style={{ insetInlineStart: at(low), inlineSize: `${((high - low) / 5) * 100}%` }} />
        <b style={{ insetInlineStart: at(row.mean) }} />
      </div>
      <small>
        {formatPoints(row.mean)} ± {formatPoints(row.standardDeviation)}
      </small>
    </div>
  );
}

const tone = (good: boolean, poor: boolean) => (good ? styles.good : poor ? styles.poor : undefined);

// StatsTable is each rater of a batch: what they give, how much they agree
// with the others and with themselves, and the habits worth a word.
function StatsTable({ stats }: { stats: ScoringRaterStats[] }) {
  return (
    <table className={styles.scTable}>
      <thead>
        <tr>
          <th scope="col">打分人</th>
          <th scope="col">平均分 ± 标准差</th>
          <th scope="col" title="给出的分数；跳过是无法判断，标记是图片有问题">
            打分
          </th>
          <th scope="col" title="与其他人在同一批照片上平均分的相关系数，共同照片少于 20 张时不算">
            与他人一致
          </th>
          <th scope="col" title="同一张照片隔一段时间再出现时，两次分数之差的平均">
            重复差
          </th>
          <th scope="col" title="从照片出现到打分，所有打分的中位数">
            看图
          </th>
        </tr>
      </thead>
      <tbody>
        {stats.map((row) => {
          const name = row.rater.username
            ? displayName({ username: row.rater.username, nickname: row.rater.nickname })
            : "已停用的账号";
          const alerts = [];
          if (row.scores > 0 && row.fastShare >= fastShareAlert) {
            alerts.push(`${formatPercent(row.fastShare)} 的分数在 1 秒内打出`);
          }
          if (row.longestRun >= sameRunAlert) alerts.push(`连续 ${formatCount(row.longestRun)} 张同分`);
          const agreement = row.agreement;
          const repeat = row.repeatDifference;
          return (
            <Fragment key={row.rater.id}>
              <tr>
                <th scope="row">
                  <div className={styles.scWho}>
                    <span className={styles.scInitial} style={{ background: tintOf(row.rater.id) }} aria-hidden="true">
                      {Array.from(name)[0]?.toUpperCase()}
                    </span>
                    <span>
                      <strong>{name}</strong>
                      {row.rater.username && row.rater.nickname && <small>{row.rater.username}</small>}
                    </span>
                  </div>
                </th>
                <td>
                  <Spread row={row} />
                </td>
                <td className={styles.scMetric}>
                  <strong>{formatCount(row.scores)}</strong>
                  <small>
                    跳过 {formatCount(row.skips)} · 标记 {formatCount(row.flags)}
                  </small>
                </td>
                <td className={styles.scMetric}>
                  <strong
                    className={
                      agreement === null ? undefined : tone(agreement >= agreementGood, agreement < agreementPoor)
                    }
                  >
                    {formatCorrelation(agreement)}
                  </strong>
                  <small>
                    {agreement === null ? "共同照片不足 20 张" : `按 ${formatCount(row.agreementPhotos)} 张`}
                  </small>
                </td>
                <td className={styles.scMetric}>
                  <strong className={repeat === null ? undefined : tone(repeat <= repeatGood, repeat >= repeatPoor)}>
                    {repeat === null ? "—" : formatPoints(repeat)}
                  </strong>
                  <small>重复 {formatCount(row.repeats)} 次</small>
                </td>
                <td className={styles.scMetric}>
                  <strong>{row.scores > 0 ? formatSeconds(row.medianViewMs) : "—"}</strong>
                </td>
              </tr>
              {alerts.length > 0 && (
                <tr className={styles.scAlertsRow}>
                  <td colSpan={6}>
                    <div className={styles.scAlerts}>
                      {alerts.map((alert) => (
                        <span key={alert}>{alert}</span>
                      ))}
                    </div>
                  </td>
                </tr>
              )}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}

function NewBatchDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (batch: ScoringBatch) => void }) {
  const [sources, setSources] = useState<string[]>();
  const [failure, setFailure] = useState("");
  const [source, setSource] = useState("");
  const [name, setName] = useState("");
  const [required, setRequired] = useState(3);
  const [rubric, setRubric] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    listSources()
      .then((items) => {
        if (!live) return;
        setSources(items);
        if (items.length > 0) {
          setSource(items[0]);
          setName(items[0].slice(0, 40));
        }
      })
      .catch((caught: unknown) => live && setFailure(scoringError(caught)));
    return () => {
      live = false;
    };
  }, []);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      onCreated(await createBatch({ name: name.trim(), source, required, rubric: rubric.trim() }));
    } catch (caught) {
      setFailure(scoringError(caught));
      setBusy(false);
    }
  };
  return (
    <Dialog
      title="新建批次"
      className={styles.scDialog}
      busy={busy}
      onClose={onClose}
      wide
      footer={
        <>
          <button type="button" className={styles.scQuiet} disabled={busy} onClick={onClose}>
            取消
          </button>
          <button
            type="submit"
            form="scoring-new-batch"
            className={styles.scPrimary}
            disabled={busy || !source || !name.trim()}
          >
            新建
          </button>
        </>
      }
    >
      <form id="scoring-new-batch" className={styles.scForm} onSubmit={(event) => void submit(event)}>
        {failure && <Notice tone="danger">{failure}</Notice>}
        {!sources && !failure && <Spinner small />}
        {sources?.length === 0 && (
          <Notice tone="info">共享空间的“照片打分”文件夹里还没有子文件夹。新建一个并放入照片后再来。</Notice>
        )}
        {sources && sources.length > 0 && (
          <>
            <label className={styles.scField}>
              <span className={styles.scFieldLabel}>照片文件夹</span>
              <span className={styles.scInput}>
                <FolderOpen aria-hidden="true" />
                <select
                  value={source}
                  onChange={(event) => {
                    if (name === source.slice(0, 40)) setName(event.target.value.slice(0, 40));
                    setSource(event.target.value);
                  }}
                >
                  {sources.map((item) => (
                    <option key={item} value={item}>
                      照片打分 / {item}
                    </option>
                  ))}
                </select>
                <ChevronDown aria-hidden="true" />
              </span>
            </label>
            <BatchFields
              name={name}
              setName={setName}
              required={required}
              setRequired={setRequired}
              rubric={rubric}
              setRubric={setRubric}
            />
          </>
        )}
      </form>
    </Dialog>
  );
}

const fewestRaters = 1;
const mostRaters = 10;

function BatchFields({
  name,
  setName,
  required,
  setRequired,
  rubric,
  setRubric,
}: {
  name: string;
  setName: (value: string) => void;
  required: number;
  setRequired: (value: number) => void;
  rubric: string;
  setRubric: (value: string) => void;
}) {
  const hint = useId();
  const requiredLabel = useId();
  const setCount = (value: number) => setRequired(Math.min(mostRaters, Math.max(fewestRaters, Math.round(value) || 1)));
  return (
    <>
      <div className={styles.scPair}>
        <label className={styles.scField}>
          <span className={styles.scFieldLabel}>批次名称</span>
          <span className={styles.scInput}>
            <input value={name} maxLength={40} onChange={(event) => setName(event.target.value)} />
          </span>
        </label>
        <div className={styles.scField}>
          <span id={requiredLabel} className={styles.scFieldLabel}>
            每张照片几人打分
          </span>
          <div className={styles.scStepper}>
            <button
              type="button"
              aria-label="少一人"
              disabled={required <= fewestRaters}
              onClick={() => setCount(required - 1)}
            >
              −
            </button>
            <input
              type="number"
              min={fewestRaters}
              max={mostRaters}
              value={required}
              aria-labelledby={requiredLabel}
              onChange={(event) => setCount(Number(event.target.value))}
            />
            <button
              type="button"
              aria-label="多一人"
              disabled={required >= mostRaters}
              onClick={() => setCount(required + 1)}
            >
              +
            </button>
          </div>
        </div>
      </div>
      <label className={styles.scField}>
        <span className={styles.scFieldLabel}>打分标准</span>
        <textarea
          value={rubric}
          maxLength={2000}
          rows={5}
          placeholder="打分人开始前会看到，例如每个分数段是什么样的照片"
          aria-describedby={hint}
          onChange={(event) => setRubric(event.target.value)}
        />
      </label>
      <p id={hint} className={styles.scNote}>
        <Info aria-hidden="true" />
        <span>
          参考照片放在照片文件夹的 <code>_锚点</code> 子文件夹里，文件名以 0–5 的参考分开头，如{" "}
          <code>3.5-湖边日落.jpg</code>。
        </span>
      </p>
    </>
  );
}

function EditBatchDialog({
  batch,
  onClose,
  onSaved,
}: {
  batch: ScoringBatch;
  onClose: () => void;
  onSaved: (batch: ScoringBatch) => void;
}) {
  const [name, setName] = useState(batch.name);
  const [required, setRequired] = useState(batch.required);
  const [rubric, setRubric] = useState(batch.rubric);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      onSaved(await updateBatch(batch.id, { name: name.trim(), required, rubric: rubric.trim() }));
    } catch (caught) {
      setFailure(scoringError(caught));
      setBusy(false);
    }
  };
  return (
    <Dialog
      title="修改批次"
      className={styles.scDialog}
      busy={busy}
      onClose={onClose}
      wide
      footer={
        <>
          <button type="button" className={styles.scQuiet} disabled={busy} onClick={onClose}>
            取消
          </button>
          <button type="submit" form="scoring-edit-batch" className={styles.scPrimary} disabled={busy || !name.trim()}>
            保存
          </button>
        </>
      }
    >
      <form id="scoring-edit-batch" className={styles.scForm} onSubmit={(event) => void submit(event)}>
        {failure && <Notice tone="danger">{failure}</Notice>}
        <BatchFields
          name={name}
          setName={setName}
          required={required}
          setRequired={setRequired}
          rubric={rubric}
          setRubric={setRubric}
        />
      </form>
    </Dialog>
  );
}

// RatersPage chooses which members score.
export function RatersPage({ toast, onChanged }: { toast: Toast; onChanged: () => void }) {
  const [entries, setEntries] = useState<ScoringRaterEntry[]>();
  const [failure, setFailure] = useState("");
  const [changing, setChanging] = useState<string>();
  useEffect(() => {
    let live = true;
    listRaters()
      .then((items) => live && setEntries(items))
      .catch((caught: unknown) => live && setFailure(scoringError(caught)));
    return () => {
      live = false;
    };
  }, []);
  const toggle = async (entry: ScoringRaterEntry) => {
    setChanging(entry.id);
    try {
      await setRater(entry.id, !entry.rater);
      setEntries((list) => list?.map((item) => (item.id === entry.id ? { ...item, rater: !entry.rater } : item)));
      onChanged();
    } catch (caught) {
      toast(scoringError(caught), "error");
    } finally {
      setChanging(undefined);
    }
  };
  const chosen = entries?.filter((entry) => entry.rater).length ?? 0;
  return (
    <div className={styles.scPage}>
      <header className={styles.scPageHead}>
        <h2>打分人</h2>
        {entries && <span className={styles.scMuted}>已选 {formatCount(chosen)} 人</span>}
      </header>
      <p className={styles.scLead}>选中的成员桌面上会出现“照片打分”，只能看到自己的分数。</p>
      {failure && <Notice tone="danger">{failure}</Notice>}
      {!entries && !failure && <Spinner />}
      {entries?.length === 0 && <EmptyState icon={<Users />} title="没有可选的成员" />}
      {entries && entries.length > 0 && (
        <ul className={styles.scPeople}>
          {entries.map((entry) => {
            const name = displayName({ username: entry.username, nickname: entry.nickname });
            return (
              <li key={entry.id}>
                <label>
                  <span className={styles.scInitial} style={{ background: tintOf(entry.id) }} aria-hidden="true">
                    {Array.from(name)[0]?.toUpperCase()}
                  </span>
                  <span className={styles.scPerson}>
                    <strong>{name}</strong>
                    {entry.nickname && <span>{entry.username}</span>}
                  </span>
                  <input
                    type="checkbox"
                    role="switch"
                    className={styles.scSwitch}
                    checked={entry.rater}
                    aria-checked={entry.rater}
                    disabled={changing === entry.id}
                    onChange={() => void toggle(entry)}
                  />
                </label>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
