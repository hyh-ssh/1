import { describe, expect, it } from 'vitest'
import { localizeError, localizeOAuthError } from './errors'

describe('localizeError upstream reasons', () => {
  it('maps a policy block to copy that names the switch to flip', () => {
    const message = localizeError('provider address is blocked by the network policy')

    expect(message).toContain('上游地址被网络策略拦截')
    expect(message).toContain('GATEWAY_ALLOW_PRIVATE_UPSTREAMS=true')
  })

  it('keeps the upstream detail after the localized reason', () => {
    const message = localizeError('upstream rejected the provider credentials: {"error":"invalid api key"}')

    expect(message).toContain('上游拒绝了该凭据')
    expect(message).toContain('invalid api key')
  })

  it('adds no separator when the reason carries no detail', () => {
    expect(localizeError('upstream rejected the provider credentials')).toBe('上游拒绝了该凭据（API Key 无效或无权限）')
  })

  it('prefers the gateway reason over words inside the echoed upstream body', () => {
    const message = localizeError('upstream returned status 404: {"error":"model not found"}')

    expect(message).toContain('上游返回错误状态')
    expect(message).toContain('model not found')
  })

  it('explains a reason recorded before the failure was classified', () => {
    expect(localizeError('verification failed')).toContain('重新点「验证连接」')
    expect(localizeError('retryable upstream failure')).toContain('已进入冷却')
  })

  it('maps the Responses API refusal to the switch that fixes it', () => {
    const message = localizeError('the selected provider does not implement the OpenAI Responses API; use /v1/chat/completions instead')

    expect(message).toContain('未声明支持 Responses API')
    expect(message).toContain('支持 Responses API')
    expect(message).toContain('/v1/chat/completions')
    expect(message).not.toContain('does not implement')
  })
})

// These three are the copy a newly created ordinary account sees. The strings
// are the contract of backend/internal/httpapi (auth.go, admin.go,
// login_limit.go); auth_test.go asserts the same wording on the server side.
describe('localizeError login and role copy', () => {
  it('tells a disabled account apart from a wrong password', () => {
    expect(localizeError('account is disabled')).toBe('账号已停用，请联系管理员')
    expect(localizeError('invalid credentials')).toBe('用户名或密码错误')
  })

  it('explains that the management surface is administrator-only', () => {
    expect(localizeError('administrator privileges required')).toBe('该操作仅管理员可用')
  })

  it('maps the login throttle using the message the server actually sends', () => {
    expect(localizeError('too many login attempts')).toBe('登录尝试过于频繁，请稍后再试')
  })
})

describe('localizeError fallbacks', () => {
  it('passes an unmapped message through and falls back for empty input', () => {
    expect(localizeError('quantum flux overflow')).toBe('quantum flux overflow')
    expect(localizeError('')).toBe('请求失败')
  })
})

// The copy a signed-in ordinary account sees when one of its own write requests
// is rejected. These strings are the contract of backend/internal/httpapi/self.go
// (self_test.go asserts the same wording on the server side).
describe('localizeError self-service copy', () => {
  it('explains an incorrect current password', () => {
    expect(localizeError('current password is incorrect')).toBe('当前密码不正确')
  })

  it('explains the rules for the new password', () => {
    expect(localizeError('current password and new password are required')).toBe('请填写当前密码与新密码')
    expect(localizeError('new password must be at least 8 characters')).toBe('新密码至少需要 8 个字符')
    expect(localizeError('new password must differ from the current one')).toBe('新密码不能与当前密码相同')
  })

  it('explains the rules for an API key name', () => {
    expect(localizeError('api key name is required')).toBe('请填写密钥名称')
    expect(localizeError('api key name must be at most 64 characters')).toBe('密钥名称最长 64 个字符')
    // The server capitalises API in its own wording; the table matches case-insensitively.
    expect(localizeError('API key name must be at most 64 characters')).toBe('密钥名称最长 64 个字符')
    expect(localizeError('invalid api key request')).toBe('密钥请求参数无效')
    expect(localizeError('expires_at must be in the future')).toBe('过期时间必须晚于当前时间')
  })

  it('reports a half-completed password change instead of a success', () => {
    expect(localizeError('password changed but other sessions could not be signed out'))
      .toBe('密码已修改，但其它设备上的登录未能失效，请重新登录后再试一次')
  })
})

// OIDC 回调失败的错误码来自查询串，只允许映射成固定中文；未知码必须落到通用提示，
// 绝不能把原始值（可能是攻击者构造的字符串）渲染回页面。
describe('localizeOAuthError', () => {
  it('maps the callback failure codes to stable Chinese copy', () => {
    expect(localizeOAuthError('access_denied')).toBe('你已取消 OAuth 授权，未完成登录')
    expect(localizeOAuthError('invalid_state')).toContain('状态校验失败')
    expect(localizeOAuthError('token_exchange_failed')).toContain('换取令牌失败')
  })

  it('falls back to a generic sentence for an unknown code and never echoes it', () => {
    const message = localizeOAuthError('<img src=x onerror=alert(1)>')
    expect(message).toBe('OAuth 登录失败，请重试或改用用户名密码登录')
    expect(message).not.toContain('onerror')
  })

  it('returns an empty string when there is no error code', () => {
    expect(localizeOAuthError('')).toBe('')
    expect(localizeOAuthError(null)).toBe('')
    expect(localizeOAuthError(undefined)).toBe('')
  })
})
