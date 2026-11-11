// Vendored from PC2005-cloud/dsh-pet 10f6c868b811 (MIT, see LICENSE).
/**
 * 单只宠物的渲染输入：容器把 /state 叶子分发成这些字段，卡片只读不写。
 * 字段全部来自容器（轮询、配置、碰撞站场），卡片内部不订阅事件——它只负责渲染与响应，
 * 需要改 React 状态时走自己的 setter，不向上要回调。
 * @module dsh-pet/src/client/pet/card/props
 */
import type * as ReactNS from 'react';
import type { PetCollisionSlot } from '../../../shared/physics';
import type { LockState, PositionMode, SayCue, PetFacing, SleepState } from '../../../shared/state';
import type { WorkStatusSnapshot } from '../../../shared/work-status';
import type { RuntimePet } from '../types';
import type { Center } from '../motion/geometry';

/** 一张宠物卡的渲染输入。 */
export interface PetCardProps {
  /** 成品配置。 */
  cfg: RuntimePet;
  /** 工作状态快照。 */
  workStatus: WorkStatusSnapshot | null;
  /** 工作状态变化次数。 */
  workStatusTick: number;
  /** 点工作气泡时跳到它所属会话；缺省即气泡不可点（桌面端由容器转发）。 */
  onFocusSession?: (sessionId: string) => void;
  /** 本宠物的说话指令；kind 为 clear 时收起气泡，其余弹气泡。 */
  say?: SayCue & { seq: number };
  /** 说话指令变化次数。 */
  sayTick: number;
  /** 本宠物的点播指令：name 与 url 恰好一个；facing 未指定时保持当前朝向。 */
  animCue?: { name?: string; url?: string; seq: number; playbackId?: string; facing?: PetFacing };
  /** 点播指令变化次数。 */
  animCueTick: number;
  /** 本宠物的位置指令：x/y 是宠物中心的归一化坐标，duration 仅 line 模式有效。 */
  posCue?: { x: number; y: number; mode: PositionMode; duration: number; seq: number };
  /** 位置指令变化次数。 */
  posCueTick: number;
  /** 本宠物的抛掷指令：angle 单位为度，0 为正右、逆时针为正。 */
  throwCue?: { angle: number; power: number; seq: number };
  /** 抛掷指令变化次数。 */
  throwCueTick: number;
  /** 本宠物的占用锁；expiresAt 是唯一过期真相，宿主不会推「锁没了」。 */
  lock?: LockState;
  /** 锁叶子变化次数。 */
  lockTick: number;
  /** 本宠物的睡觉状态：三段名字由宿主挑定；睡着时随机链停住、自动来源闭嘴。 */
  sleep?: SleepState;
  /** 睡觉叶子变化次数。 */
  sleepTick: number;
  /** 宠物间碰撞站场。 */
  arena: ReactNS.MutableRefObject<{ slots: Record<string, PetCollisionSlot> }>;
  /** A-NAS: the page is idle, hidden or under the screen saver. The card freezes as under a lock and
   *  holds a flight too; the container sends it no commands meanwhile. */
  paused?: boolean;
  /** A-NAS: where the pet was left (its centre, as shares of the viewport), for the page to keep on this
   *  device; dsh-pet keeps it for the page's life only. */
  onPlace?: (center: Center) => void;
}
