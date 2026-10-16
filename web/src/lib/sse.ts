// SSE client for GET /api/events. The browser's EventSource retries a dropped
// stream by itself; when the stream is closed for good (e.g. a 401), onClosed lets the
// caller check the session, and a fresh EventSource is opened after a capped backoff.
// The server sends a `snapshot` frame on every connect, which replaces client state.

export const FRAME_EVENTS = ['snapshot', 'state', 'health', 'throughput'] as const;

const CLOSED = 2;
const MIN_DELAY_MS = 1000;
const MAX_DELAY_MS = 30_000;

export interface EventSourceLike {
  readyState: number;
  onopen: (() => void) | null;
  onerror: (() => void) | null;
  addEventListener(type: string, fn: (e: { data: string }) => void): void;
  close(): void;
}

export interface SseDeps {
  url: string;
  create: (url: string) => EventSourceLike;
  onFrame: (event: string, data: unknown) => void;
  onClosed?: () => void;
  /** Stream (re)connected. */
  onOpen?: () => void;
  /** Any stream error, including transient ones the browser retries itself. */
  onError?: () => void;
  schedule?: (fn: () => void, ms: number) => unknown;
  cancel?: (handle: unknown) => void;
}

/** Opens the stream; returns a function that closes it for good. */
export function connectEvents(deps: SseDeps): () => void {
  const schedule = deps.schedule ?? ((fn, ms) => setTimeout(fn, ms));
  const cancel = deps.cancel ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  let es: EventSourceLike | null = null;
  let timer: unknown = null;
  let delay = MIN_DELAY_MS;
  let stopped = false;

  function open() {
    timer = null;
    if (stopped) return;
    const cur = deps.create(deps.url);
    es = cur;
    for (const ev of FRAME_EVENTS) {
      cur.addEventListener(ev, (e) => {
        let data: unknown;
        try {
          data = JSON.parse(e.data);
        } catch {
          return;
        }
        deps.onFrame(ev, data);
      });
    }
    cur.onopen = () => {
      delay = MIN_DELAY_MS;
      deps.onOpen?.();
    };
    cur.onerror = () => {
      if (!stopped && es === cur) deps.onError?.();
      if (stopped || cur.readyState !== CLOSED || es !== cur) return;
      cur.close();
      es = null;
      deps.onClosed?.();
      timer = schedule(open, delay);
      delay = Math.min(delay * 2, MAX_DELAY_MS);
    };
  }

  open();
  return () => {
    stopped = true;
    if (timer !== null) cancel(timer);
    es?.close();
    es = null;
  };
}

/** Browser EventSource factory (session cookie is same-origin). */
export function browserEventSource(url: string): EventSourceLike {
  return new EventSource(url) as unknown as EventSourceLike;
}
