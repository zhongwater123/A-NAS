// Vendored from PC2005-cloud/dsh-pet 10f6c868b811 (MIT, see LICENSE).
/**
 * 指令处理：余额、余额不可用、工作状态、说话、点播、位置、抛掷七条指令各一个消费函数。
 * 不负责判定口径（余额档位、工作状态索引、播完分支分别属于 shared 与动画链），也不持有驱动。
 * 依赖卡运行时与当帧 props；每条指令自带「这一拍是否已消费」的守卫。
 * 不变量：只有计数器变化才动手；占用锁生效时自动来源整拍丢弃，显式指令不挡。
 * @module dsh-pet/src/client/pet/card/cue-effects
 */
import { inPools, pickSlot, poolIncludes } from '../../../shared/pickers';
import { WORK_STATUS_INDEX } from '../../../shared/work-status';
import type { CardHub } from './hub';
import type { PetCardProps } from './props';
import type * as ReactNS from 'react';

/** 注入的 react 能力。 */
export interface CueEffectsRuntime {
  /** 副作用；七个消费函数共用一个 effect。 */
  useEffect: (effect: ReactNS.EffectCallback, deps?: ReactNS.DependencyList) => void;
  /** 引用；守卫各自的已消费计数器。 */
  useRef: <T>(initial: T) => ReactNS.MutableRefObject<T>;
}

/** 日志行的时刻前缀。 */
const stamp = (): string => new Date().toTimeString().slice(0, 8);

/** 一行带时刻的日志：客户端统一前缀，便于 grep。 */
const log = (message: string): void => {
  // A-NAS: dsh-pet's trace lines only in a development build.
  if (import.meta.env.DEV) console.log('[dsh-pet] ' + stamp() + ' ' + message);
};

/**
 * 制造指令处理的 hook。
 *
 * @param rt - 注入的 react 能力。
 * @returns 接收运行时与 props 的 hook；内部只注册一个分派 effect。
 */
export function createCueEffects(rt: CueEffectsRuntime) {
  const { useEffect, useRef } = rt;

  return function useCueEffects(hub: CardHub, props: PetCardProps): void {
    const { animCue, animCueTick, posCue, posCueTick, say, sayTick, throwCue, throwCueTick } = props;
    const { workStatus, workStatusTick } = props;

    const prevWorkRef = useRef(0);
    // 调试日志：上一档位，null 表示空闲，undefined 表示启动后尚未触发。
    const prevWorkStateRef = useRef<string | null | undefined>(undefined);
    // A-NAS: the clip a step asked for that the stage last played, which loops outside the stage's pool.
    const workAnimRef = useRef<string | null>(null);
    const prevSayRef = useRef(0);
    const prevAnimRef = useRef(0);
    const prevPosRef = useRef(0);
    const prevThrowRef = useRef(0);

    /** 工作状态联动：按档位播动画并弹文本气泡；回空闲时收起气泡并改播完即停。 */
    const consumeWorkStatus = (): void => {
      const cfg = hub.cfg();
      const card = hub.card;
      if (workStatusTick === 0 || workStatusTick === prevWorkRef.current) return;
      prevWorkRef.current = workStatusTick;
      // 占用锁：工作状态是自动来源，锁生效时不响应；解冻时由冻结驱动重新对齐当前状态。
      // 不挡宿主写入：sections.workStatus 是所有宠物共用一份，按宠物在渲染端忽略。
      if (hub.ownAnim.isFrozen()) return;
      // 睡觉：同样不响应工作状态（不换动画、不弹文案）；醒来后由下一次状态变化重新对齐
      if (hub.sleeping()) return;
      if (!workStatus || workStatus.state === null) {
        log('pet=' + cfg.id + ' ' + (prevWorkStateRef.current ?? 'null') + '->null    播完即停（回待机，收起气泡）');
        prevWorkStateRef.current = null;
        // 回空闲时把循环中的工作动画改成「播完即停」：否则 ended 永不触发，动画链拿不到
        // 推进信号，宠物会一直卡在工作动画上。
        // A-NAS: so does the clip a step asked for.
        const current = card.animRef.current;
        if (poolIncludes(hub.anims().events?.workStatus ?? [], current) || current === workAnimRef.current)
          hub.ownAnim.stopLoopingFront();
        workAnimRef.current = null;
        hub.bubbles.resetWork();
        return;
      }
      const pool = hub.anims().events?.workStatus;
      if (!pool || pool.length === 0) {
        console.error('[dsh-pet] 配置缺少 animations.events.workStatus，无法播放工作状态动画');
        return;
      }
      const idx = WORK_STATUS_INDEX[workStatus.state];
      const slot = pool[idx];
      if (slot === undefined) {
        console.error('[dsh-pet] work-status 档位索引越界：state=' + workStatus.state + ' idx=' + idx);
        return;
      }
      // A-NAS: the clip the step asks for, when the pet has it, rather than one of the stage's.
      const own = workStatus.anim != null && inPools(hub.anims(), workStatus.anim) ? workStatus.anim : null;
      // A-NAS: a stage whose animation this NAS has not installed is an empty slot (it keeps its index);
      // the pet carries on with what it was doing rather than switch to no animation at all.
      if (own === null && slot.length === 0) return;
      const name = own ?? pickSlot(slot, card.animRef.current); // 档内随机抽 1，并避开当前正播动画。
      log('pet=' + cfg.id + ' ' + (prevWorkStateRef.current ?? 'null') + '->' + workStatus.state + '    ' + name);
      const stateChanged = prevWorkStateRef.current !== workStatus.state;
      prevWorkStateRef.current = workStatus.state;
      // A-NAS: a stage stops the pet's own walk, not a move VNAS asked for: the turn that asked for it
      // ends right after, and its end would leave the pet halfway.
      hub.stopWalk();
      // 气泡文本：任务详情优先，否则从 workStatusTexts[档位] 随机抽一句。
      const textGroup = Array.isArray(cfg.workStatusTexts) ? cfg.workStatusTexts[idx] : undefined;
      const configuredText =
        Array.isArray(textGroup) && textGroup.length > 0
          ? textGroup[Math.floor(Math.random() * textGroup.length)]
          : undefined;
      // 文本总是更新，任务详情可能在同一状态下变化。
      hub.bubbles.updateWorkText(workStatus.task ?? configuredText ?? null);
      const terminal = workStatus.state === 'success' || workStatus.state === 'error';
      // 只在状态变化时点亮或收起气泡：同状态后续 tick 不重新点亮已自动收起的终态气泡。
      if (stateChanged) hub.bubbles.lightWork(terminal);
      // 循环语义：终态播一遍；非终态单候选无限循环，多候选播一遍后由播完判定轮换。
      // A-NAS: a step's own clip loops like a single one.
      const rotating = own === null && !terminal && Array.isArray(slot) && slot.length > 1;
      workAnimRef.current = own;
      card.actions.setOnce(terminal || rotating);
      card.actions.setAnim(name);
    };

    /** 说话（碎碎念、命令气泡、对话回复、清空）：序号变化即执行清空或弹气泡。 */
    const consumeSay = (): void => {
      if (!say || say.seq === prevSayRef.current) return;
      prevSayRef.current = say.seq;
      if (say.kind === 'clear') {
        hub.bubbles.clearWhisper();
        return;
      }
      hub.speak(say.text, say.image, say.url, say.duration, say.anim);
    };

    /** 点播动画：url 插播一段，名称走菜单同一套动作。 */
    const consumeAnimCue = (): void => {
      const card = hub.card;
      if (!animCue || animCue.seq === prevAnimRef.current) return;
      prevAnimRef.current = animCue.seq;
      // facing 必须在换源前落地，并同步镜像 ref：换源在 onReady 里读它。
      // 未传 facing 时保持当前朝向，不回落 left。
      const explicitFacing = animCue.facing !== undefined;
      if (animCue.facing !== undefined) {
        card.actions.setFacing(animCue.facing);
        card.facingRef.current = animCue.facing;
      }
      // url 形态只插播一段：停漫游、换源播一次，播完由播完判定回正常动画链；
      // 不走菜单动作——裸地址没有文字类、移动类这些属性。
      if (animCue.url !== undefined) {
        hub.stopMotion();
        hub.ownAnim.setCueUrl(animCue.url);
        card.actions.setOnce(true);
        card.actions.bumpSeq(); // 同一地址连播两次时状态不变，递增序号强制再走一遍换源。
        card.actions.setAnim(animCue.url);
        card.actions.setCuePlayback(animCue.playbackId ?? null);
        return;
      }
      if (animCue.name === undefined) return;
      // keepFacing：显式指定的朝向优先于「文字类强制朝左」的镜像修正。
      hub.playByName(animCue.name, animCue.playbackId, explicitFacing);
    };

    /** 位置指令：交给位置跟随；用户正按着宠物时由驱动整拍丢弃。 */
    const consumePosCue = (): void => {
      if (!posCue || posCue.seq === prevPosRef.current) return;
      prevPosRef.current = posCue.seq;
      hub.posFollow.apply(posCue.x, posCue.y, posCue.mode, posCue.duration);
    };

    /** 抛掷指令：交给抛掷驱动。 */
    const consumeThrowCue = (): void => {
      if (!throwCue || throwCue.seq === prevThrowRef.current) return;
      prevThrowRef.current = throwCue.seq;
      hub.applyThrow(throwCue.angle, throwCue.power);
    };

    // 七条指令共用一个 effect：每条自带已消费守卫，只有变化的那条真正动手。
    // 分开写七个 effect 时每个渲染期闭包都要重新订阅，守卫与依赖说明也重复七遍。
    useEffect(() => {
      consumeWorkStatus();
      consumeSay();
      consumeAnimCue();
      consumePosCue();
      consumeThrowCue();
      // 守卫函数每渲染重建，但只读 ref 与当拍的 props；进依赖会让每个渲染都重跑七个守卫。
      // eslint-disable-next-line react-hooks/exhaustive-deps -- 变化原因只有这七个计数器
    }, [workStatusTick, sayTick, animCueTick, posCueTick, throwCueTick]);
  };
}
