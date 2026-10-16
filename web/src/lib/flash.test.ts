import { expect, it } from 'vitest';
import { setFlash, takeFlash } from './flash';

it('flash is returned once', () => {
  setFlash('hello');
  expect(takeFlash()).toBe('hello');
  expect(takeFlash()).toBe('');
});
