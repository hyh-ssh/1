// Presentation mapping for the messages the gateway returns. The API answers in
// English so the codes stay stable; this table is the single place that turns
// them into the Chinese copy the console shows. Targets may contain `%s`, which
// is replaced by the trailing detail of the source message (for example the
// upstream's own error body), so an operator still sees the raw reason.
const replacements: Record<string, string> = {
  'authentication required': '需要登录后才能继续',
  'invalid credentials': '用户名或密码错误',
  'csrf token required': '缺少 CSRF Token',
  'invalid csrf token': 'CSRF Token 无效',
  forbidden: '没有权限执行此操作',
  'not found': '资源不存在',
  'method not allowed': '不支持此操作',
  'rate limit exceeded': '请求过于频繁，已触发限流',
  'budget limit exceeded': '已超过预算上限',
  'invalid request': '请求参数无效',
  'alias name already exists': '别名名称已存在',
  alias_conflict: '别名名称已存在',
  // Provider probes: name the cause instead of repeating the old generic
  // "verification failed", which gave the administrator nothing to act on.
  'provider address is blocked by the network policy':
    '上游地址被网络策略拦截（私网/保留地址默认被拒）。如需放行私网上游，请在生产 .env 设置 GATEWAY_ALLOW_PRIVATE_UPSTREAMS=true 并重建后端容器',
  'upstream rejected the provider credentials': '上游拒绝了该凭据（API Key 无效或无权限）%s',
  'upstream rate limited the probe': '上游对本次探测限流%s',
  'upstream did not answer within the provider timeout': '上游在供应商超时时间内未响应（可在供应商配置里调大超时）',
  'could not connect to the upstream': '无法连接上游（网络不通或端口未开放）%s',
  'upstream returned status': '上游返回错误状态%s',
  'provider model discovery failed': '模型发现失败（上游未返回可用的模型列表）',
  'verification failed': '验证失败，未记录具体原因（重新点「验证连接」可看到原因）',
  'retryable upstream failure': '上游请求失败，供应商已进入冷却',
  // Login and role failures. An ordinary account may sign in, but the
  // management surface stays administrator-only, and a disabled account must
  // not be reported as a wrong password.
  'account is disabled': '账号已停用，请联系管理员',
  'administrator privileges required': '该操作仅管理员可用',
  // 限流响应只有 message 会进到 localizeError（api.ts 只取 error.message），
  // 所以这里必须按真实文案 "too many login attempts" 匹配。
  'too many login attempts': '登录尝试过于频繁，请稍后再试',
  // Self-service writes of an ordinary account: creating and managing its own
  // API keys, and rotating its own password.
  'api key name is required': '请填写密钥名称',
  'api key name must be at most 64 characters': '密钥名称最长 64 个字符',
  'expires_at must be in the future': '过期时间必须晚于当前时间',
  'invalid api key request': '密钥请求参数无效',
  'invalid api key id': '密钥 ID 无效',
  'current password and new password are required': '请填写当前密码与新密码',
  'current password is incorrect': '当前密码不正确',
  'new password must be at least 8 characters': '新密码至少需要 8 个字符',
  'new password must differ from the current one': '新密码不能与当前密码相同',
  'password changed but other sessions could not be signed out': '密码已修改，但其它设备上的登录未能失效，请重新登录后再试一次',
  // 存储层失败也会发生在普通用户身上（自助写走的是同一套错误助手），所以这些
  // 文案不能把「administrator」字样原样丢给一个普通账号。
  'administrator store operation failed': '服务端存储操作失败，请稍后重试；持续失败请联系管理员',
  'administrator store operation is not configured': '服务端未配置存储，无法完成该操作',
  'could not generate api key': '生成密钥失败，请重试',
  'could not hash password': '密码处理失败，请重试',
  'limit must be between 1 and 200': '分页参数无效（limit 需在 1–200 之间）',
  'offset must not be negative': '分页参数无效（offset 不能为负）',
  // Responses API 明确拒答：操作者最容易把它读成「流式坏了」，所以中文文案要直接
  // 给两步出路（先勾开关，再不行就换协议面），而不是把英文原文丢回去。
  'does not implement the openai responses api':
    '该供应商未声明支持 Responses API，网关已在上游调用前直接拒答（501）。若上游确实实现了 /v1/responses，请在管理台的供应商表单勾选「支持 Responses API」；否则请改用 /v1/chat/completions',
  'request failed': '请求失败',
  // 主 OIDC Provider 的管理端校验（字段名与表单字段一一对应）。清空配置走 DELETE，
  // 所以「留空 client_secret」不是错误，这里也没有对应文案。
  'issuer must be an absolute url': 'Issuer 必须是绝对 URL（例如 https://idp.example.com）',
  'redirect_url must be an absolute url': '回调地址必须是绝对 URL（例如 https://网关域名/api/auth/oidc/callback）',
  'enabled oidc provider requires': '启用主 OIDC Provider 前必须填写以下字段%s',
  'invalid oidc provider': '主 OIDC Provider 配置无效',
  'oidc client secret encryption is unavailable': '服务端无法加密客户端密钥，请检查网关主密钥配置后重试',
}

// 主 OIDC Provider 的缺失字段列表直接来自后端原文（issuer, client_id, …）。字段名与表单
// 一致，但没有中文说明时管理员不好判断该改哪一项，所以逐个括注中文标签。
const providerFieldLabels: Record<string, string> = {
  issuer: 'issuer（签发方）',
  client_id: 'client_id（客户端 ID）',
  client_secret: 'client_secret（客户端密钥）',
  redirect_url: 'redirect_url（回调地址）',
}

// 只对需要二次翻译的明细生效：上游原始报错等其它 %s 明细必须原样透出。
const detailTransforms: Record<string, (detail: string) => string> = {
  'enabled oidc provider requires': (detail) =>
    Object.entries(providerFieldLabels).reduce((text, [field, label]) => text.split(field).join(label), detail),
}

export function localizeError(message: unknown): string {
  const value = String(message || '')
  if (!value) return '请求失败'
  const lower = value.toLowerCase()
  // Match the leading clause first: the reason sits before the colon, while the
  // echoed upstream body after it may itself contain words like "not found" that
  // must not win over the reason the gateway is reporting.
  const head = lower.split(/[:：]/)[0] ?? lower
  const entries = Object.entries(replacements)
  const hit = entries.find(([source]) => head.includes(source)) ?? entries.find(([source]) => lower.includes(source))
  if (!hit) return value
  const [source, target] = hit
  const at = lower.indexOf(source)
  const detail = at < 0 ? '' : value.slice(at + source.length).replace(/^[\s:：]+/, '')
  const transform = detailTransforms[source]
  const translated = detail && transform ? transform(detail) : detail
  return target.includes('%s') ? target.replace('%s', translated ? `：${translated}` : '') : target
}

// OIDC 回调失败时，服务端把错误码放在登录页的 ?oauth_error=<code> 查询参数里。
// 这个值来自 URL、可能被篡改，所以只当查表键用：命中给出固定中文，未命中一律落到
// 通用文案，绝不把原始值原样渲染回页面。code/state/token 等协议参数根本不读取。
const oauthErrors: Record<string, string> = {
  access_denied: '你已取消 OAuth 授权，未完成登录',
  invalid_state: 'OAuth 登录状态校验失败，请重新登录',
  state_mismatch: 'OAuth 登录状态校验失败，请重新登录',
  state_expired: 'OAuth 登录已超时，请重新登录',
  missing_code: 'OAuth 回调缺少授权码，请重新登录',
  token_exchange_failed: 'OAuth 换取令牌失败，请稍后重试',
  userinfo_failed: 'OAuth 无法获取账号信息，请稍后重试',
  email_not_verified: 'OAuth 账号邮箱未验证，无法登录',
  account_disabled: '账号已停用，请联系管理员',
  user_not_allowed: '该 OAuth 账号未被允许登录',
  provider_unavailable: '身份提供方暂时不可用，请稍后重试或改用用户名密码登录',
  oidc_not_configured: '服务端未启用 OAuth 登录',
}

// 空输入返回空串，让调用方据此判断「没有错误」而不是显示一条空提示。
export function localizeOAuthError(code: unknown): string {
  const key = String(code ?? '').trim().toLowerCase()
  if (!key) return ''
  return oauthErrors[key] || 'OAuth 登录失败，请重试或改用用户名密码登录'
}
