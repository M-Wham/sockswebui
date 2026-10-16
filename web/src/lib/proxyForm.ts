// Upstream proxy add/edit form. The password is write-only: the form
// never holds the stored value; an empty field on edit means "keep the server's value".

import { isSensitiveHost } from './dialTargets';
import type { EndpointView, SessionCharset } from './endpointFilter';

export const SESSION_TOKEN = '{session}';

export type ProxyKind = 'socks5' | 'http';

export interface ProxyForm {
  name: string;
  kind: ProxyKind;
  host: string;
  port: string;
  username: string;
  /** Newly typed password only; '' means unchanged. */
  password: string;
  /** Explicitly remove the stored password. */
  clearPassword: boolean;
  /** From the server's password_set flag. */
  passwordSet: boolean;
  /** From the server's password_has_session flag. */
  passwordHasSession: boolean;
  /** "Rotating session" toggle. */
  sessionEnabled: boolean;
  sessionCharset: SessionCharset;
  sessionLength: string;
  /** Read-only, server-owned; never sent. */
  currentId: string;
}

export interface ProxyBody {
  name: string;
  kind: ProxyKind;
  host: string;
  port: number;
  username: string;
  password?: string;
  session?: { charset: SessionCharset; length: number };
}

export interface ProxyRequest {
  method: 'POST' | 'PUT';
  path: string;
  body: ProxyBody;
}

export function emptyProxyForm(): ProxyForm {
  return { name: '', kind: 'socks5', host: '', port: '', username: '', password: '', clearPassword: false, passwordSet: false,
    passwordHasSession: false, sessionEnabled: false, sessionCharset: 'alnum', sessionLength: '10', currentId: '' };
}

export function formFromEndpoint(e: EndpointView): ProxyForm {
  return {
    name: e.name ?? '',
    kind: e.kind === 'http' ? 'http' : 'socks5',
    host: e.host ?? '',
    port: e.port ? String(e.port) : '',
    username: e.username ?? '',
    password: '',
    clearPassword: false,
    passwordSet: e.password_set === true,
    passwordHasSession: e.password_has_session === true,
    sessionEnabled: e.session !== undefined,
    sessionCharset: e.session?.charset === 'digits' ? 'digits' : 'alnum',
    sessionLength: String(e.session?.length ?? 10),
    currentId: e.session?.current_id ?? '',
  };
}

export function passwordStatus(set: boolean): 'set' | 'not set' {
  return set ? 'set' : 'not set';
}

function parsePort(s: string): number {
  const t = s.trim();
  return /^\d+$/.test(t) ? Number(t) : 0;
}

/** Builds POST (new) or PUT (existing id) for /api/endpoints. */
export function buildProxyRequest(f: ProxyForm, id?: string): ProxyRequest {
  const body: ProxyBody = {
    name: f.name.trim(),
    kind: f.kind,
    host: f.host.trim(),
    port: parsePort(f.port),
    username: f.username.trim(),
  };
  if (f.password !== '') body.password = f.password;
  else if (f.clearPassword) body.password = '';
  if (f.sessionEnabled) {
    // current_id is server-owned and never sent.
    body.session = { charset: f.sessionCharset, length: parsePort(f.sessionLength) };
  }
  return id === undefined
    ? { method: 'POST', path: '/api/endpoints', body }
    : { method: 'PUT', path: `/api/endpoints/${encodeURIComponent(id)}`, body };
}

export interface ProxyFormErrors {
  username?: string;
  password?: string;
  session?: string;
  sessionLength?: string;
}

function count(s: string): number {
  return s.split(SESSION_TOKEN).length - 1;
}

/**
 * Client-side mirror of the server proxy rules for instant feedback; the server stays the authority.
 * Messages never contain the typed username or password.
 */
export function validateProxyForm(f: ProxyForm, editing = false): ProxyFormErrors {
  const errs: ProxyFormErrors = {};
  const user = f.username.trim();
  const retyped = f.password !== '';
  const keepsStored = editing && !retyped && !f.clearPassword;
  const pass = retyped ? f.password : '';
  const passTokens = retyped ? count(pass) : keepsStored && f.passwordHasSession ? 1 : 0;
  const tokens = count(user) + passTokens;

  if (user === SESSION_TOKEN) errs.username = 'The username cannot be only {session}; add the fixed part of your account name.';
  if (retyped && pass === SESSION_TOKEN) errs.password = 'The password cannot be only {session}; add the fixed part of the password.';

  if (f.sessionEnabled) {
    if (tokens === 0) errs.session = 'Rotating session needs exactly one {session} in the username or password.';
    else if (tokens > 1) errs.session = 'Use {session} exactly once across username and password.';
    const len = /^\d+$/.test(f.sessionLength.trim()) ? Number(f.sessionLength.trim()) : 0;
    if (len < 1 || len > 32) errs.sessionLength = 'Length must be between 1 and 32.';
    else if (f.kind === 'socks5') {
      const enc = new TextEncoder();
      const over = (s: string) => enc.encode(s).length - SESSION_TOKEN.length + len > 255;
      if (count(user) === 1 && over(user)) errs.username = 'The username is too long for SOCKS5 (255 bytes after the ID is added).';
      if (retyped && count(pass) === 1 && over(pass)) errs.password = 'The password is too long for SOCKS5 (255 bytes after the ID is added).';
    }
  } else if (tokens > 0) {
    errs.session = 'Remove {session} from the username and password, or turn on Rotating session.';
  }
  return errs;
}

export function hasErrors(e: ProxyFormErrors): boolean {
  return Object.keys(e).length > 0;
}

/**
 * Non-blocking warnings: the first proxy hop's own host is not filtered by
 * the egress deny table, so loopback, link-local and metadata hosts deserve a heads-up.
 */
export function proxyFormWarnings(f: ProxyForm): string[] {
  const host = f.host.trim();
  if (host !== '' && isSensitiveHost(host)) {
    return ['This host is a loopback, link-local, unspecified, multicast or cloud-metadata address. Saving is allowed, but check it is the proxy you intend.'];
  }
  return [];
}
