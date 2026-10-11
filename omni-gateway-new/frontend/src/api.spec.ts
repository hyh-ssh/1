import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// ---- helpers ---------------------------------------------------------------

type FetchStub = ReturnType<typeof vi.fn>

function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  }
}

let fetchMock: FetchStub

function installFetch(impl: (url: string, init?: RequestInit) => unknown) {
  fetchMock = vi.fn(impl as any)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function lastCall(): { url: string; init: RequestInit } {
  const calls = fetchMock.mock.calls
  const call = calls[calls.length - 1]!
  return { url: String(call[0]), init: (call[1] || {}) as RequestInit }
}

beforeEach(() => {
  vi.resetModules()
  sessionStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// ---- request: headers ------------------------------------------------------

describe('request headers', () => {
  it('adds Content-Type: application/json for non-GET/HEAD and omits X-CSRF-Token when there is no token', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ ok: true }))

    await api.api.resource('widgets', { method: 'POST', body: JSON.stringify({ a: 1 }) })

    const { init } = lastCall()
    const headers = init.headers as Record<string, string>
    expect(headers['Content-Type']).toBe('application/json')
    expect(headers['X-CSRF-Token']).toBeUndefined()
  })

  it('adds X-CSRF-Token only once a token has been set', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ ok: true }))
    api.setCSRFToken('tok-123')

    await api.api.resource('widgets', { method: 'POST', body: '{}' })

    const headers = lastCall().init.headers as Record<string, string>
    expect(headers['Content-Type']).toBe('application/json')
    expect(headers['X-CSRF-Token']).toBe('tok-123')
  })

  it('sends neither Content-Type nor X-CSRF-Token on GET', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ ok: true }))
    api.setCSRFToken('tok-123')

    await api.api.resource('widgets')

    const { init } = lastCall()
    expect(init.method).toBeUndefined()
    const headers = init.headers as Record<string, string>
    expect(headers['Content-Type']).toBeUndefined()
    expect(headers['X-CSRF-Token']).toBeUndefined()
  })
})

// ---- request: error handling ----------------------------------------------

describe('request error handling', () => {
  it('returns { error, status } with a localized message for a non-2xx response', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ error: { message: 'invalid credentials' } }, 401))

    const result = await api.api.resource('widgets', { method: 'POST', body: '{}' })

    expect(result).toEqual({ error: '用户名或密码错误', status: 401 })
  })

  it('returns an unmapped english message unchanged', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ error: { message: 'quantum flux overflow' } }, 418))

    const result = await api.api.resource('widgets')

    expect(result.error).toBe('quantum flux overflow')
    expect(result.status).toBe(418)
  })

  it('falls back to 请求失败 when the error body carries no message', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse('', 500))

    const result = await api.api.resource('widgets')

    expect(result).toEqual({ error: '请求失败', status: 500 })
  })

  it('returns a 网络错误 error instead of throwing when fetch rejects', async () => {
    const api = await import('./api')
    installFetch(() => Promise.reject('offline'))

    const result = await api.api.resource('widgets')

    expect(result.error).toMatch(/^网络错误/)
    expect(result.data).toBeUndefined()
  })

  it('maps a real browser fetch TypeError to the localized network error', async () => {
    const api = await import('./api')
    installFetch(() => Promise.reject(new TypeError('Failed to fetch')))

    const result = await api.api.resource('widgets')

    expect(result).toEqual({ error: '网络错误' })
  })
})

// ---- request: success ------------------------------------------------------

describe('request success', () => {
  it('returns parsed JSON as data', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ requests: 7, nested: { ok: true } }))

    const result = await api.api.resource<{ requests: number }>('stats')

    expect(result.error).toBeUndefined()
    expect(result.data).toEqual({ requests: 7, nested: { ok: true } })
  })

  it('survives a non-JSON plain text body and returns it as data', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse('plain text ok'))
    const result = await api.api.resource<string>('health')
    expect(result.data).toBe('plain text ok')
  })
})

// ---- token flow ------------------------------------------------------------

describe('CSRF token flow', () => {
  it('setCSRFToken persists to and clears sessionStorage key llm_gateway_csrf', async () => {
    const api = await import('./api')

    api.setCSRFToken('persisted')
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBe('persisted')

    api.setCSRFToken('')
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBeNull()
  })

  it('api.login stores csrf_token from the response into sessionStorage', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ csrf_token: 'csrf-from-login', user: { id: 1 } }))

    const result = await api.api.login('alice', 'secret')

    expect(result.data?.csrf_token).toBe('csrf-from-login')
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBe('csrf-from-login')
  })

  it('api.me stores csrf_token from the response into sessionStorage', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ csrf_token: 'csrf-from-me', username: 'alice' }))

    await api.api.me()

    expect(sessionStorage.getItem('llm_gateway_csrf')).toBe('csrf-from-me')
  })

  it('api.logout clears the stored csrf token on success', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ ok: true }))
    api.setCSRFToken('stale-token')
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBe('stale-token')

    const result = await api.api.logout()

    expect(result.error).toBeUndefined()
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBeNull()
  })

  it('api.logout keeps the token when the request fails', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ error: { message: 'authentication required' } }, 401))
    api.setCSRFToken('keep-me')

    const result = await api.api.logout()

    expect(result.error).toBe('需要登录后才能继续')
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBe('keep-me')
  })

// ---- self: 自助接口的请求体契约 --------------------------------------------

describe('api.self.createApiKey', () => {
  it('留空过期时间时只发 name，不发空字符串', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ key: { id: 1 }, secret: 'sk-llm-x' }, 201))

    await api.api.self.createApiKey('ci-key')

    const { url, init } = lastCall()
    expect(url).toBe('/api/self/api-keys')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ name: 'ci-key' })
  })

  it('填了过期时间时一起发送 expires_at（UTC RFC3339）', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ key: { id: 1 }, secret: 'sk-llm-x' }, 201))

    await api.api.self.createApiKey('ci-key', '2030-01-02T03:04:05.000Z')

    expect(JSON.parse(String(lastCall().init.body))).toEqual({ name: 'ci-key', expires_at: '2030-01-02T03:04:05.000Z' })
  })
})
})

describe('api.oidc', () => {
  it('GET /api/auth/oidc/config 只解析 enabled，不外泄其它配置字段', async () => {
    const api = await import('./api')
    installFetch(() => fakeResponse({ enabled: true, client_secret: 'leaked' }))

    const result = await api.api.oidc.config()

    expect(lastCall().url).toBe('/api/auth/oidc/config')
    expect(result.data?.enabled).toBe(true)
  })

  it('startUrl 返回同源相对路径，供整页跳转使用', async () => {
    const api = await import('./api')
    expect(api.api.oidc.startUrl()).toBe('/api/auth/oidc/start')
  })
})

describe('product diagnostic and personal contract', () => {
  it('retains only actual structured error identifiers', async () => {
    const { api } = await import('./api')
    installFetch(() => fakeResponse({error:{message:'not found',code:'not_found',request_id:'real-server-id'}},404))
    expect(await api.resource('self/access?api_key_id=5')).toMatchObject({status:404,code:'not_found',request_id:'real-server-id'})
    installFetch(() => Promise.reject(new Error('offline')))
    const failure = await api.self.summary()
    expect(failure.code).toBeUndefined()
    expect(failure.request_id).toBeUndefined()
  })
  it('serializes own request filters and uses session-bound creation without owner input', async () => {
    const { api } = await import('./api')
    installFetch(() => fakeResponse([]))
    await api.self.requests({offset:50,status:'error',from:'2026-10-01T00:00:00Z'})
    expect(lastCall().url).toBe('/api/self/requests?limit=50&offset=50&status=error&from=2026-10-01T00%3A00%3A00Z')
    await api.self.createApiKey('local-client','',['model-a'])
    expect(JSON.parse(lastCall().init.body as string)).toEqual({name:'local-client',model_allow:['model-a']})
    await api.self.access(10)
    expect(lastCall().url).toBe('/api/self/access?api_key_id=10')
  })
})

describe('跨会话延迟响应隔离', () => {
  it('退出并登录新账号后，旧 me 响应不能覆盖身份或新 CSRF', async () => {
    const { api } = await import('./api')
    let finish!: (response: unknown) => void
    installFetch((url) => {
      if (url === '/api/auth/me') return new Promise(resolve => { finish = resolve })
      if (url === '/api/auth/login') return Promise.resolve(fakeResponse({user:{id:2,username:'new-user'},csrf_token:'new-csrf'}))
      return Promise.resolve(fakeResponse(null,204))
    })
    const pending = api.me()
    await api.logout()
    await api.login('new-user','synthetic-password')
    finish(fakeResponse({user:{id:1,username:'old-admin',role:'admin'},csrf_token:'old-csrf'}))
    expect(await pending).toEqual({stale:true})
    expect(sessionStorage.getItem('llm_gateway_csrf')).toBe('new-csrf')
    await api.resource('self/api-keys/9',{method:'PATCH',body:JSON.stringify({active:false})})
    expect((lastCall().init.headers as Record<string,string>)['X-CSRF-Token']).toBe('new-csrf')
  })
})
