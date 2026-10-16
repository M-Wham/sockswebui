import { afterEach, describe, expect, it, vi } from 'vitest';
import { THEMES, THEME_LABELS, applyTheme, loadTheme } from './theme';

function stubStorage(impl: Partial<Storage>) {
  vi.stubGlobal('localStorage', impl);
}

afterEach(() => vi.unstubAllGlobals());

describe('theme', () => {
  it('lists the four palettes', () => {
    expect([...THEMES]).toEqual(['forest', 'tide', 'wine', 'forest-light']);
  });

  it('labels every theme', () => {
    for (const t of THEMES) expect(THEME_LABELS[t]).toBeTruthy();
    expect(THEME_LABELS['forest-light']).toBe('Forest light');
  });

  it('defaults to forest when storage is empty', () => {
    stubStorage({ getItem: () => null });
    expect(loadTheme()).toBe('forest');
  });

  it('defaults to forest for an unknown value', () => {
    stubStorage({ getItem: () => 'neon' });
    expect(loadTheme()).toBe('forest');
  });

  it('defaults to forest when getItem throws', () => {
    stubStorage({
      getItem: () => {
        throw new Error('blocked');
      },
    });
    expect(loadTheme()).toBe('forest');
  });

  it('returns forest-light when stored', () => {
    stubStorage({ getItem: () => 'forest-light' });
    expect(loadTheme()).toBe('forest-light');
  });

  it('applyTheme sets the dataset and stores the choice', () => {
    const doc = { documentElement: { dataset: {} as Record<string, string> } };
    const setItem = vi.fn();
    vi.stubGlobal('document', doc);
    stubStorage({ setItem });
    applyTheme('tide');
    expect(doc.documentElement.dataset.theme).toBe('tide');
    expect(setItem).toHaveBeenCalledWith('sockswebui.theme', 'tide');
  });

  it('applyTheme still sets the dataset when setItem throws', () => {
    const doc = { documentElement: { dataset: {} as Record<string, string> } };
    vi.stubGlobal('document', doc);
    stubStorage({
      setItem: () => {
        throw new Error('full');
      },
    });
    applyTheme('wine');
    expect(doc.documentElement.dataset.theme).toBe('wine');
  });
});
