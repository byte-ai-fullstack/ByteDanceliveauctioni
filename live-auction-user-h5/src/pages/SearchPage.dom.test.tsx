// @vitest-environment jsdom
import { render, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { serializeMarkup } from '../test/domSnapshot';
import SearchPage from './SearchPage';

const dependencies = vi.hoisted(() => ({
  listPublicRooms: vi.fn(),
  listBuyerSuggestions: vi.fn(),
  consultBuyer: vi.fn(),
  clearSearchAIState: vi.fn(),
  readSearchAIStateForRestore: vi.fn(),
  saveSearchAIStateForRoomReturn: vi.fn(),
}));

vi.mock('../features/auction/api/auctionApi', () => ({
  listPublicRooms: dependencies.listPublicRooms,
  listBuyerSuggestions: dependencies.listBuyerSuggestions,
  consultBuyer: dependencies.consultBuyer,
}));

vi.mock('../features/search/model/searchAIState', () => ({
  clearSearchAIState: dependencies.clearSearchAIState,
  readSearchAIStateForRestore: dependencies.readSearchAIStateForRestore,
  saveSearchAIStateForRoomReturn: dependencies.saveSearchAIStateForRoomReturn,
}));

beforeEach(() => {
  window.history.replaceState({}, '', '/home/search');
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.spyOn(Math, 'random').mockReturnValue(0.5);
  dependencies.readSearchAIStateForRestore.mockReturnValue({ query: '', reply: null, scrollY: 0 });
  dependencies.listPublicRooms.mockResolvedValue([]);
  dependencies.listBuyerSuggestions.mockResolvedValue({
    suggestions: [],
    fallbackUsed: false,
  });
});

describe('SearchPage 表现冻结', () => {
  it('展示默认搜索入口、历史和榜单', async () => {
    const { container } = render(<SearchPage />);

    await waitFor(() => {
      expect(dependencies.listPublicRooms).toHaveBeenCalledTimes(1);
      expect(dependencies.listBuyerSuggestions).toHaveBeenCalledWith(6);
    });

    expect(container.textContent).toContain('找好拍');
    expect(container.querySelector('[aria-label="搜索历史"]')).not.toBeNull();
    expect(container.textContent).toContain('拍品热榜');
    await expect(serializeMarkup(container)).toMatchFileSnapshot('./__dom__/search-default.html');
  });
});
