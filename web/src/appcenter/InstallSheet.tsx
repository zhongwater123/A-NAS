import { APIError } from "../api";
import {
  ArrowUpRight,
  Boxes,
  Check,
  CircleAlert,
  Download,
  FileCode,
  FolderOpen,
  KeyRound,
  Network,
  RotateCcw,
  TriangleAlert,
  UserRound,
  Waypoints,
  X,
} from "lucide-react";
import { ReactNode, useCallback, useEffect, useEffectEvent, useState } from "react";

import { AppJob, CatalogApp, InstallPlan, installApp, readPlan, uninstallApp } from "../appsApi";
import { AppIcon, JobOutput, Layer, Logins, Progress, RichText } from "./parts";
import {
  appURL,
  describeError,
  installFailed,
  leftoverText,
  mountLabels,
  openApp,
  runAsLabels,
  runtimeSummary,
  serviceText,
  useElapsed,
} from "./model";

interface SheetProps {
  app: CatalogApp;
  onChanged: () => Promise<void>;
  onClose: () => void;
}

// Plan → confirm → progress → result, in one sheet. Once the install is
// submitted, the stage follows the app's state from the server, so closing
// the sheet never stops the install.
export function InstallSheet({ app, busyApp, onChanged, onClose }: SheetProps & { busyApp?: CatalogApp }) {
  const [submitted, setSubmitted] = useState(app.state === "installing");
  // The job this sheet started; until the list catches up, an earlier failed job is not ours.
  const [started, setStarted] = useState<AppJob>();
  const [plan, setPlan] = useState<InstallPlan>();
  // A sheet that opens before the install plans at once.
  const [planning, setPlanning] = useState(app.state !== "installing");
  const [submitting, setSubmitting] = useState(false);
  const [failure, setFailure] = useState<string>();

  const fetchPlan = useCallback(
    () =>
      readPlan(app.id)
        .then(setPlan)
        .catch((error: unknown) => {
          setFailure(describeError(error));
        })
        .finally(() => setPlanning(false)),
    [app.id],
  );
  const loadPlan = useCallback(() => {
    setPlanning(true);
    setPlan(undefined);
    return fetchPlan();
  }, [fetchPlan]);
  // Plan once per opening; a retry reloads explicitly.
  const planOnOpen = useEffectEvent(() => {
    if (!submitted) void fetchPlan();
  });
  useEffect(() => planOnOpen(), []);

  const confirm = async () => {
    if (!plan) return;
    setSubmitting(true);
    setFailure(undefined);
    try {
      setStarted(await installApp(app.id, plan.digest));
      setSubmitted(true);
      await onChanged();
    } catch (error) {
      setFailure(describeError(error));
      if (error instanceof APIError && error.code === "plan_changed") void loadPlan();
    } finally {
      setSubmitting(false);
    }
  };
  const retry = () => {
    setSubmitted(false);
    setStarted(undefined);
    setFailure(undefined);
    void loadPlan();
  };

  // One layer for every stage, so focus and the open animation carry over.
  const failed = installFailed(app) && (!started || app.job?.startedAt === started.startedAt);
  const stage = !submitted ? "plan" : app.state === "installed" ? "done" : failed ? "failed" : "running";
  const labels = { plan: "安装计划", running: "安装进度", done: "安装完成", failed: "安装失败" };
  return (
    <Layer label={labels[stage]} onDismiss={submitting ? undefined : onClose}>
      {stage === "plan" && (
        <Review
          app={app}
          plan={plan}
          planning={planning}
          submitting={submitting}
          failure={failure}
          busyApp={busyApp}
          onConfirm={() => void confirm()}
          onReplan={() => {
            setFailure(undefined);
            void loadPlan();
          }}
          onClose={onClose}
        />
      )}
      {stage === "running" && <Installing app={app} onClose={onClose} />}
      {stage === "done" && <Finished app={app} onClose={onClose} />}
      {stage === "failed" && <Failed app={app} onClose={onClose} onRetry={retry} />}
    </Layer>
  );
}

interface ReviewProps {
  app: CatalogApp;
  plan?: InstallPlan;
  planning: boolean;
  submitting: boolean;
  failure?: string;
  busyApp?: CatalogApp;
  onConfirm: () => void;
  onReplan: () => void;
  onClose: () => void;
}

function Review({ app, plan, planning, submitting, failure, busyApp, onConfirm, onReplan, onClose }: ReviewProps) {
  const blocker =
    busyApp && busyApp.id !== app.id
      ? `${busyApp.title} 正在${busyApp.state === "installing" ? "安装" : "卸载"}，完成后才能开始安装。`
      : undefined;
  return (
    <>
      <SheetHead
        app={app}
        title={`安装 ${app.title}`}
        detail={plan ? `版本 ${plan.version || app.version} · 确认后 A-NAS 将执行下列操作` : "正在生成安装计划…"}
        onClose={submitting ? undefined : onClose}
      />
      <div className="ac-sheet-body">
        {failure && (
          <div className="ac-banner-note error" role="alert">
            <CircleAlert />
            {failure}
          </div>
        )}
        {planning && !plan && (
          <div className="ac-sheet-wait">
            <Progress />
            <p>容器代理正在按 A-NAS 安装策略渲染 Compose 文件…</p>
          </div>
        )}
        {plan && <PlanReview app={app} plan={plan} />}
      </div>
      <footer className="ac-sheet-foot">
        {blocker ? (
          <small className="ac-blocker">{blocker}</small>
        ) : (
          plan && (
            <small className="ac-digest" title={plan.digest}>
              计划摘要 {plan.digest.slice(0, 12)}
            </small>
          )
        )}
        <button className="ac-btn" onClick={onClose} disabled={submitting}>
          取消
        </button>
        {plan || planning ? (
          <button className="ac-btn primary" onClick={onConfirm} disabled={!plan || submitting || Boolean(blocker)}>
            {submitting ? "正在提交…" : "确认安装"}
          </button>
        ) : (
          <button className="ac-btn primary" onClick={onReplan}>
            <RotateCcw />
            重新生成计划
          </button>
        )}
      </footer>
    </>
  );
}

function PlanReview({ app, plan }: { app: CatalogApp; plan: InstallPlan }) {
  const sharedMounts = plan.mounts.filter((mount) => mount.kind === "shared");
  const shared = sharedMounts.length > 0;
  const writesShared = sharedMounts.some((mount) => !mount.readOnly);
  const counts = (["appdata", "shared", "system"] as const)
    .map((kind) => [kind, plan.mounts.filter((mount) => mount.kind === kind).length] as const)
    .filter(([, count]) => count > 0)
    .map(([kind, count]) => `${mountLabels[kind]} ${count} 个`);
  return (
    <section className="ac-plan" aria-label="安装内容">
      <p className="ac-plan-lead">容器不会获得特权、设备或宿主机网络，只能访问下面列出的文件夹。</p>
      {shared && (
        <div className="ac-banner-note warning">
          <TriangleAlert />
          {writesShared
            ? `${app.title} 将能读写共享空间中的 ${sharedMounts.length} 个文件夹，与成员的权限相同；卸载时撤销，不能访问任何人的个人空间。`
            : `${app.title} 将能读取共享空间中的 ${sharedMounts.length} 个文件夹，不能修改或删除其中的文件；卸载时撤销，不能访问任何人的个人空间。`}
        </div>
      )}
      <div className="ac-plan-list">
        <PlanItem icon={<UserRound />} tone="identity" title="运行身份" summary={runtimeSummary(plan)}>
          <span className="ac-plan-row">
            <code>{plan.identity.username}</code>UID {plan.identity.uid}
          </span>
          {(plan.services ?? []).map((service) => (
            <span key={service.service} className="ac-plan-row">
              <em className={`ac-runas ${service.runAs}`}>{runAsLabels[service.runAs]}</em>
              <code>{service.service}</code>
              <small>{serviceText(service, plan)}</small>
            </span>
          ))}
          {!shared && <small>只访问自己的应用数据，不能访问共享空间或个人空间。</small>}
        </PlanItem>
        {plan.credentials?.length ? (
          <PlanItem icon={<KeyRound />} tone="logins" title="登录信息" summary="随机生成，不用清单里人人可查的默认密码">
            <Logins credentials={plan.credentials} />
          </PlanItem>
        ) : null}
        <PlanItem
          icon={<FolderOpen />}
          tone="folders"
          title="使用文件夹"
          summary={counts.length ? counts.join(" · ") : "不使用宿主机文件夹"}
        >
          {plan.mounts.map((mount) => (
            <span key={`${mount.hostPath}:${mount.containerPath}`} className="ac-plan-row">
              <em className={`ac-mount ${mount.kind}`}>
                {mountLabels[mount.kind]}
                {mount.readOnly ? " · 只读" : ""}
              </em>
              <code>{mount.hostPath}</code>
              {mount.purpose && <small>{mount.purpose}</small>}
            </span>
          ))}
        </PlanItem>
        <PlanItem
          icon={<Network />}
          tone="ports"
          title="开放端口"
          summary={plan.ports.length ? plan.ports.map((port) => port.hostPort).join("、") : "不开放端口"}
        >
          {plan.ports.map((port) => (
            <span key={`${port.hostPort}/${port.protocol}`} className="ac-plan-row">
              <code>
                {port.hostPort}/{port.protocol}
              </code>
              → 容器 {port.containerPort}
              {port.purpose && <small>{port.purpose}</small>}
            </span>
          ))}
        </PlanItem>
        <PlanItem
          icon={<Waypoints />}
          tone="network"
          title="Docker 网络"
          summary={plan.networks.length ? `新建 ${plan.networks.length} 个网络` : "使用 Docker 默认网桥，不新建网络"}
        >
          {plan.networks.length > 0 && (
            <span className="ac-plan-row">
              {plan.networks.map((name) => (
                <code key={name}>{name}</code>
              ))}
            </span>
          )}
          {plan.networks.length > 0 && <small>地址只从 {plan.addressPools.join("、")} 分配，不占用局域网网段</small>}
        </PlanItem>
        <PlanItem icon={<Download />} tone="images" title="拉取镜像" summary={`${plan.images.length} 个镜像`} folded>
          {plan.images.map((image) => (
            <code key={image}>{image}</code>
          ))}
        </PlanItem>
        <PlanItem
          icon={<Boxes />}
          tone="containers"
          title="创建容器"
          summary={`${plan.containers.length} 个容器`}
          folded
        >
          <span className="ac-plan-row">
            {plan.containers.map((name) => (
              <code key={name}>{name}</code>
            ))}
          </span>
        </PlanItem>
      </div>
      <details className="ac-compose">
        <summary>
          <FileCode />
          查看将运行的 Compose 文件
        </summary>
        <pre>{plan.compose}</pre>
      </details>
    </section>
  );
}

// A plan fact: a one-line summary, with the exact items below it or folded away.
function PlanItem({
  icon,
  tone,
  title,
  summary,
  folded,
  children,
}: {
  icon: ReactNode;
  tone: string;
  title: string;
  summary: string;
  folded?: boolean;
  children?: ReactNode;
}) {
  const head = (
    <>
      <span className={`ac-plan-icon ${tone}`}>{icon}</span>
      <span className="ac-plan-title">
        <strong>{title}</strong>
        <span>{summary}</span>
      </span>
    </>
  );
  if (folded) {
    return (
      <details className="ac-plan-item">
        <summary>{head}</summary>
        <div className="ac-plan-detail">{children}</div>
      </details>
    );
  }
  return (
    <div className="ac-plan-item">
      <div className="ac-plan-summary">{head}</div>
      {children && <div className="ac-plan-detail">{children}</div>}
    </div>
  );
}

function Installing({ app, onClose }: { app: CatalogApp; onClose: () => void }) {
  const elapsed = useElapsed(app.job?.startedAt, app.job?.state === "running");
  const latest = app.job?.output.at(-1);
  return (
    <>
      <div className="ac-stage">
        <span className="ac-stage-icon busy">
          <AppIcon app={app} size="lg" />
        </span>
        <h3>正在安装 {app.title}</h3>
        <p>拉取镜像和启动容器可能需要几分钟{elapsed && ` · 已用时 ${elapsed}`}</p>
        <Progress />
        <code className="ac-latest">{latest ?? "正在准备文件夹与运行身份…"}</code>
        {app.job && <JobOutput job={app.job} />}
      </div>
      <footer className="ac-sheet-foot">
        <small>关闭后安装会在后台继续。</small>
        <button className="ac-btn" onClick={onClose}>
          在后台继续
        </button>
      </footer>
    </>
  );
}

function Finished({ app, onClose }: { app: CatalogApp; onClose: () => void }) {
  return (
    <>
      <div className="ac-stage">
        <span className="ac-stage-icon ok">
          <AppIcon app={app} size="lg" />
          <b>
            <Check />
          </b>
        </span>
        <h3>{app.title} 已安装</h3>
        <p>
          {app.running}/{app.total} 个容器运行中{app.webPort ? ` · 端口 ${app.webPort}` : ""}
        </p>
        {app.credentials?.length ? (
          <div className="ac-stage-logins">
            <strong>登录信息</strong>
            <Logins credentials={app.credentials} />
          </div>
        ) : null}
        {app.notes && (
          <div className="ac-stage-notes">
            <strong>使用须知</strong>
            <RichText text={app.notes} />
          </div>
        )}
      </div>
      <footer className="ac-sheet-foot">
        <button className="ac-btn" onClick={onClose}>
          完成
        </button>
        {appURL(app) && (
          <button
            className="ac-btn primary"
            onClick={() => {
              openApp(app);
              onClose();
            }}
          >
            打开 {app.title}
            <ArrowUpRight />
          </button>
        )}
      </footer>
    </>
  );
}

function Failed({ app, onClose, onRetry }: { app: CatalogApp; onClose: () => void; onRetry: () => void }) {
  const leftovers = leftoverText(app);
  return (
    <>
      <div className="ac-stage">
        <span className="ac-stage-icon error">
          <AppIcon app={app} size="lg" />
          <b>
            <X />
          </b>
        </span>
        <h3>{app.title} 没有安装成功</h3>
        <p role="alert">{app.job?.error || "Docker Compose 没有成功启动应用，详见下方输出。"}</p>
        {leftovers && (
          <div className="ac-banner-note warning" role="note">
            <TriangleAlert />
            <span>{leftovers}</span>
          </div>
        )}
        {app.job && <JobOutput job={app.job} open />}
      </div>
      <footer className="ac-sheet-foot">
        <button className="ac-btn" onClick={onClose}>
          关闭
        </button>
        <button className="ac-btn primary" onClick={onRetry}>
          <RotateCcw />
          重新查看安装计划
        </button>
      </footer>
    </>
  );
}

export function UninstallDialog({ app, onChanged, onClose }: SheetProps) {
  const [submitting, setSubmitting] = useState(false);
  const [failure, setFailure] = useState<string>();
  const confirm = async () => {
    setSubmitting(true);
    setFailure(undefined);
    try {
      await uninstallApp(app.id);
      await onChanged();
      onClose();
    } catch (error) {
      setFailure(describeError(error));
      setSubmitting(false);
    }
  };
  return (
    <Layer label={`确认卸载${app.title}`} size="sm" onDismiss={submitting ? undefined : onClose}>
      <div className="ac-confirm">
        <AppIcon app={app} size="lg" />
        <h3>卸载 {app.title}？</h3>
        <ul>
          <li>
            <X />
            停止并删除它的容器、Docker 网络、匿名卷和未被其他容器使用的镜像
          </li>
          <li>
            <Check />
            保留应用数据与命名卷，重新安装后可继续使用
          </li>
          <li>
            <Check />
            如果它获得过共享空间的访问，立即撤销
          </li>
        </ul>
        {failure && (
          <div className="ac-banner-note error" role="alert">
            <CircleAlert />
            {failure}
          </div>
        )}
      </div>
      <footer className="ac-sheet-foot">
        <button className="ac-btn" onClick={onClose} disabled={submitting}>
          取消
        </button>
        <button className="ac-btn danger" onClick={() => void confirm()} disabled={submitting}>
          {submitting ? "正在卸载…" : "确认卸载"}
        </button>
      </footer>
    </Layer>
  );
}

function SheetHead({
  app,
  title,
  detail,
  onClose,
}: {
  app: CatalogApp;
  title: string;
  detail: string;
  onClose?: () => void;
}) {
  return (
    <header className="ac-sheet-head">
      <AppIcon app={app} size="sm" />
      <div>
        <h3>{title}</h3>
        <p>{detail}</p>
      </div>
      {onClose && (
        <button className="ac-icon-btn" aria-label="关闭" onClick={onClose}>
          <X />
        </button>
      )}
    </header>
  );
}
