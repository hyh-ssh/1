import { localizeError } from './errors'

export type ApiResult<T> = { data?: T; error?: string; status?: number; code?: string; request_id?: string; stale?: boolean }

// 主 OIDC Provider 的管理端视图：secret 只以 has_client_secret 出现，明文与密文都不
// 会回到浏览器（后端 adminOIDCProviderView 的镜像）。
export type OidcProviderView = {
  enabled: boolean
  issuer: string
  client_id: string
  redirect_url: string
  display_name: string
  has_client_secret: boolean
  ready: boolean
  env_seed_applied: boolean
  updated_at: string | null
  updated_by: number
}

export type OidcProviderInput = {
  enabled: boolean
  issuer: string
  client_id: string
  client_secret?: string
  redirect_url: string
  display_name: string
}

let csrfToken = sessionStorage.getItem('llm_gateway_csrf') || ''
let authGeneration = 0

export function setCSRFToken(token: string) {
  csrfToken = token
  if (token) sessionStorage.setItem('llm_gateway_csrf', token)
  else sessionStorage.removeItem('llm_gateway_csrf')
}

async function request<T>(path: string, init: RequestInit = {}): Promise<ApiResult<T>> {
  try {
    const method = (init.method || 'GET').toUpperCase()
    const headers: HeadersInit = {
      ...(method !== 'GET' && method !== 'HEAD' ? { 'Content-Type': 'application/json' } : {}),
      ...(method !== 'GET' && method !== 'HEAD' && csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      ...(init.headers || {}),
    }
    const res = await fetch(path, { credentials: 'include', ...init, headers })
    const text = await res.text()
    let body: any = text
    try { body = text ? JSON.parse(text) : null } catch { /* keep plain text */ }
    if (!res.ok) {
      const message = body?.error?.message || body?.error || body?.message || `Request failed (${res.status})`
      return { error: localizeError(typeof message === 'string' ? message : `Request failed (${res.status})`), status: res.status, ...(typeof body?.error?.code === 'string' ? { code: body.error.code } : {}), ...(typeof body?.error?.request_id === 'string' ? { request_id: body.error.request_id } : {}) }
    }
    return { data: body as T }
  } catch {
    // fetch() only rejects when no response was produced at all (offline, DNS
    // failure, CORS, abort). There is no server error body to translate, and the
    // browser's own message ("Failed to fetch") is English, so report the generic
    // localized network error instead of leaking it.
    return { error: localizeError('网络错误') }
  }
}

// 管理端 PUT 的请求体。client_secret 留空 = 保留已存密钥，此时字段必须整个缺席：协议里
// 写死的语义是「省略即保留」，显式清空只能走 DELETE，前端不能自己发明第三种状态。
export function oidcProviderPayload(input: OidcProviderInput): Record<string, any> {
  const payload: Record<string, any> = {
    enabled: input.enabled === true,
    issuer: input.issuer,
    client_id: input.client_id,
    redirect_url: input.redirect_url,
    display_name: input.display_name,
  }
  if (typeof input.client_secret === 'string' && input.client_secret !== '') payload.client_secret = input.client_secret
  return payload
}

export const api = {
  me: async (): Promise<ApiResult<any>> => {
    const current = authGeneration
    const result = await request<any>('/api/auth/me')
    if (current !== authGeneration) return { stale: true }
    if (result.data?.csrf_token) setCSRFToken(result.data.csrf_token)
    return result
  },
  login: async (username: string, password: string): Promise<ApiResult<any>> => {
    const current = ++authGeneration
    const result = await request<any>('/api/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })
    if (current !== authGeneration) return { stale: true }
    if (result.data?.csrf_token) setCSRFToken(result.data.csrf_token)
    return result
  },
  logout: async (): Promise<ApiResult<any>> => {
    const current = ++authGeneration
    const result = await request('/api/auth/logout', { method: 'POST' })
    if (current !== authGeneration) return { stale: true }
    if (!result.error) setCSRFToken('')
    return result
  },
  oidc: {
    // 登录页只关心 OAuth 是否启用。该接口不会返回 client secret 或任何 provider
    // 配置，前端也只允许读取 enabled 一个字段。
    config: () => request<{ enabled?: boolean }>('/api/auth/oidc/config'),
    // OAuth 由整页跳转发起：/start 会 302 到身份提供方并写入 state cookie，用
    // fetch 是拿不到这个跳转的，所以只暴露一个固定 URL 给 <a href>。
    startUrl: () => '/api/auth/oidc/start',
  },
  // 主 OIDC Provider 的管理端配置（仅管理员）。GET 在未配置时也返回 200 的空视图；
  // PUT 省略 client_secret 表示保留原值；清空用 DELETE（置空 + enabled=false + 保留记录）。
  oidcProvider: {
    get: () => request<OidcProviderView>('/api/admin/oidc-provider'),
    save: (input: OidcProviderInput) =>
      request<OidcProviderView>('/api/admin/oidc-provider', { method: 'PUT', body: JSON.stringify(oidcProviderPayload(input)) }),
    clear: () => request<null>('/api/admin/oidc-provider', { method: 'DELETE' }),
  },
  resource: <T = any>(name: string, init?: RequestInit) => request<T>(`/api/${name}`, init),
  // The signed-in account's own view. The backend scopes every one of these to
  // the session account and ignores any user id in the body or the query
  // string, so the console must not try to narrow or widen them itself. The
  // writes need the session's CSRF token, which request() adds by itself.
  self: {
    summary: () => request<any>('/api/self/summary'),
    apiKeys: () => request<any>('/api/self/api-keys'),
    requests: (query: number | Record<string, string | number> = 50) => {
      const fields: Record<string, string | number> = typeof query === 'number' ? { limit: query } : { limit: 50, ...query }
      const params = new URLSearchParams(Object.entries(fields).filter(([,value]) => value !== '').map(([key, value]) => [key, String(value)]))
      return request<any>(`/api/self/requests?${params}`)
    },
    access: (id: number) => request<any>(`/api/self/access?api_key_id=${encodeURIComponent(id)}`),
    // expiresAt 是 UTC RFC3339；留空表示永不过期，此时不能把空字符串发出去，否则服务端
    // 会按「非法时间」直接 400。
    createApiKey: (name: string, expiresAt = '', modelAllow?: string[]) => request<any>('/api/self/api-keys', {
      method: 'POST',
      body: JSON.stringify({ name, ...(expiresAt ? { expires_at: expiresAt } : {}), ...(modelAllow ? { model_allow: modelAllow } : {}) }),
    }),
    updateApiKey: (id: number, body: Record<string, any>) => request<any>(`/api/self/api-keys/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }),
    deleteApiKey: (id: number) => request<any>(`/api/self/api-keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    changePassword: (currentPassword: string, newPassword: string) =>
      request<any>('/api/self/password', { method: 'POST', body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }) }),
  },
}
