// One-shot notice carried across a hash navigation (e.g. setup → login).
let pending = '';

export function setFlash(msg: string): void {
  pending = msg;
}

/** Returns the pending notice once, then clears it. */
export function takeFlash(): string {
  const m = pending;
  pending = '';
  return m;
}
