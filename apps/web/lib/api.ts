import { toast } from "sonner"
import type { Path, UseFormSetError, FieldValues } from "react-hook-form"

/**
 * Typed API client for docs/api-reference.md. One file, typed request/response
 * per route, and every failure surfaces as an `ApiError` holding the backend's
 * { code, message } shape — nothing downstream parses raw fetch responses.
 *
 * The Go API serializes failures as `{ "error": { "code", "message" } }`
 * (apps/api/internal/platform/httperr). A `fields` map is tolerated when a
 * validation error carries it so 400s can map onto react-hook-form state.
 */

export const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080/api/v1"

export interface ApiErrorOptions {
  status: number
  code: string
  message: string
  /** Per-field validation detail, when the backend includes it. */
  fields?: Record<string, string>
  /** Seconds from the 429 Retry-After header, when present. */
  retryAfterSeconds?: number
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly fields?: Record<string, string>
  readonly retryAfterSeconds?: number

  constructor({ status, code, message, fields, retryAfterSeconds }: ApiErrorOptions) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.fields = fields
    this.retryAfterSeconds = retryAfterSeconds
  }

  get isRateLimited(): boolean {
    return this.status === 429
  }
}

interface ApiRequestOptions extends Omit<RequestInit, "body" | "headers"> {
  token?: string
  body?: unknown
  headers?: Record<string, string>
}

function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds > 0) return seconds
  const date = Date.parse(value)
  if (Number.isFinite(date)) return Math.max(0, Math.ceil((date - Date.now()) / 1000))
  return undefined
}

async function toApiError(response: Response): Promise<ApiError> {
  let payload: { error?: { code?: string; message?: string; fields?: Record<string, string> } } = {}
  try {
    payload = (await response.json()) as typeof payload
  } catch {
    // Body wasn't JSON — fall through to the generic shape below.
  }

  const error = payload.error
  const status = response.status
  const retryAfterSeconds = parseRetryAfter(response.headers.get("retry-after"))

  if (error && typeof error.code === "string") {
    return new ApiError({
      status,
      code: error.code,
      message: error.message ?? "Something went wrong.",
      fields: error.fields,
      retryAfterSeconds,
    })
  }

  return new ApiError({
    status,
    code: status === 429 ? "rate_limited" : "unknown_error",
    message:
      status === 429
        ? "Too many attempts."
        : `Request failed with status ${status}.`,
    retryAfterSeconds,
  })
}

/** Core request: success → typed data, failure → throws ApiError. */
export async function apiRequest<T>(
  path: string,
  { token, body, headers, ...init }: ApiRequestOptions = {}
): Promise<T> {
  const requestHeaders: Record<string, string> = {}
  if (body !== undefined) requestHeaders["content-type"] = "application/json"
  if (token) requestHeaders.authorization = `Bearer ${token}`
  Object.assign(requestHeaders, headers)

  let response: Response
  try {
    response = await fetch(`${API_BASE}${path}`, {
      ...init,
      headers: requestHeaders,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
  } catch {
    throw new ApiError({
      status: 0,
      code: "network_error",
      message: "Could not reach the Shopkeet API. Check your connection and try again.",
    })
  }

  if (!response.ok) throw await toApiError(response)
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export const api = {
  get: <T>(path: string, options?: ApiRequestOptions) =>
    apiRequest<T>(path, { ...options, method: "GET" }),
  post: <T>(path: string, body?: unknown, options?: ApiRequestOptions) =>
    apiRequest<T>(path, { ...options, method: "POST", body }),
  patch: <T>(path: string, body?: unknown, options?: ApiRequestOptions) =>
    apiRequest<T>(path, { ...options, method: "PATCH", body }),
  put: <T>(path: string, body?: unknown, options?: ApiRequestOptions) =>
    apiRequest<T>(path, { ...options, method: "PUT", body }),
  delete: <T>(path: string, options?: ApiRequestOptions) =>
    apiRequest<T>(path, { ...options, method: "DELETE" }),
}

/* ---------------------------------------------------------------------------
   Error → UI helpers
--------------------------------------------------------------------------- */

/** Per-field validation detail from a 400, when the backend included it. */
export function apiFieldErrors(error: unknown): Record<string, string> | null {
  if (!(error instanceof ApiError)) return null
  if (error.status !== 400 || !error.fields) return null
  return error.fields
}

/**
 * Maps a 400's field messages onto react-hook-form state so they render inline
 * next to the field (never as a toast). Returns false when the error carried no
 * field detail — the caller decides (toast/ErrorState) then.
 */
export function applyApiFieldErrors<T extends FieldValues>(
  error: unknown,
  setError: UseFormSetError<T>
): boolean {
  const fields = apiFieldErrors(error)
  if (!fields) return false
  for (const [name, message] of Object.entries(fields)) {
    setError(name as Path<T>, { type: "server", message })
  }
  return true
}

/** Human copy for a toast, with the 429 Retry-After surfaced explicitly. */
export function apiErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.isRateLimited && error.retryAfterSeconds != null) {
      const minutes = Math.max(1, Math.round(error.retryAfterSeconds / 60))
      return `Too many attempts — try again in ${minutes} minute${minutes === 1 ? "" : "s"}.`
    }
    if (error.message) return error.message
  }
  if (error instanceof Error && error.message) return error.message
  return "Something went wrong. Please try again."
}

/** Default surface for anything that isn't a field error: a toast. */
export function apiErrorToast(error: unknown): void {
  toast.error(apiErrorMessage(error))
}

/* ---------------------------------------------------------------------------
   Typed wrappers per endpoint (docs/api-reference.md). Screens extend this as
   they land; below are the shared shapes every screen depends on.
--------------------------------------------------------------------------- */

export interface Credentials {
  email: string
  password: string
}

export interface AuthSession {
  token: string
  tenant_id: string
  user_id: string
  role: string
}

export const authApi = {
  login: (credentials: Credentials) => api.post<AuthSession>("/auth/login", credentials),
  signup: (body: { store_name: string; subdomain: string; email: string; password: string }) =>
    api.post<AuthSession>("/auth/signup", body),
}

export interface MediaAsset {
  id: string
  url: string
  content_type: string
  size_bytes: number
  alt_text?: string
}

export const mediaApi = {
  list: (token: string) => api.get<MediaAsset[]>("/media", { token }),
}