// HTTP client. It returns the response envelope
// untouched, sends cookies same-origin, adds the CSRF token to mutations, and
// throws ApiRequestError (the ApiError shape) on failures. It never stores the
// session token (HttpOnly cookie) and keeps the CSRF token in memory only.
import type { ApiError, ApiResponse } from "./types";

type Method = "GET" | "POST" | "PATCH" | "PUT" | "DELETE";

export type RequestOptions = {
  method?: Method;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
};

const API_BASE: string = import.meta.env.VITE_API_BASE_URL ?? "/api/v1";
const MUTATING: ReadonlySet<Method> = new Set(["POST", "PATCH", "PUT", "DELETE"]);

export class ApiRequestError extends Error implements ApiError {
  code: string;
  fields: Record<string, string>;
  request_id: string;
  run_id?: string;
  status: number;

  constructor(e: ApiError, status: number) {
    super(e.message);
    this.name = "ApiRequestError";
    this.code = e.code;
    this.fields = e.fields;
    this.request_id = e.request_id;
    this.run_id = e.run_id;
    this.status = status;
  }
}

let csrfToken: string | null = null;
let csrfPending: Promise<string> | null = null;
const unauthorizedListeners = new Set<() => void>();

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

/** The app shell subscribes here to drop the user and open the login page on 401. */
export function onUnauthorized(listener: () => void): () => void {
  unauthorizedListeners.add(listener);
  return () => unauthorizedListeners.delete(listener);
}

/** Fetches a fresh CSRF token; call again after login (the secret changes with the session). */
export async function refreshCsrf(signal?: AbortSignal): Promise<string> {
  csrfPending ??= request<{ csrf_token: string }>("/auth/csrf", { signal })
    .then((res) => {
      const token = res?.data.csrf_token ?? "";
      csrfToken = token;
      return token;
    })
    .finally(() => {
      csrfPending = null;
    });
  return csrfPending;
}

/** Name check, not instanceof: fetch may throw a DOMException from another realm (jsdom, workers). */
export function isAbortError(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { name?: unknown }).name === "AbortError";
}

function networkError(): ApiRequestError {
  return new ApiRequestError(
    { code: "NETWORK_ERROR", message: "Tidak dapat menghubungi server.", fields: {}, request_id: "" },
    0,
  );
}

async function toError(res: Response): Promise<ApiRequestError> {
  const requestId = res.headers.get("X-Request-ID") ?? "";
  try {
    const body = (await res.json()) as { error?: Partial<ApiError> };
    const e = body.error;
    if (e && typeof e.code === "string") {
      return new ApiRequestError(
        {
          code: e.code,
          message: e.message ?? "",
          fields: e.fields ?? {},
          request_id: e.request_id ?? requestId,
          run_id: e.run_id,
        },
        res.status,
      );
    }
  } catch {
    // fall through: non-JSON error body
  }
  return new ApiRequestError(
    { code: "INTERNAL_ERROR", message: "Respons server tidak dapat dibaca.", fields: {}, request_id: requestId },
    res.status,
  );
}

export async function request<T>(
  path: string,
  options: RequestOptions = {},
): Promise<ApiResponse<T> | undefined> {
  const method = options.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json", ...options.headers };

  if (MUTATING.has(method)) {
    headers["X-CSRF-Token"] = csrfToken ?? (await refreshCsrf(options.signal));
  }
  let body: string | undefined;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body);
  }

  let res: Response;
  try {
    res = await fetch(API_BASE + path, { method, headers, body, signal: options.signal, credentials: "same-origin" });
  } catch (err) {
    if (isAbortError(err)) throw err; // caller cancelled
    throw networkError();
  }

  if (!res.ok) {
    const error = await toError(res);
    if (res.status === 401) {
      csrfToken = null;
      unauthorizedListeners.forEach((l) => l());
    } else if (error.code === "CSRF_REJECTED") {
      csrfToken = null; // next mutation fetches a new token
    }
    throw error;
  }
  if (res.status === 204) return undefined;

  try {
    return (await res.json()) as ApiResponse<T>;
  } catch {
    throw new ApiRequestError(
      { code: "INTERNAL_ERROR", message: "Respons server tidak dapat dibaca.", fields: {}, request_id: res.headers.get("X-Request-ID") ?? "" },
      res.status,
    );
  }
}
