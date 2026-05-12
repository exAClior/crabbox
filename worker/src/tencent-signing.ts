import type { Env } from "./types";

const tc3Algorithm = "TC3-HMAC-SHA256";
const contentType = "application/json; charset=utf-8";
const signedHeaders = "content-type;host;x-tc-action";
const textEncoder = new TextEncoder();

export class TencentAPIError extends Error {
  readonly code: string;
  readonly httpStatus: number;
  readonly requestID?: string;

  constructor(
    action: string,
    code: string,
    message: string,
    httpStatus: number,
    requestID?: string,
  ) {
    super(`tencent ${action}: ${code}: ${message}`);
    this.name = "TencentAPIError";
    this.code = code;
    this.httpStatus = httpStatus;
    if (requestID) {
      this.requestID = requestID;
    }
  }
}

export interface TC3SignedHeaders {
  authorization: string;
  timestamp: string;
}

export async function tc3SignedHeaders(
  secretID: string,
  secretKey: string,
  service: string,
  host: string,
  _region: string,
  action: string,
  _version: string,
  payloadJSON: string,
  now: Date = new Date(),
): Promise<TC3SignedHeaders> {
  const timestamp = String(Math.trunc(now.getTime() / 1000));
  const date = now.toISOString().slice(0, 10);
  const hashedPayload = await sha256Hex(payloadJSON);
  const canonicalHeaders = `content-type:${contentType}\nhost:${host}\nx-tc-action:${action.toLowerCase()}\n`;
  const canonicalRequest = ["POST", "/", "", canonicalHeaders, signedHeaders, hashedPayload].join(
    "\n",
  );
  const credentialScope = `${date}/${service}/tc3_request`;
  const stringToSign = [
    tc3Algorithm,
    timestamp,
    credentialScope,
    await sha256Hex(canonicalRequest),
  ].join("\n");
  const secretDate = await hmacSHA256(`TC3${secretKey}`, date);
  const secretService = await hmacSHA256(secretDate, service);
  const secretSigning = await hmacSHA256(secretService, "tc3_request");
  const signature = hex(await hmacSHA256(secretSigning, stringToSign));
  return {
    authorization: `${tc3Algorithm} Credential=${secretID}/${credentialScope}, SignedHeaders=${signedHeaders}, Signature=${signature}`,
    timestamp,
  };
}

export interface TencentCallOptions {
  service: string;
  host?: string;
  version: string;
  action: string;
  region: string;
  payload?: Record<string, unknown>;
  timeoutMs?: number;
  token?: string;
  fetcher?: typeof fetch;
}

export async function tencentCall<T>(env: Env, opts: TencentCallOptions): Promise<T> {
  const secretID = env.TENCENT_SECRET_ID?.trim();
  const secretKey = env.TENCENT_SECRET_KEY?.trim();
  if (!secretID || !secretKey) {
    throw new Error("TENCENT_SECRET_ID and TENCENT_SECRET_KEY secrets are required");
  }
  const host = opts.host ?? `${opts.service}.tencentcloudapi.com`;
  const url = `https://${host}/`;
  const timeoutMs = Math.max(1_000, opts.timeoutMs ?? 30_000);
  const deadline = Date.now() + timeoutMs;
  let attempt = 0;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      const payloadJSON = JSON.stringify(opts.payload ?? {});
      // oxlint-disable-next-line eslint/no-await-in-loop -- Tencent retries must sign each sequential attempt with the exact body sent.
      const signed = await tc3SignedHeaders(
        secretID,
        secretKey,
        opts.service,
        host,
        opts.region,
        opts.action,
        opts.version,
        payloadJSON,
      );
      const controller = new AbortController();
      const remaining = Math.max(1_000, deadline - Date.now());
      const timer = setTimeout(() => controller.abort(), remaining);
      try {
        // oxlint-disable-next-line eslint/no-await-in-loop -- retry timing depends on the previous Tencent response.
        const response = await (opts.fetcher ?? fetch)(url, {
          method: "POST",
          headers: {
            Authorization: signed.authorization,
            "Content-Type": contentType,
            "X-TC-Action": opts.action,
            "X-TC-Version": opts.version,
            "X-TC-Timestamp": signed.timestamp,
            "X-TC-Region": opts.region,
            ...(opts.token ? { "X-TC-Token": opts.token } : {}),
          },
          body: payloadJSON,
          signal: controller.signal,
        });
        // oxlint-disable-next-line eslint/no-await-in-loop -- response body belongs to this sequential retry attempt.
        const text = await response.text();
        const parsed = parseTencentBody(text);
        const root = record(parsed["Response"]);
        const requestID = asString(root["RequestId"]);
        const error = record(root["Error"]);
        const code = asString(error["Code"]);
        if (!response.ok || code) {
          const apiError = new TencentAPIError(
            opts.action,
            code || `HTTP${response.status}`,
            asString(error["Message"]) || trimBody(text),
            response.status,
            requestID,
          );
          if (isRetryableTencentCallError(apiError) && Date.now() < deadline) {
            lastError = apiError;
            // oxlint-disable-next-line eslint/no-await-in-loop -- backoff is intentionally sequential.
            await sleep(backoffMs(attempt));
            attempt += 1;
            continue;
          }
          throw apiError;
        }
        return root as T;
      } finally {
        clearTimeout(timer);
      }
    } catch (error) {
      lastError = error;
      if (!isRetryableTransportError(error) || Date.now() >= deadline) {
        throw error;
      }
      // oxlint-disable-next-line eslint/no-await-in-loop -- backoff is intentionally sequential.
      await sleep(backoffMs(attempt));
      attempt += 1;
    }
  }
  throw lastError instanceof Error ? lastError : new Error(String(lastError));
}

function isRetryableTransportError(error: unknown): boolean {
  if (error instanceof TencentAPIError) {
    return isRetryableTencentCallError(error);
  }
  if (error instanceof DOMException && error.name === "AbortError") {
    return false;
  }
  return true;
}

function isRetryableTencentCallError(error: TencentAPIError): boolean {
  return (
    error.httpStatus === 429 ||
    error.httpStatus >= 500 ||
    error.code === "RequestLimitExceeded" ||
    error.code.startsWith("InternalError") ||
    error.code === "InternalServerError"
  );
}

async function sha256Hex(input: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", textEncoder.encode(input));
  return hex(new Uint8Array(digest));
}

async function hmacSHA256(key: string | Uint8Array, input: string): Promise<Uint8Array> {
  const rawKey = typeof key === "string" ? textEncoder.encode(key) : key;
  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    rawKey,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const signature = await crypto.subtle.sign("HMAC", cryptoKey, textEncoder.encode(input));
  return new Uint8Array(signature);
}

function hex(bytes: Uint8Array): string {
  return [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function parseTencentBody(text: string): Record<string, unknown> {
  try {
    return record(JSON.parse(text) as unknown);
  } catch {
    return { Response: { Error: { Code: "InvalidJSON", Message: trimBody(text) } } };
  }
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function asString(value: unknown): string {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function trimBody(text: string): string {
  const normalized = text.replace(/\s+/g, " ").trim();
  return normalized.length > 500 ? `${normalized.slice(0, 500)}...` : normalized;
}

function backoffMs(attempt: number): number {
  return Math.min(1_000 * 2 ** attempt, 5_000);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
