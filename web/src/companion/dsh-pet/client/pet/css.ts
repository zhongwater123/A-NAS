// Vendored from PC2005-cloud/dsh-pet 10f6c868b811 (MIT, see LICENSE).
/**
 * 宠物页面的内联样式与注入：只有伪类、过渡、指针手势与光标这类行内 style 表达不了的东西。
 * 菜单与悬浮提示的样式与桌面端共用同一份（`shared/menu`、`shared/tooltip`），不在这里抄第二份。
 * 注入按 `data-plugin-css` 去重：同一页面重复挂载不会长第二份样式。
 * @module dsh-pet/src/client/pet/css
 */
import { TOOLTIP_CSS } from '../../shared/tooltip';

/** 宠物页面样式表。 */
const PET_CSS = [
  // A-NAS: the page sets --dsh-pet-z to put the pet among its own layers (above the desktop's icons,
  // below its windows); dsh-pet's 40 stays the default.
  '.dsh-pet-root{position:fixed;z-index:var(--dsh-pet-z,40);pointer-events:none;user-select:none}',
  '.dsh-pet-root[data-corner="bottom-right"]{right:var(--dsh-pet-mx,24px);bottom:var(--dsh-pet-my,0)}',
  '.dsh-pet-root[data-corner="bottom-left"]{left:var(--dsh-pet-mx,24px);bottom:var(--dsh-pet-my,0)}',
  '.dsh-pet-root[data-corner="top-right"]{right:var(--dsh-pet-mx,24px);top:var(--dsh-pet-my,0)}',
  '.dsh-pet-root[data-corner="top-left"]{left:var(--dsh-pet-mx,24px);top:var(--dsh-pet-my,0)}',
  '.dsh-pet-stage{position:relative;width:var(--dsh-pet-size,462px);height:calc(var(--dsh-pet-size,462px)*9/16);pointer-events:none}',
  // A-NAS: without dsh-pet's alpha fix, an SVG filter over every frame of the video: on the NAS, which
  // may draw without a GPU, it is likely to cost CPU on every frame.
  '.dsh-pet-video{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;pointer-events:none;opacity:0;transition:opacity .18s ease;transform-origin:center}',
  '.dsh-pet-video.is-front{opacity:1}',
  // 触屏拖拽必需：缺 touch-action:none 时浏览器会把手势判成滚动并触发 pointercancel；
  // 两条 -webkit-* 抑制长按选中与 iOS 长按系统菜单。
  // A-NAS: the plain grab cursors; A-NAS does not serve dsh-pet's cursor images.
  '.dsh-pet-hit{position:absolute;pointer-events:auto;cursor:grab;z-index:1;touch-action:none;-webkit-user-select:none;-webkit-touch-callout:none}',
  '.dsh-pet-hit.dragging{cursor:grabbing}',
  '@media (prefers-reduced-motion: reduce){.dsh-pet-video{transition:none}}',
  // 悬浮提示样式，与桌面注入同一份。
  TOOLTIP_CSS,
].join('\n');

/** 注入去重用的标记值，写在 `<style>` 的 `data-plugin-css` 上。 */
const cssTag = 'dsh-pet/style.css';

/** 注入宠物页面样式，每页只注入一次；没有 document 时静默返回。 */
export function injectPetCss(): void {
  if (typeof document === 'undefined') return;
  if (document.querySelector('style[data-plugin-css="' + cssTag + '"]') !== null) return;
  const tag = document.createElement('style');
  tag.dataset.plugin = 'dsh-pet';
  tag.dataset.pluginCss = cssTag;
  tag.textContent = PET_CSS;
  document.head.appendChild(tag);
}
