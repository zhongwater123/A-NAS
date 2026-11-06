// Vendored from PC2005-cloud/dsh-pet 10f6c868b811 (MIT, see LICENSE).
/**
 * 气泡单槽：余额、说话、工作状态三种气泡的显隐、停留计时与优先级选择。
 * 不负责何时点亮与动画联动：tick 判定、抽动画、打日志都在 PetCard；也不负责外观，哑组件在 client/bubble.ts。
 * 依赖注入的 react 与两个气泡组件；三个停留计时器都归本模块清理。
 * 不变量：同一时刻只渲染一个气泡，优先级依次为终态任务、说话、余额、非终态任务。
 * @module dsh-pet/src/client/pet/card/bubble-slots
 */
import {
  BUBBLE_HOLD_IDLE,
  reduceBubbleHold,
  type BubbleHoldEvent,
  type BubbleHoldState,
} from '../../../shared/bubble-hold';
import { BUBBLE_DURATION_MS } from '../timings';
import type { Dispatch, ReactNode, SetStateAction } from 'react';
import type * as ReactNS from 'react';
import type { jsx } from 'react/jsx-runtime';

/** 当前该挂哪个气泡；`none` 表示哪个条件都不成立。 */
export type BubbleSlot = 'todo' | 'work-terminal' | 'whisper' | 'work' | 'none';

/** 优先级判定的输入：全部是渲染期的既成事实，判定本身不产生副作用。 */
export interface BubbleSlotInput {
  /** 待办面板是否打开；打开时独占槽位，其余气泡一律让位。 */
  todoOn: boolean;
  /** 工作状态是否为终态（success / error）。 */
  workTerminal: boolean;
  /** 工作气泡是否点亮。 */
  workOn: boolean;
  /** 工作气泡文本。 */
  workText: string | null;
  /** 配置是否启用工作状态联动。 */
  workStatusEnabled: boolean;
  /** 说话气泡是否点亮。 */
  whisperOn: boolean;
  /** 说话气泡文本。 */
  whisperText: string | null;
}

/**
 * 决定当前挂哪个气泡。
 *
 * 终态任务只活 10s，所以不让位；非终态常驻，让位 10s 没有损失。
 *
 * @param input - 三种气泡的显隐与开关。
 * @returns 命中的槽位；都不成立时为 `none`。
 */
export function decideBubbleSlot(input: BubbleSlotInput): BubbleSlot {
  // 面板是用户主动打开的窗口：它占槽时其余气泡一律让位，关掉后按原规则恢复。
  if (input.todoOn) return 'todo';
  if (input.workTerminal && input.workOn && input.workText && input.workStatusEnabled) return 'work-terminal';
  if (input.whisperOn && input.whisperText) return 'whisper';
  if (input.workOn && input.workText && input.workStatusEnabled) return 'work';
  return 'none';
}

/** 说话气泡组件；配图二选一，props 与 `client/bubble.ts` 的 WhisperBubble 一致。 */
export type WhisperBubbleComponent = (props: {
  text: string;
  image?: string;
  url?: string;
  assetRoot?: string;
  on: boolean;
  /** 可点时的回调；工作气泡用它跳会话，缺省即不可点。 */
  onClick?: (e: ReactNS.MouseEvent<HTMLDivElement>) => void;
}) => ReactNode;

/** 注入的 react 能力。 */
export interface BubbleSlotsRuntime {
  /** 创建元素。 */
  h: typeof jsx;
  /** 状态。 */
  useState: <T>(init: T) => [T, Dispatch<SetStateAction<T>>];
  /** 引用。 */
  useRef: <T>(initial: T) => ReactNS.MutableRefObject<T>;
  /** 副作用；用于卸载时清计时器。 */
  useEffect: (effect: ReactNS.EffectCallback, deps?: ReactNS.DependencyList) => void;
}

/** 待办面板气泡组件；props 与 `client/todo-bubble.ts` 的 TodoBubble 一致。 */
export type TodoBubbleComponent = (props: { on: boolean; onClose: () => void }) => ReactNode;

/** 三种气泡组件。 */
export interface BubbleComponents {
  /** 待办面板气泡。 */
  todoBubble: TodoBubbleComponent;
  /** 说话气泡。 */
  whisperBubble: WhisperBubbleComponent;
}

/** 每次渲染由 PetCard 提供的视图输入。 */
export interface BubbleSlotsInput {
  /** 配置是否启用工作状态联动。 */
  workStatusEnabled: boolean;
  /** 工作状态是否处于终态。 */
  workTerminal: boolean;
  /** 当前展示条目所属会话；有值才把工作气泡做成可点。 */
  workSessionId?: string | null;
  /** 点工作气泡时跳过去；缺省即不可点（网页端直接切，桌面端由容器转发）。 */
  onFocusSession?: (sessionId: string) => void;
  /** 说话气泡配图的素材根。 */
  assetRoot?: string;
}

/** 气泡单槽对外的读写面。 */
export interface BubbleSlots {
  /** 点亮说话气泡；duration 为 0 时不改动当前气泡，undefined 用默认时长。 */
  showWhisper(text: string, parts: { image?: string; url?: string }, duration?: number): void;
  /** 收起说话气泡；不碰动画与另两种气泡。 */
  clearWhisper(): void;
  /** 更新工作气泡文本，显隐不动。 */
  updateWorkText(text: string | null): void;
  /** 点亮工作气泡；terminal 决定 10s 后自动收起，非终态常驻。 */
  lightWork(terminal: boolean): void;
  /** 收起工作气泡但保留文本。 */
  hideWork(): void;
  /** 回空闲：收起工作气泡并清文本。 */
  resetWork(): void;
  /** 打开待办面板：它独占槽位，其余气泡让位。 */
  showTodo(): void;
  /** 收起待办面板，按原优先级恢复其余气泡。 */
  hideTodo(): void;
  /** 开关待办面板：已开则收起，供快捷键使用。 */
  toggleTodo(): void;
  /** 按优先级渲染当前该挂的气泡；无气泡时为 null。 */
  node(): ReactNode;
}

/**
 * 制造气泡单槽的 hook，每个 PetCard 调一次。
 *
 * @param rt - 注入的 react 能力。
 * @param components - 余额与说话气泡组件。
 * @returns 接收视图输入的 hook；钩子内部的状态与计时器随卡片卸载一并清理。
 */
export function createBubbleSlots(rt: BubbleSlotsRuntime, components: BubbleComponents) {
  const { h, useState, useRef, useEffect } = rt;
  const TodoBubble = components.todoBubble;
  const WhisperBubble = components.whisperBubble;

  return function useBubbleSlots(input: BubbleSlotsInput): BubbleSlots {
    // 说话气泡：显隐、文本与配图（池内名与完整地址二选一）。收起时机在 bubbleHoldRef：
    // 语音合成期间挂起计时，播完再留一小段尾巴（见 shared/bubble-hold）。
    const [whisperOn, setWhisperOn] = useState(false);
    const [whisperText, setWhisperText] = useState<string | null>(null);
    const [whisperImage, setWhisperImage] = useState<string | undefined>(undefined);
    const [whisperUrl, setWhisperUrl] = useState<string | undefined>(undefined);
    const whisperHoldRef = useRef<BubbleHoldState>(BUBBLE_HOLD_IDLE);
    const whisperTextRef = useRef<string | null>(null);
    const whisperTimerRef = useRef<number | null>(null);
    // 待办面板：独占槽位，没有停留计时，显隐只由用户开关。
    const [todoOn, setTodoOn] = useState(false);
    // 工作状态气泡：显隐与文本。
    const [workOn, setWorkOn] = useState(false);
    const [workText, setWorkText] = useState<string | null>(null);
    const workTimerRef = useRef<number | null>(null);

    // 卸载后不再 setState：三个计时器都在这里清。
    useEffect(
      () => () => {
        if (whisperTimerRef.current !== null) window.clearTimeout(whisperTimerRef.current);
        if (workTimerRef.current !== null) window.clearTimeout(workTimerRef.current);
      },
      [],
    );

    /** 推进说话气泡的收起计划，并按新计划重排计时器。 */
    const applyWhisperHold = (event: BubbleHoldEvent): void => {
      const next = reduceBubbleHold(whisperHoldRef.current, event);
      if (next === whisperHoldRef.current) return; // 同引用 = 无事发生，别白白重排计时器
      whisperHoldRef.current = next;
      setWhisperOn(next.on);
      if (whisperTimerRef.current !== null) window.clearTimeout(whisperTimerRef.current);
      whisperTimerRef.current = null;
      if (next.delayMs === null) return;
      whisperTimerRef.current = window.setTimeout(() => {
        whisperTimerRef.current = null;
        applyWhisperHold({ kind: 'hide' });
      }, next.delayMs);
    };

    const showWhisper = (text: string, parts: { image?: string; url?: string }, duration?: number) => {
      if (duration === 0) return; // 只联动动画、不改气泡时走这里。
      setWhisperText(text);
      whisperTextRef.current = text;
      setWhisperImage(parts.image);
      setWhisperUrl(parts.url);
      applyWhisperHold({ durationMs: duration ?? BUBBLE_DURATION_MS, kind: 'show' });
    };

    const clearWhisper = () => {
      whisperTextRef.current = null;
      setWhisperText(null);
      setWhisperImage(undefined);
      applyWhisperHold({ kind: 'clear' });
    };

    const lightWork = (terminal: boolean) => {
      setWorkOn(true);
      if (workTimerRef.current !== null) window.clearTimeout(workTimerRef.current);
      // 非终态常驻，不设自动收起。
      workTimerRef.current = terminal ? window.setTimeout(() => setWorkOn(false), BUBBLE_DURATION_MS) : null;
    };

    const hideWork = () => {
      if (workTimerRef.current !== null) window.clearTimeout(workTimerRef.current);
      workTimerRef.current = null;
      setWorkOn(false);
    };

    const resetWork = () => {
      hideWork();
      setWorkText(null);
    };

    const showTodo = (): void => setTodoOn(true);
    const hideTodo = (): void => setTodoOn(false);
    const toggleTodo = (): void => setTodoOn((on) => !on);

    const node = (): ReactNode => {
      const slot = decideBubbleSlot({
        todoOn,
        workTerminal: input.workTerminal,
        workOn,
        workText,
        workStatusEnabled: input.workStatusEnabled,
        whisperOn,
        whisperText,
      });
      if (slot === 'todo') return h(TodoBubble, { on: todoOn, onClose: hideTodo });
      if (slot === 'whisper') {
        return whisperText === null
          ? null
          : h(WhisperBubble, {
              text: whisperText,
              image: whisperImage,
              url: whisperUrl,
              assetRoot: input.assetRoot,
              on: whisperOn,
            });
      }
      // 终态与非终态共用同一个组件，差别只在停留时长与优先级。
      if (slot === 'work' || slot === 'work-terminal') {
        if (workText === null) return null;
        const sessionId = input.workSessionId ?? '';
        const onFocus = input.onFocusSession;
        // 拿不到会话就别做成可点：点下去没有任何去处，比不可点更糟。
        const onClick = sessionId !== '' && onFocus !== undefined ? () => onFocus(sessionId) : undefined;
        return h(WhisperBubble, {
          text: workText,
          on: workOn,
          ...(onClick === undefined ? {} : { onClick }),
        });
      }
      return null;
    };

    return {
      clearWhisper,
      hideTodo,
      hideWork,
      lightWork,
      node,
      resetWork,
      showTodo,
      showWhisper,
      toggleTodo,
      updateWorkText: setWorkText,
    };
  };
}
