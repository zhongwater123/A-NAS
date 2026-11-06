// Vendored from PC2005-cloud/dsh-pet 10f6c868b811 (MIT, see LICENSE).
/**
 * 一张宠物卡的装配根：按固定顺序创建驱动与 hook，再把当帧能力拼成视图输入。
 * 只做接线：不判定（判定在 shared 与各驱动）、不渲染（DOM 在 card-view）、不持有跨卡状态。
 * 装配顺序不能改：动画链早于运动（回调要读走位）、运动早于拖拽（拖拽读飞行）、浮层最后。
 * 必须在渲染期填池：迟绑槽里的驱动实例只在本次渲染内赋值，别处不得写入。
 * @module dsh-pet/src/client/pet/card/runtime
 */
import { pick } from '../../../shared/pickers';
import { postAction } from '../../../shared/state';
import { ROUTE_PATH } from '../../../shared/constants';
import { resolveFontPx } from '../../../shared/fonts';
import { resolvePetSizePx } from '../../../shared/pet-size';
import { TOOLTIP_GAP_RATIO, tooltipPlacement } from '../../../shared/tooltip';
import { makeWhisperBubble } from '../../bubble';
import { playClickSound } from '../click-sound';
import { boxToRatio, derivePetMetrics, petRootStyle, petStageStyle } from '../motion/geometry';
import { makePlaybackReporter } from '../playback-report';
import { createAnimationFlow } from './animation-flow';
import { createBubbleSlots } from './bubble-slots';
import { createCardAnimation } from './animation';
import { createCardDrag } from './drag';
import { createCardMotion } from './motion';
import { createCardState } from './state';
import { createCueEffects } from './cue-effects';
import { createLockFreeze } from './lock-freeze';
import type { CardHub } from './hub';
import type { CardLive } from './live';
import type { CardViewProps } from './view';
import type { PetCardProps } from './props';
import type { PetRuntime } from '../types';

/**
 * 造卡片装配根的工厂：组件级单例（气泡、状态、指令、动画流）都在这里建一次。
 *
 * @param rt - 注入的 react 能力。
 * @returns 接收容器 props 的 hook，返回交给 card-view 的渲染输入。
 */
export function createCardRuntime(rt: PetRuntime) {
  const { h, useEffect, useRef, useState } = rt;
  /** 碎碎念气泡：文本与显隐由装配根传入。 */
  const WhisperBubble = makeWhisperBubble({ h });
  /** A-NAS: there are no todos. Only the menu entry opens the panel, so its slot renders nothing. */
  const TodoBubble = () => null;
  /** 四种气泡的单槽：显隐、停留计时与优先级都在这里。 */
  const useBubbleSlots = createBubbleSlots(
    { h, useState, useEffect, useRef },
    { todoBubble: TodoBubble, whisperBubble: WhisperBubble },
  );
  const useCardState = createCardState({ useState, useRef });
  const useLockFreeze = createLockFreeze({ useEffect, useRef });
  const useCueEffects = createCueEffects({ useEffect, useRef });
  const useAnimationFlow = createAnimationFlow({ useRef });
  const useCardAnimation = createCardAnimation({ useEffect, useRef });
  const useCardMotion = createCardMotion({ useEffect, useRef });
  const useCardDrag = createCardDrag({ useRef, useEffect });

  return function useCardRuntime(props: PetCardProps): CardViewProps {
    const { arena, cfg, lock, lockTick, paused, sleep, sleepTick, workStatus } = props;
    const { onFocusSession } = props;

    // 实际渲染宽度：百分比只在挂载与配置变更时按当次视口宽解析一次，拖窗口不跟随。
    // 物理、字号与 CSS 变量都吃这一个值——两处各算一次会让命中框与视觉错位。
    const effectiveSizePx = resolvePetSizePx(cfg.size, cfg.sizePercent, window.innerWidth);

    // 卡片状态容器：动画、朝向、拖拽态、提示与自定义位置；写入口在 card-state。
    const card = useCardState({
      anim: cfg.animations.idle[0] ?? '',
      corner: cfg.position.corner,
      margin: { x: cfg.position.marginX, y: cfg.position.marginY },
      size: effectiveSizePx,
    });
    const { actions, state } = card;
    const { corner, customPos, facing, margin, size, tipOn } = state;

    // 驱动实例与跨关注点入口：创建顺序见模块头，填充只在本函数内。
    const liveRef = useRef<CardLive>({} as CardLive);
    const pendingSquashRef = useRef(false);

    // 成品配置与工作状态的最新值：回调晚于状态更新，只能读 ref。
    const cfgRef = useRef(cfg);
    cfgRef.current = cfg;
    const workStatusRef = useRef(workStatus);
    workStatusRef.current = workStatus;
    // A-NAS: the page's pause, for the animation chain's callbacks.
    const pausedRef = useRef(paused === true);
    pausedRef.current = paused === true;

    // DOM 节点：视图挂载后才有值，驱动全部经取值函数读。
    const rootRef = useRef<HTMLDivElement | null>(null);
    const stageRef = useRef<HTMLDivElement | null>(null);
    const videoARef = useRef<HTMLVideoElement | null>(null);
    const videoBRef = useRef<HTMLVideoElement | null>(null);
    const tipRef = useRef<HTMLDivElement | null>(null);

    // 回执在 playback-report.ts：地址、请求体与失败日志都在那里，这里只要一个函数。
    const reportPlayback = makePlaybackReporter({ petId: cfg.id });

    // 尺寸与派生几何；度量系数在 motion/geometry.ts，这里只解构使用。
    const { halfW, halfH, bottomPad, sideAllow } = derivePetMetrics(size);
    // 字号按本宠物尺寸算好注入 root，气泡与悬浮提示读它。
    const fontPx = resolveFontPx(cfg.font, size);
    const petAnims = cfg.animations;

    // 配置变化即时跟随，容器重新合并或设置页保存后触发。
    useEffect(() => {
      actions.setSize(resolvePetSizePx(cfg.size, cfg.sizePercent, window.innerWidth));
      // eslint-disable-next-line react-hooks/exhaustive-deps -- setter 稳定，变化原因只有配置的两个尺寸字段
    }, [cfg.size, cfg.sizePercent]);

    useEffect(() => {
      actions.setCorner(cfg.position.corner);
      actions.setMargin({ x: cfg.position.marginX, y: cfg.position.marginY });
      // 改位置是明确的「放这里」：清掉会话内的拖拽位置，让配置重新生效——
      // 这也是设置页把跑出视口的宠物叫回来的通道。只调大小不在此列，不该丢位置。
      actions.setCustomPos(null);
      // eslint-disable-next-line react-hooks/exhaustive-deps -- setter 稳定，变化原因只有这三个位置字段
    }, [cfg.position.corner, cfg.position.marginX, cfg.position.marginY]);

    // 四种气泡的单槽：显隐、停留计时与优先级都在 bubble-slots，这里只喂视图输入。
    const bubbles = useBubbleSlots({
      assetRoot: cfg.assetRoot,
      workStatusEnabled: cfg.workStatusEnabled,
      workTerminal: workStatus?.state === 'success' || workStatus?.state === 'error',
      workSessionId: workStatus?.sessionId ?? null,
      ...(onFocusSession === undefined ? {} : { onFocusSession }),
    });

    const ownAnim = useCardAnimation({
      card,
      cfgRef,
      live: liveRef,
      lock,
      pendingSquash: pendingSquashRef,
      petId: cfg.id,
      report: reportPlayback,
      setCuePlayback: actions.setCuePlayback,
      videoA: () => videoARef.current,
      videoB: () => videoBRef.current,
      workStatusRef,
      paused: () => pausedRef.current,
    });

    const motion = useCardMotion({
      arena,
      card,
      cfgRef,
      live: liveRef,
      metrics: { bottomPad, halfH, halfW, sideAllow, size },
      moves: petAnims.moves,
      petId: cfg.id,
      root: () => rootRef.current,
      setAnim: actions.setAnim,
      setCustomPos: actions.setCustomPos,
      setOnce: actions.setOnce,
    });

    const drag = useCardDrag({
      bumpSeq: actions.bumpSeq,
      card,
      cfgRef,
      live: liveRef,
      motion,
      pendingSquash: pendingSquashRef,
      root: () => rootRef.current,
      setAnim: actions.setAnim,
      setCustomPos: actions.setCustomPos,
      setOnce: actions.setOnce,
      setTipOn: actions.setTipOn,
    });

    // A-NAS: the right-click menu, chat dialog and hotkeys (entry.ts) and the menu's actions
    // (menu-actions.ts) are not vendored: they open the balance, todos, settings and chat. The two
    // things the card still needs from them follow, as dsh-pet wrote them: the hover tooltip the
    // menu entry places (entry.ts:111-123, 334-335 and 396-404) and the menu actions' branch for a
    // named animation, the only one a host instruction takes (menu-actions.ts:179-198).
    /** 把提示框摆到光标右下，放不下时翻边并夹回视口。 */
    const placeTip = (clientX: number, clientY: number): void => {
      const tipEl = tipRef.current;
      if (!tipEl) return;
      const viewport = document.documentElement;
      const p = tooltipPlacement(
        { x: clientX, y: clientY },
        { w: tipEl.offsetWidth, h: tipEl.offsetHeight },
        { x: 0, y: 0, w: viewport.clientWidth, h: viewport.clientHeight },
        motion.metrics().size * TOOLTIP_GAP_RATIO,
      );
      tipEl.style.left = p.x + 'px';
      tipEl.style.top = p.y + 'px';
    };
    liveRef.current.placeTip = placeTip;
    liveRef.current.tipOn = () => card.state.tipOn;
    /** 依据名点播：文字类朝右时强制朝左（keepFacing 时跳过），移动类走真实位移，挪不动时退化为纯播放。 */
    const playByName = (anim: string, playbackId?: string, keepFacing = false): void => {
      const cuePlaybackId = playbackId ?? null;
      const anims = cfgRef.current.animations;
      const noMirror = (anims.categories ?? []).some((c) => c.noMirror === true && c.actions.includes(anim));
      if (!keepFacing && noMirror && card.facingRef.current === 'right') actions.setFacing('left');
      const playOnce = (): void => {
        actions.setOnce(true);
        actions.setAnim(anim);
        actions.setCuePlayback(cuePlaybackId);
      };
      if (anims.moves.actions.some((action) => action.name === anim)) {
        if (motion.walk.tryMove(anim) === false) {
          motion.stopMove();
          playOnce();
        } else if (cuePlaybackId !== null) {
          // 移动系统接管，不换视频源，本段点播不会真的播起来，如实回 failed。
          reportPlayback(cuePlaybackId, 'failed', 'busy');
        }
        return;
      }
      motion.stopMove();
      playOnce();
    };

    // 占用锁：宿主靠内存持有，渲染端靠 /state 的 pets.<id>.lock 叶子感知。
    // 冻结只挡自播行为；显式指令（say / anim / position / throw）一概不挡，那正是持锁者要保护的内容。
    const lockFreeze = useLockFreeze({
      lock,
      lockTick,
      paused,
      onFrozen: () => {
        // 光堵随机链不够：它只在动画播完时调用，不暂停会把当前这段播完才停。
        motion.stopMove();
        // 任务状态气泡：锁一生效就收掉，解冻不补亮。
        bubbles.hideWork();
      },
      onResumed: () => {
        // A-NAS: the freeze paused the frame in front; play it on. A stage's single loop, picked again
        // below, is the same animation and would not switch, so it stayed still; anything else picked
        // takes over once it is ready. A pet asleep under the freeze sleeps on.
        const front = ownAnim.frontVideo();
        if (front?.paused && !front.ended) void front.play().catch(() => {});
        if (liveRef.current.flow?.sleeping() ?? false) return;
        // 解冻：非终态工作状态回档位循环，否则回 idle 一次，播完由动画链接回随机链。
        if (liveRef.current.flow?.resumeWorkStatusAnim() ?? false) return;
        if (petAnims.idle.length) {
          actions.setAnim(pick(petAnims.idle, card.animRef.current));
          actions.setOnce(true);
          actions.bumpSeq();
        }
      },
      setFrozen: ownAnim.setFrozen,
    });
    liveRef.current.releaseLock = () => {
      // A-NAS: pressing a pet that only a pause froze is the member coming back, not taking it over.
      if (!lockFreeze.locked()) return;
      lockFreeze.release();
      void postAction(ROUTE_PATH + '/lock/release?pet=' + encodeURIComponent(cfg.id), { force: true });
    };

    // A-NAS: a pause also holds a flight where it is (a lock lets it fly on) and keeps where a walk the
    // freeze stopped got to, so that nothing runs a frame loop while the member is away; the flight
    // carries on at its speed when the pause ends.
    const heldFlightRef = useRef<{ vx: number; vy: number } | null>(null);
    useEffect(() => {
      if (paused) {
        if (motion.flight.isFlying()) {
          heldFlightRef.current = motion.flight.velocity();
          motion.flight.stop();
        }
        const box = motion.box();
        const viewport = { width: window.innerWidth, height: window.innerHeight };
        if (box) actions.setCustomPos(boxToRatio(box, viewport, { w: halfW, h: halfH }));
        return;
      }
      const held = heldFlightRef.current;
      const box = motion.box();
      heldFlightRef.current = null;
      if (held && box) motion.startThrow(box.x, box.y, held.vx, held.vy);
      // eslint-disable-next-line react-hooks/exhaustive-deps -- only a pause starting or ending moves anything
    }, [paused]);

    // 共享运行时：状态容器、驱动与跨关注点动作拼成一份，指令处理按需取值。
    const hub: CardHub = {
      anims: () => cfgRef.current.animations,
      applyThrow: motion.applyThrow,
      bubbles,
      card,
      cfg: () => cfgRef.current,
      isDragging: () => drag.isActive(),
      ownAnim,
      // 来自 pets.<id>.anim 的点播是宿主指令，不按「用户点击」判睡觉
      playByName,
      posFollow: motion.posFollow,
      speak: (text, image, url, duration, anim) => liveRef.current.flow?.speak(text, image, url, duration, anim),
      // 睡觉是状态不是指令：自动来源（工作状态/余额/碎碎念）读它决定要不要闭嘴
      sleeping: () => liveRef.current.flow?.sleeping() ?? false,
      // 占用锁：锁的语义是把控制权交给第三方，睡觉的菜单闸门与自动化闸门都对持锁者让路
      locked: () => ownAnim.isFrozen(),
      stopMotion: motion.stopMove,
      tryWalk: motion.walk.tryMove,
    };
    // 七条指令的分派在 cue-effects：这里只把当拍 props 交过去。
    useCueEffects(hub, props);
    // 动画流比指令处理晚一步创建：它要读工作状态，也要经 hub 说话与恢复档位。
    const flow = useAnimationFlow(hub, workStatus);
    liveRef.current.flow = flow;

    // 睡觉：宿主写叶子、渲染端执行；只在叶子变化时推一次（未变化的重渲染不重播动画）。
    useEffect(() => {
      if (sleep) flow.applySleep(sleep);
      // eslint-disable-next-line react-hooks/exhaustive-deps -- 只按叶子变化次数触发，sleep 是当拍快照
    }, [sleepTick]);

    // A-NAS: every move that ends sets the custom position (a drag, a flight, a walk, a position command,
    // the window's new size); the page keeps the last one on this device.
    useEffect(() => {
      if (customPos) props.onPlace?.(customPos);
      // eslint-disable-next-line react-hooks/exhaustive-deps -- only a new position is news to the page
    }, [customPos]);

    const stageStyle = petStageStyle(bottomPad);
    const rootStyle = petRootStyle(
      customPos,
      { width: window.innerWidth, height: window.innerHeight },
      { h: halfH, w: halfW },
    );

    return {
      // 气泡单槽：四种气泡共用同一套定位，按优先级只挂一个。
      bubble: bubbles.node(),
      corner,
      facing,
      fontBubblePx: fontPx.bubble,
      fontTipPx: fontPx.tip,
      marginX: margin.x,
      marginY: margin.y,
      name: cfg.name,
      onClick: () => {
        // 拖拽守卫必须在睡觉分支之前：指针捕获后浏览器一定会在松手时补发一次 click，
        // 它不是点击（拖了多久都一样）。醒着时这条由 drag.onClick 内部判，睡觉分支绕过了那里。
        if (drag.justDragged() || drag.isActive()) return;
        const phase = liveRef.current.flow?.sleepPhase() ?? null;
        // 真睡着（循环中或醒来过渡）：点一下 = 叫醒。挤压与音效照常，
        // 但不播点击回应动画——这一下的产物是「起来」。
        if (phase !== null && phase !== 'start') {
          const front = ownAnim.frontVideo();
          if (front) liveRef.current.squash.start(front);
          playClickSound();
          void postAction(ROUTE_PATH + '/wake?pet=' + encodeURIComponent(cfg.id));
          return;
        }
        // 入睡过渡里被点：这次入睡作废，按普通点击处理（挤压 + 音效 + 点击回应），不再播「起来」
        if (phase === 'start') void postAction(ROUTE_PATH + '/wake?pet=' + encodeURIComponent(cfg.id));
        drag.onClick();
      },
      // A-NAS: no menu of its own; a right-click is left to the page.
      onContextMenu: () => {},
      onPointerDown: drag.onPointerDown,
      onPointerEnter: (e) => {
        placeTip(e.clientX, e.clientY); // 先摆位再显示，否则会先在兜底坐标闪一帧。
        actions.setTipOn(true);
      },
      onPointerLeave: () => actions.setTipOn(false),
      onPointerMove: drag.onPointerMove,
      onPointerUp: drag.onPointerUp,
      rootRef,
      rootStyle,
      size,
      stageRef,
      stageStyle,
      tipOn,
      tipRef,
      videoARef,
      videoBRef,
    };
  };
}
