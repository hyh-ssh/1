// Personal-console presentation only. Never derive permissions or free pricing
// from amounts, last-used timestamps, or an administrator resource.
export type PersonalContext = Record<string, string>
export type AccessModel = {
  id: string
  protocols: string[]
  capabilities: Record<string, boolean> | null
  pricing_status: string
  ready: boolean
  reasons: string[]
}
export type KeyAccess = {
  api_key_id: number
  key_status: string
  first_success_at: string | null
  models: AccessModel[]
}

export const protocols = {
  chat_completions: { label: 'Chat Completions', path: '/chat/completions' },
  responses: { label: 'Responses', path: '/responses' },
  messages: { label: 'Messages', path: '/messages' },
  embeddings: { label: 'Embeddings', path: '/embeddings' },
  images_generations: { label: 'Images Generations', path: '/images/generations' },
} as const
export type Protocol = keyof typeof protocols

export function knownProtocols(model?: AccessModel): Protocol[] {
  return (model?.protocols || []).filter((name): name is Protocol => Object.prototype.hasOwnProperty.call(protocols, name) && model?.capabilities?.[name] !== false)
}

export function normalizeAccess(data: any, id: number): KeyAccess | null {
  if (!data || Number(data.api_key_id) !== id || !Array.isArray(data.models)) return null
  return {
    api_key_id: id,
    key_status: ['active', 'inactive', 'expired'].includes(data.key_status) ? data.key_status : 'unknown',
    first_success_at: typeof data.first_success_at === 'string' && Number.isFinite(Date.parse(data.first_success_at)) ? data.first_success_at : null,
    models: data.models.filter((row: any) => typeof row?.id === 'string').map((row: any) => ({
      id: row.id,
      protocols: Array.isArray(row.protocols) ? row.protocols.filter((value: any) => typeof value === 'string') : [],
      // A string "true" or number 1 is NOT a confirmed capability.
      capabilities: row.capabilities && typeof row.capabilities === 'object' && !Array.isArray(row.capabilities)
        ? Object.fromEntries(Object.entries(row.capabilities).filter(([, value]) => typeof value === 'boolean')) as Record<string, boolean>
        : null,
      pricing_status: typeof row.pricing_status === 'string' ? row.pricing_status : 'unknown',
      ready: row.ready === true,
      reasons: Array.isArray(row.reasons) ? row.reasons.filter((value: any) => typeof value === 'string') : [],
    })),
  }
}

export function capabilityLabel(name: string): string {
  const labels: Record<string, string> = { streaming: '流式输出', tools: '工具调用', vision: '图像输入', json_mode: 'JSON 输出', chat_completions: 'Chat Completions', responses: 'Responses', messages: 'Messages', embeddings: 'Embeddings', images_generations: '图像生成' }
  return Object.prototype.hasOwnProperty.call(labels, name) ? labels[name] : name
}

function ownText(labels: Record<string, string>, key: unknown, fallback: string): string {
  const name = String(key)
  return Object.prototype.hasOwnProperty.call(labels, name) ? labels[name] : fallback
}

export function pricingLabel(status: unknown): string {
  return ownText({ priced: '已定价', free: '免费（显式零费率）', credits_free: '免积分（成本仍独立核算）', unpriced: '缺少价格（不等同免费）', mixed: '价格状态混合' }, status, '价格状态未知')
}

export function settlementLabel(status: unknown): string {
  return ownText({ settled: '已结算', free: '免费（显式零费率）', credits_free: '免积分（成本仍独立核算）', unpriced: '缺价（不等同免费）', failed: '结算失败', none: '未结算' }, status, '结算状态未知')
}

export function utcTime(value: unknown): string {
  if (typeof value !== 'string' || !value || !Number.isFinite(Date.parse(value))) return '—'
  return `${new Date(value).toISOString().replace('T', ' ').replace(/\.\d{3}Z$/, '')} UTC`
}

export function numberText(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  const number = Number(value)
  return Number.isFinite(number) ? number.toLocaleString('zh-CN') : '—'
}

export function keyStatus(key: any): string {
  if (key?.active === false) return '已停用'
  if (key?.expires_at && Number.isFinite(Date.parse(key.expires_at)) && Date.parse(key.expires_at) <= Date.now()) return '已过期'
  return key?.active === true ? '有效' : '状态未知'
}

export function localInput(value: unknown): string {
  if (typeof value !== 'string' || !value || !Number.isFinite(Date.parse(value))) return ''
  const date = new Date(value)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function utcInput(value: string): string {
  if (!value || !Number.isFinite(Date.parse(value))) return ''
  const stamp = new Date(value).toISOString().replace(/Z$/, '').replace(/\.000$/, '')
  return stamp.replace(/:00$/, '')
}

export function inputUTC(value: string): string {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?$/.test(value)) return ''
  const date = new Date(`${value}Z`)
  return Number.isFinite(date.getTime()) ? date.toISOString() : ''
}

const safeID = /^[A-Za-z0-9_.:-]{1,160}$/
const positiveID = /^[1-9]\d{0,14}$/
export function storedRequestID(row: any): string {
  const value = row?.id ?? row?.request_id
  return (typeof value === 'string' || typeof value === 'number') && safeID.test(String(value)) ? String(value) : ''
}

export function storedErrorCode(row: any): string {
  const value = row?.error_code ?? row?.code
  return typeof value === 'string' && /^[A-Za-z0-9_.:-]{1,100}$/.test(value) ? value : ''
}

export function requestContext(input: PersonalContext = {}): PersonalContext {
  const context: PersonalContext = { page: /^[1-9]\d{0,5}$/.test(input.page || '') ? input.page : '1' }
  if (['success', 'error'].includes(input.status)) context.status = input.status
  for (const key of ['from', 'to']) {
    const value = input[key]
    if (typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T.*(?:Z|[+-]\d{2}:\d{2})$/.test(value) && Number.isFinite(Date.parse(value))) context[key] = new Date(value).toISOString()
  }
  if (positiveID.test(input.api_key_id || '')) context.api_key_id = input.api_key_id
  if (safeID.test(input.request_id || '')) context.request_id = input.request_id
  return context
}

export function requestQuery(context: PersonalContext): string {
  const params = new URLSearchParams({ limit: '50', offset: String((Number(context.page) - 1) * 50) })
  for (const key of ['status', 'from', 'to', 'api_key_id', 'request_id']) if (context[key]) params.set(key, context[key])
  return params.toString()
}

export function diagnosis(row: any): { type: string; reason: string; next: string } {
  const code = String(row?.error_code || row?.code || row?.error_type || '')
  if (row?.status === 'success') return { type: '成功', reason: '请求已成功完成', next: '无需重试；结算状态单独查看。' }
  if (/rate_limit|rate_limited/.test(code) || row?.status_code === 429) return { type: '限流', reason: '请求频率或并发超过限制', next: '降低并发或请求频率，等待后再试；查看账号限流规则。' }
  if (/timeout/.test(code) || [408, 504].includes(row?.status_code)) return { type: '超时', reason: '请求未在限定时间内完成', next: '稍后重试，适当缩短输入；持续发生时携请求 ID 联系管理员。' }
  if (/authentication|invalid_api_key|key_expired|key_disabled/.test(code) || row?.status_code === 401) return { type: '鉴权失败', reason: '密钥无效、停用或已过期', next: '核对网关密钥及其有效状态，不要填写上游供应商密钥。' }
  if (/permission|forbidden|model_not_allowed|account_disabled/.test(code) || row?.status_code === 403) return { type: '访问受限', reason: '账号状态或密钥模型白名单不允许此调用', next: '检查账号启停和密钥白名单，必要时联系管理员。' }
  if (/unsupported|invalid_request|model_not_found/.test(code) || [400, 404, 422, 501].includes(row?.status_code)) return { type: '参数或协议不兼容', reason: '模型别名、请求参数或协议不被支持', next: '使用快速接入中可确认的别名和协议，检查请求参数。' }
  if (/upstream|provider|connection/.test(code) || Number(row?.status_code) >= 500) return { type: '服务暂不可用', reason: '上游连接或服务处理失败', next: '稍后重试；持续失败时提供请求 ID，由管理员排查路由。' }
  return { type: '原因未确认', reason: '此记录没有足够的安全诊断信息', next: '保留实际请求 ID 与时间，联系管理员；不展示原始上游错误或正文。' }
}

// Do not echo raw upstream/error message text in a personal view. The API layer
// supplies actual code/request_id when present; absent identifiers stay absent.
export function safeError(result: any): string {
  const code = typeof result?.code === 'string' && /^[A-Za-z0-9_.:-]{1,100}$/.test(result.code) ? result.code : ''
  const id = typeof result?.request_id === 'string' && safeID.test(result.request_id) ? result.request_id : ''
  const status = Number.isInteger(result?.status) && result.status >= 100 && result.status <= 599 ? result.status : null
  const byCode: Record<string, string> = {
    invalid_current_password: '当前密码不正确', weak_password: '新密码至少需要 8 个字符',
    csrf_failed: '会话安全校验失败，请刷新页面后重试', authentication_required: '登录已失效，请重新登录',
    not_found: '资源不存在或不属于当前账号', key_owner_immutable: '密钥归属不可更改',
    account_disabled: '账号已停用，请联系管理员', invalid_request: '提交参数无效，请检查后重试',
  }
  const knownMessages = ['网络错误', '当前密码不正确', '新密码至少需要 8 个字符', '新密码不能与当前密码相同', '过期时间必须晚于当前时间', '请填写密钥名称', '密钥名称最长 64 个字符', '资源不存在', 'CSRF Token 无效', '需要登录后才能继续', '密码已修改，但其它设备上的登录未能失效，请重新登录后再试一次']
  const message = ownText(byCode, code, knownMessages.includes(result?.error) ? result.error : '操作失败，请稍后重试；持续失败请联系管理员')
  return [message, status ? `HTTP ${status}` : '', code ? `code: ${code}` : '', id ? `请求 ID: ${id}` : ''].filter(Boolean).join(' · ')
}

export function passwordMode(user: any, summaryUser: any): 'current' | 'sso' | 'unknown' {
  // The fresh self summary is authoritative when it explicitly states capability.
  for (const value of [summaryUser, user]) {
    const caps = value?.password_capabilities
    if (caps?.local_enabled === true || value?.password_login_enabled === true) return 'current'
    if ((caps?.local_enabled === false || value?.password_login_enabled === false) && caps?.can_set_without_current === true) return 'sso'
    if (caps || typeof value?.password_login_enabled === 'boolean') return 'unknown'
  }
  return 'unknown'
}

function shellQuote(value: string): string { return `'${value.replace(/'/g, `'\\''`)}'` }
export function snippets(baseURL: string, alias: string, protocol: Protocol): { curl: string; python: string; url: string } {
  const common = `export OPENAI_BASE_URL=${shellQuote(baseURL)}\nexport GATEWAY_MODEL=${shellQuote(alias)}\n# 请仅在本机终端设置 GATEWAY_API_KEY；不要将密钥写进示例或 URL。\n`
  const bodies: Record<Protocol, string> = {
    chat_completions: '{"model": os.environ["GATEWAY_MODEL"], "messages": [{"role": "user", "content": "Hello"}]}',
    responses: '{"model": os.environ["GATEWAY_MODEL"], "input": "Hello"}',
    messages: '{"model": os.environ["GATEWAY_MODEL"], "max_tokens": 128, "messages": [{"role": "user", "content": "Hello"}]}',
    embeddings: '{"model": os.environ["GATEWAY_MODEL"], "input": "Hello"}',
    images_generations: '{"model": os.environ["GATEWAY_MODEL"], "prompt": "A landscape"}',
  }
  const header = protocol === 'messages' ? '  -H "x-api-key: ${GATEWAY_API_KEY}" \\\n  -H "anthropic-version: 2023-06-01"' : '  -H "Authorization: Bearer ${GATEWAY_API_KEY}"'
  const curl = `${common}\ncurl "\${OPENAI_BASE_URL}${protocols[protocol].path}" \\\n${header} \\\n  -H 'Content-Type: application/json' \\\n  --data "$(python3 -c '${`import json, os; print(json.dumps(${bodies[protocol]}))`}')"`
  const sdkCalls: Partial<Record<Protocol, string>> = {
    chat_completions: 'client.chat.completions.create(model=model, messages=[{"role": "user", "content": "Hello"}])',
    responses: 'client.responses.create(model=model, input="Hello")',
    embeddings: 'client.embeddings.create(model=model, input="Hello")',
    images_generations: 'client.images.generate(model=model, prompt="A landscape")',
  }
  const python = protocol === 'messages'
    ? `# Messages 使用 Python 标准库；不是 OpenAI SDK 接口。\nimport json, os, urllib.request\nbody = ${bodies.messages}\nrequest = urllib.request.Request(\n    os.environ["OPENAI_BASE_URL"] + "/messages",\n    data=json.dumps(body).encode(),\n    headers={"x-api-key": os.environ["GATEWAY_API_KEY"],\n             "anthropic-version": "2023-06-01", "Content-Type": "application/json"},\n)\nwith urllib.request.urlopen(request) as response:\n    print(json.load(response))`
    : `# 先在终端设置上述环境变量，并安装 openai SDK。\nimport os\nfrom openai import OpenAI\nclient = OpenAI(api_key=os.environ["GATEWAY_API_KEY"],\n                base_url=os.environ["OPENAI_BASE_URL"])\nmodel = os.environ["GATEWAY_MODEL"]\nprint(${sdkCalls[protocol]})`
  return { curl, python, url: baseURL + protocols[protocol].path }
}
