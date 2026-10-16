// Thin REST client. Mutating requests carry X-CSRF-Token from the last
// login; any 401/403 drops the token and routes to the login screen.

import { navigate as hashNavigate } from './router';
import type { ExportRequest, ImportConfigResult } from './configTransfer';
import { filenameFromDisposition } from './configTransfer';
import { session as defaultSession, type Session } from './session';

export const CSRF_HEADER = 'X-CSRF-Token';

// Endpoint DTO additions for "Rotating session": `session` {charset, length} is
// sent; `current_id` is server-owned and only read; `password_has_session` labels the password
// as containing {session}. Types live in endpointFilter.ts.
export type { Session as EndpointSession, EndpointView } from './endpointFilter';

export interface FieldError {
  field: string;
  message: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly fieldErrors: FieldError[];
  /** Seconds from a Retry-After header (429), when present and numeric. */
  readonly retryAfter?: number;
  constructor(status: number, message: string, fieldErrors: FieldError[] = [], retryAfter?: number) {
    super(message);
    this.retryAfter = retryAfter;
    this.name = 'ApiError';
    this.status = status;
    this.fieldErrors = fieldErrors;
  }
}

export type Method = 'GET' | 'POST' | 'PUT' | 'DELETE';

export interface Settings {
  ui_bind_addr: string;
  /** Read-only. */
  app_version?: string;
  start_on_enable?: boolean;
  /** Read-only; never sent back in PUT. */
  ui_bind_effective?: string;
  ui_bind_source?: 'flag' | 'setting';
  health_defaults?: Record<string, unknown>;
  config_error: string;
}

export interface ApiDeps {
  fetch: typeof fetch;
  session: Session;
  navigate: (hash: string) => void;
}

export interface Api {
  request<T = unknown>(method: Method, path: string, body?: unknown): Promise<T>;
  setup(password: string): Promise<void>;
  login(password: string): Promise<void>;
  logout(): Promise<void>;
  getSettings(): Promise<Settings>;
  rotateEndpoint(id: string): Promise<RotateResult>;
  exportConfig(req: ExportRequest): Promise<{ blob: Blob; filename: string }>;
  importConfig(file: File, currentPassword: string, passphrase?: string): Promise<ImportConfigResult>;
}

export interface RotateResult {
  current_id: string;
  rebuilt_slots: number[];
}

const UNAUTHENTICATED = new Set(['/api/setup', '/api/login']);

function parseFieldErrors(v: unknown): FieldError[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (e): e is FieldError =>
      typeof e === 'object' && e !== null && typeof e.field === 'string' && typeof e.message === 'string',
  );
}

async function readJSON(res: Response): Promise<unknown> {
  const text = await res.text();
  if (text === '') return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

export function createApi(deps: ApiDeps): Api {
  const { session, navigate } = deps;

  function toLogin(): void {
    session.clear();
    navigate('#/login');
  }

  /** Sends the request; resolves with the Response only when it is ok, else throws ApiError. */
  async function send(method: Method, path: string, body?: unknown): Promise<Response> {
    const headers = new Headers({ Accept: 'application/json' });
    const multipart = body instanceof FormData; // browser sets the boundary header
    if (body !== undefined && !multipart) headers.set('Content-Type', 'application/json');
    if (method !== 'GET' && !UNAUTHENTICATED.has(path)) {
      const tok = session.token();
      if (!tok) {
        toLogin();
        throw new ApiError(401, 'login required');
      }
      headers.set(CSRF_HEADER, tok);
    }
    let res: Response;
    try {
      res = await deps.fetch(path, {
        method,
        headers,
        credentials: 'same-origin',
        body: body === undefined ? undefined : multipart ? body : JSON.stringify(body),
      });
    } catch {
      throw new ApiError(0, 'network error: the device did not respond');
    }
    if (res.ok) return res;
    const data = await readJSON(res);
    const obj = (typeof data === 'object' && data !== null ? data : {}) as Record<string, unknown>;
    const msg = typeof obj.error === 'string' ? obj.error : `request failed (${res.status})`;
    if (res.status === 409 && path === '/api/login' && msg === 'setup required') {
      navigate('#/setup');
    } else if (res.status === 401 || res.status === 403) {
      toLogin();
    }
    const ra = res.headers.get('Retry-After');
    const retryAfter = ra !== null && /^\d+$/.test(ra.trim()) ? Number(ra.trim()) : undefined;
    throw new ApiError(res.status, msg, parseFieldErrors(obj.errors), retryAfter);
  }

  async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
    return (await readJSON(await send(method, path, body))) as T;
  }

  return {
    request,
    async setup(password) {
      await request('POST', '/api/setup', { password });
    },
    async login(password) {
      const out = await request<{ csrf_token?: unknown }>('POST', '/api/login', { password });
      if (typeof out?.csrf_token !== 'string' || out.csrf_token === '') {
        throw new ApiError(500, 'login response had no CSRF token');
      }
      session.setToken(out.csrf_token);
    },
    async logout() {
      try {
        await request('POST', '/api/logout');
      } finally {
        toLogin();
      }
    },
    getSettings: () => request<Settings>('GET', '/api/settings'),
    async exportConfig(req) {
      const res = await send('POST', '/api/config/export', req);
      return {
        blob: await res.blob(),
        filename: filenameFromDisposition(res.headers.get('Content-Disposition')),
      };
    },
    importConfig(file, currentPassword, passphrase) {
      const form = new FormData();
      form.append('file', file, file.name);
      form.append('current_password', currentPassword);
      if (passphrase) form.append('passphrase', passphrase);
      return request<ImportConfigResult>('POST', '/api/config/import', form);
    },
    rotateEndpoint: (id) => request<RotateResult>('POST', `/api/endpoints/${encodeURIComponent(id)}/rotate`),
  };
}

export const api: Api = createApi({
  fetch: (input, init) => globalThis.fetch(input, init),
  session: defaultSession,
  navigate: hashNavigate,
});
