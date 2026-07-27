import { act, render, waitFor } from '@testing-library/react';
import type { ReactElement } from 'react';

export const SNAPSHOT_NOW_MS = 1_760_000_000_000;

function stripUnstableIds(markup: string) {
  return markup
    .replace(/:r[0-9a-z]+:/g, ':rID:')
    .replace(/«r[0-9a-z]+»/g, '«rID»')
    .replace(/_r_[0-9a-z]+_/g, '_r_ID_');
}

function formatForReview(markup: string) {
  return markup.replace(/></g, '>\n<');
}

export async function renderSettledMarkup(element: ReactElement, readySelector?: string) {
  const view = render(element);

  // Let resolved request mocks and their React state updates drain before we
  // decide that a route is ready. Some pages do not render aria-busy during
  // the first effect turn, so checking it immediately can freeze a loading
  // state instead of the settled page.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

  await waitFor(() => {
    if (readySelector && !view.container.querySelector(readySelector)) {
      throw new Error(`页面尚未渲染 ${readySelector}`);
    }
    if (view.container.querySelector('[aria-busy="true"]')) {
      throw new Error('页面仍在加载');
    }
  });
  return `${formatForReview(stripUnstableIds(view.container.innerHTML))}\n`;
}
