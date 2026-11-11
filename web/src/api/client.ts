// Typed API client. Cookies stay same-origin and HttpOnly; the CSRF token
// lives in memory only and is added to every mutation. Failures become
// ApiError; NETWORK_ERROR exists only on the client.
import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];

const baseUrl: string = import.meta.env.VITE_API_BASE_URL ?? "/api/v1";
const MUTATING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export class ApiError extends Error {
  readonly code: string;
  readonly fields: Record<string, string>;
  readonly requestId: string;
  readonly status: number;

  constructor(e: { code: string; message: string; fields?: Record<string, string>; request_id?: string }, status: number) {
    super(e.message);
    this.name = "ApiError";
    this.code = e.code;
    this.fields = e.fields ?? {};
    this.requestId = e.request_id ?? "";
    this.status = status;
  }
}

let csrfToken: string | null = null;
let csrfPending: Promise<string> | null = null;
const unauthorizedListeners = new Set<() => void>();

/** Forget the CSRF token; the next mutation fetches the token of the current session. */
export function resetCsrf(): void {
  csrfToken = null;
}

/** The app layout reacts to a 401 (expired or revoked session) here. */
export function onUnauthorized(listener: () => void): () => void {
  unauthorizedListeners.add(listener);
  return () => unauthorizedListeners.delete(listener);
}

/** Name check, not instanceof: fetch may throw a DOMException from another realm. */
export function isAbortError(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { name?: unknown }).name === "AbortError";
}

export const api = createClient<paths>({
  baseUrl,
  credentials: "same-origin",
  headers: { Accept: "application/json" },
  fetch: (request) => globalThis.fetch(request), // looked up per call, so test interceptors installed later apply
});

function refreshCsrf(): Promise<string> {
  csrfPending ??= unwrap(api.GET("/auth/csrf"))
    .then((res) => (csrfToken = res.data.csrf_token))
    .finally(() => (csrfPending = null));
  return csrfPending;
}

// Endpoints whose 401 is an answer, not an expired session.
const AUTH_PROBES = ["/auth/me", "/auth/login"];

const session: Middleware = {
  async onRequest({ request }) {
    if (MUTATING.has(request.method)) {
      request.headers.set("X-CSRF-Token", csrfToken ?? (await refreshCsrf()));
    }
    return request;
  },
  onResponse({ request, response, schemaPath }) {
    if (response.status === 401) {
      csrfToken = null;
      if (!AUTH_PROBES.includes(schemaPath)) unauthorizedListeners.forEach((l) => l());
    } else if (response.status === 403 && MUTATING.has(request.method)) {
      csrfToken = null; // a CSRF_REJECTED token is useless; refetch on the next mutation
    }
    return response;
  },
};
api.use(session);

type FetchResult<T> = { data?: T; error?: unknown; response: Response };

function toError(body: unknown, res: Response): ApiError {
  const e = (body as { error?: { code?: unknown } } | undefined)?.error;
  if (e && typeof e.code === "string") {
    return new ApiError(e as ConstructorParameters<typeof ApiError>[0], res.status);
  }
  return new ApiError(
    { code: "INTERNAL_ERROR", message: "Respons server tidak dapat dibaca.", request_id: res.headers.get("X-Request-ID") ?? "" },
    res.status,
  );
}

/** Resolves to the response body (the {data} envelope; undefined for 204) or throws ApiError. */
export async function unwrap<T>(call: Promise<FetchResult<T>>): Promise<T> {
  let result: FetchResult<T>;
  try {
    result = await call;
  } catch (err) {
    if (isAbortError(err)) throw err;
    throw new ApiError({ code: "NETWORK_ERROR", message: "Tidak dapat menghubungi server." }, 0);
  }
  if (!result.response.ok) throw toError(result.error, result.response);
  return result.data as T;
}

/** 4xx answers are final: retrying cannot change them. */
export function isClientError(err: unknown): boolean {
  return err instanceof ApiError && err.status >= 400 && err.status < 500;
}
