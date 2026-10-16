// WireGuard endpoint secrets editor. Key values are write-only: they
// live only in the form object, are never rendered back, and are wiped after every save
// attempt (success or failure). Nothing here touches stores or browser storage.

import { ApiError } from './api';
import type { EndpointView } from './endpointFilter';

export interface WgSecretsForm {
  privateKey: string;
  presharedKey: string;
}

export interface WgSecretsBody {
  name: string;
  kind: 'wireguard';
  addresses?: string[];
  dns?: string[];
  peer_public_key?: string;
  endpoint?: string;
  allowed_ips?: string[];
  mtu?: number;
  mullvad_relay?: string;
  country?: string;
  /** Present only when the user typed it. */
  private_key?: string;
  /** Present only when the user typed it. */
  preshared_key?: string;
}

export interface WgSecretsRequest {
  method: 'PUT';
  path: string;
  body: WgSecretsBody;
}

export interface WgSecretsErrors {
  form?: string;
  privateKey?: string;
  presharedKey?: string;
}

export type Requester = (method: 'PUT', path: string, body: unknown) => Promise<unknown>;

export function emptyWgSecretsForm(): WgSecretsForm {
  return { privateKey: '', presharedKey: '' };
}

export function clearWgSecretsForm(f: WgSecretsForm): void {
  f.privateKey = '';
  f.presharedKey = '';
}

export function privateKeyHint(e: EndpointView): string {
  return e.private_key_set === true ? '' : 'private key missing';
}

export function validateWgSecrets(f: WgSecretsForm): WgSecretsErrors {
  if (f.privateKey.trim() === '' && f.presharedKey.trim() === '') {
    return { form: 'Enter a private key, a preshared key, or both.' };
  }
  return {};
}

/**
 * Builds PUT /api/endpoints/:id. The server rebuilds a WireGuard endpoint from the whole
 * body (only secrets are kept when omitted), so the stored non-secret settings are re-sent;
 * a secret is included only when typed.
 */
export function buildWgSecretsRequest(e: EndpointView, f: WgSecretsForm): WgSecretsRequest {
  const body: WgSecretsBody = { name: e.name, kind: 'wireguard' };
  if (e.addresses) body.addresses = e.addresses;
  if (e.dns) body.dns = e.dns;
  if (e.peer_public_key) body.peer_public_key = e.peer_public_key;
  if (e.endpoint) body.endpoint = e.endpoint;
  if (e.allowed_ips) body.allowed_ips = e.allowed_ips;
  if (e.mtu) body.mtu = e.mtu;
  if (e.mullvad_relay) body.mullvad_relay = e.mullvad_relay;
  if (e.country) body.country = e.country;
  const priv = f.privateKey.trim();
  const psk = f.presharedKey.trim();
  if (priv !== '') body.private_key = priv;
  if (psk !== '') body.preshared_key = psk;
  return { method: 'PUT', path: `/api/endpoints/${encodeURIComponent(e.id)}`, body };
}

export type SaveResult = { ok: true } | { ok: false; errors: WgSecretsErrors };

const FIELD: Record<string, keyof WgSecretsErrors> = { private_key: 'privateKey', preshared_key: 'presharedKey' };

/** Validates, sends, and always wipes the form afterwards. Error text never includes key text. */
export async function saveWgSecrets(e: EndpointView, f: WgSecretsForm, request: Requester): Promise<SaveResult> {
  const bad = validateWgSecrets(f);
  if (bad.form) return { ok: false, errors: bad };
  const req = buildWgSecretsRequest(e, f);
  clearWgSecretsForm(f); // the request body already holds the values; the form never does again
  try {
    await request(req.method, req.path, req.body);
    return { ok: true };
  } catch (err) {
    const errors: WgSecretsErrors = {};
    if (err instanceof ApiError) {
      for (const fe of err.fieldErrors) {
        const k = FIELD[fe.field];
        if (k) errors[k] = fe.message;
        else errors.form = `${fe.field}: ${fe.message}`;
      }
      if (!err.fieldErrors.length) errors.form = err.message;
    } else errors.form = 'could not save the keys';
    return { ok: false, errors };
  }
}
