// One shared /api/events connection for the whole app. The App shell, the Slots screen and the
// slot editor subscribe instead of each opening an EventSource. The hub keeps the reduced slot
// state so a screen that mounts later still sees the latest snapshot (the server sends
// `snapshot` only on connect). State honesty: nothing is assumed up; `connected` is true only
// between an open and the next error.

import { browserEventSource, connectEvents, type SseDeps } from './sse';
import { initialSlots, reduce, type SlotsState } from './slotsStore';

export interface LiveListener {
  /** Raw frame, after the hub applied it. */
  onFrame?: (event: string, data: unknown) => void;
  /** Slot state or connection changed. */
  onChange?: () => void;
  /** Stream closed for good (often an expired session). */
  onClosed?: () => void;
}

export function createLiveStream(open: (deps: SseDeps) => () => void) {
  const listeners = new Set<LiveListener>();
  let close: (() => void) | null = null;
  let slots = initialSlots();
  let connected = false;
  let gotSnapshot = false; // current stream delivered a snapshot

  const each = (fn: (l: LiveListener) => void) => [...listeners].forEach(fn);
  function setConnected(v: boolean) {
    connected = v; // always notify: a first error must still replace the unverified first check

    each((l) => l.onChange?.());
  }

  function start() {
    gotSnapshot = false;
    close = open({
      url: '/api/events',
      create: browserEventSource,
      onFrame(event, data) {
        slots = reduce(slots, event, data);
        if (event === 'snapshot') connected = gotSnapshot = true;
        each((l) => {
          l.onFrame?.(event, data);
          l.onChange?.();
        });
      },
      onOpen: () => setConnected(true),
      onError: () => setConnected(false),
      onClosed: () => each((l) => l.onClosed?.()),
    });
  }

  return {
    slots: () => slots,
    connected: () => connected,
    /**
     * fresh: reconnect when joining a stream that already delivered a snapshot, so a new snapshot arrives.
     * Needed because config changes (a slot created or edited) send no state frame, so the
     * cached state can lag; the Slots screen asks for fresh, as it did when it owned its stream.
     */
    subscribe(l: LiveListener, opts: { fresh?: boolean } = {}): () => void {
      listeners.add(l);
      if (listeners.size === 1) start();
      else if (opts.fresh && gotSnapshot) {
        close?.();
        start();
      }
      return () => {
        if (!listeners.delete(l) || listeners.size > 0) return;
        close?.();
        close = null;
        slots = initialSlots();
        connected = false;
      };
    },
  };
}

export const live = createLiveStream(connectEvents);
