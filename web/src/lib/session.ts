// Session state kept client-side: only the CSRF token (the session cookie is HttpOnly).
// The token lives in sessionStorage so a reload keeps working; storage may be missing or
// throw (private mode, blocked site data), so every access is guarded and an in-memory
// copy is the fallback.

const KEY = 'sockswebui.csrf';

export interface Session {
  token(): string | null;
  setToken(t: string): void;
  clear(): void;
}

export function createSession(storage: Storage | null): Session {
  let mem: string | null = null;
  try {
    mem = storage?.getItem(KEY) ?? null;
  } catch {
    mem = null;
  }
  return {
    token: () => mem,
    setToken(t) {
      mem = t;
      try {
        storage?.setItem(KEY, t);
      } catch {
        /* storage unavailable: memory only */
      }
    },
    clear() {
      mem = null;
      try {
        storage?.removeItem(KEY);
      } catch {
        /* storage unavailable */
      }
    },
  };
}

function browserStorage(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage;
  } catch {
    return null;
  }
}

export const session: Session = createSession(browserStorage());
