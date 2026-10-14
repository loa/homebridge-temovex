import { isTemperatureName, type TemperatureName, temperatureDevices } from './devices.js';

const DEFAULT_FRESHNESS_MS = 1000;
const REQUEST_TIMEOUT_MS = 20_000;

export class ProxyError extends Error {
  readonly status?: number;

  constructor(message: string, status?: number) {
    super(message);
    this.name = 'ProxyError';
    this.status = status;
  }
}

/**
 * Accepts the proxy base URL, which must include http:// or https://.
 */
export function parseProxyUrl(input: unknown): URL {
  if (typeof input !== 'string' || input.trim() === '') {
    throw new Error('url is required and must include a protocol, for example http://192.168.1.10:8080');
  }
  const trimmed = input.trim();
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    if (/^https?:\/\//i.test(trimmed)) {
      throw new Error('url must include a host, for example http://192.168.1.10:8080');
    }
    throw new Error('url must include a protocol, for example http://192.168.1.10:8080');
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error('url protocol must be http or https');
  }
  if (url.hostname === '') {
    throw new Error('url must include a host');
  }
  return url;
}

/** Host label safe to log. Userinfo and query strings are omitted. */
export function proxyLabel(url: URL): string {
  const port = url.port === '' ? '' : `:${url.port}`;
  const path = url.pathname === '/' ? '' : url.pathname.replace(/\/$/, '');
  return `${url.protocol}//${url.hostname}${port}${path}`;
}

export function failureText(error: unknown): string {
  if (error instanceof Error && error.message !== '') {
    return error.message;
  }
  return String(error);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function httpError(action: string, status: number, body: string): string {
  let detail = '';
  try {
    const parsed = JSON.parse(body) as unknown;
    if (isRecord(parsed) && typeof parsed.error === 'string' && parsed.error !== '') {
      detail = `: ${parsed.error}`;
    }
  } catch {
    detail = '';
  }
  return `${action} failed (${status})${detail}`;
}

export function parseTemperatures(body: string): Map<TemperatureName, number> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    throw new ProxyError('temperature response was not JSON');
  }
  if (!isRecord(parsed) || parsed.unit !== 'celsius' || !Array.isArray(parsed.temperatures)) {
    throw new ProxyError('temperature response is missing celsius readings');
  }

  const values = new Map<TemperatureName, number>();
  for (const entry of parsed.temperatures) {
    if (!isRecord(entry) || typeof entry.name !== 'string' || typeof entry.celsius !== 'number' || !Number.isFinite(entry.celsius)) {
      throw new ProxyError('temperature response has an invalid reading');
    }
    if (!isTemperatureName(entry.name)) {
      continue;
    }
    if (values.has(entry.name)) {
      throw new ProxyError(`temperature response repeats ${entry.name}`);
    }
    values.set(entry.name, entry.celsius);
  }
  for (const device of temperatureDevices) {
    if (!values.has(device.name)) {
      throw new ProxyError(`temperature response is missing ${device.name}`);
    }
  }
  return values;
}

/**
 * Reads and writes the modbus-proxy. Temperature reads happen when HomeKit
 * asks. Overlapping reads share one request, and a short freshness window
 * collapses the rest of that same refresh.
 */
export class ProxyClient {
  private cached?: { at: number; generation: number; values: ReadonlyMap<TemperatureName, number> };
  private inflight?: Promise<ReadonlyMap<TemperatureName, number>>;
  private generation = 0;

  constructor(
    private readonly baseUrl: URL,
    private readonly freshnessMs = DEFAULT_FRESHNESS_MS,
  ) {}

  temperatures(): Promise<ReadonlyMap<TemperatureName, number>> {
    const cached = this.cached;
    if (cached && cached.generation === this.generation && Date.now() - cached.at < this.freshnessMs) {
      return Promise.resolve(cached.values);
    }
    if (!this.inflight) {
      const generation = this.generation;
      const pending = this.read().then((values) => {
        if (generation === this.generation) {
          this.cached = { at: Date.now(), generation, values };
        }
        return values;
      }).finally(() => {
        if (this.inflight === pending) {
          this.inflight = undefined;
        }
      });
      this.inflight = pending;
    }
    return this.inflight;
  }

  async setSetpoint(celsius: number): Promise<void> {
    if (!Number.isFinite(celsius)) {
      throw new ProxyError('setpoint must be a finite number');
    }
    this.generation += 1;
    this.cached = undefined;

    let response: Response;
    try {
      response = await fetch(this.endpoint('/temperatures/set'), {
        method: 'PUT',
        headers: {
          accept: 'application/json',
          'content-type': 'application/json',
        },
        body: JSON.stringify({ celsius }),
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch (error) {
      throw new ProxyError(`failed to reach the proxy: ${failureText(error)}`);
    }
    const text = await response.text();
    if (!response.ok) {
      throw new ProxyError(httpError('set supply-air setpoint', response.status, text), response.status);
    }
  }

  private async read(): Promise<ReadonlyMap<TemperatureName, number>> {
    let response: Response;
    try {
      response = await fetch(this.endpoint('/temperatures'), {
        method: 'GET',
        headers: { accept: 'application/json' },
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch (error) {
      throw new ProxyError(`failed to reach the proxy: ${failureText(error)}`);
    }
    const text = await response.text();
    if (!response.ok) {
      throw new ProxyError(httpError('read temperatures', response.status, text), response.status);
    }
    return parseTemperatures(text);
  }

  private endpoint(path: string): string {
    const url = new URL(this.baseUrl.href);
    const prefix = url.pathname.replace(/\/+$/, '');
    url.pathname = `${prefix}${path}`;
    url.search = '';
    url.hash = '';
    return url.toString();
  }
}
