/**
 * 表现冻结门（G2）。
 *
 * 见 docs/Refactor/H5_TARGET_BLUEPRINT.md 第 2 节。重构期间任何 DOM 结构、
 * className、inline style 或文案变化都必须让这里的快照产生 diff。
 *
 * 快照以文件形式提交进 git（`__dom__/*.html`），所以"表现变了"等价于
 * "快照文件出现 git diff" —— 评审时肉眼可查，不需要跑浏览器。
 */
import { render } from '@testing-library/react';
import type { ReactElement } from 'react';

/** 冻结时钟。所有快照都在这一刻渲染，避免倒计时/相对时间抖动。 */
export const SNAPSHOT_NOW_MS = 1_760_000_000_000;

/**
 * React 内部生成的 useId 值不稳定，替换成占位符。
 * 其余内容一律保持原样 —— 包括文本节点里的空白，因为它会影响渲染。
 */
function stripUnstableIds(markup: string): string {
  return markup
    .replace(/:r[0-9a-z]+:/g, ':rID:')
    .replace(/«r[0-9a-z]+»/g, '«rID»')
    .replace(/_r_[0-9a-z]+_/g, '_r_ID_');
}

/**
 * 把 HTML 按标签换行，便于 review 快照 diff。
 * 只在标签之间插换行，不触碰标签内部与文本节点内容。
 */
function formatForReview(markup: string): string {
  return markup.replace(/></g, '>\n<');
}

/** 渲染组件并返回归一化后的 DOM 字符串。 */
export function serializeMarkup(container: HTMLElement): string {
  return `${formatForReview(stripUnstableIds(container.innerHTML))}\n`;
}

/** 渲染无需等待异步状态的组件，并返回归一化后的 DOM 字符串。 */
export function renderMarkup(element: ReactElement): string {
  return serializeMarkup(render(element).container);
}
