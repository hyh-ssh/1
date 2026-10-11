import { describe, expect, it, vi } from 'vitest'
import { capabilityLabel, knownProtocols, normalizeAccess, passwordMode, pricingLabel, requestContext, requestQuery, safeError, settlementLabel, snippets, storedErrorCode, storedRequestID, type Protocol } from './personalConsole'

// The application tsconfig intentionally has no Node declarations. This
// test-only runtime import supplies the small UTF-8 process surface it uses.
const { spawnSync } = await vi.importActual<{
  spawnSync: (command: string, args: string[], options: { encoding: 'utf8'; input?: string }) => { status: number | null; stdout: string; stderr: string }
}>('node:child_process')

describe('个人控制台安全展示 helpers', () => {
  it('请求上下文只接受完整安全白名单，并将有明确时区的时间标准化为 UTC', () => {
    const context = requestContext({ page: '2', status: 'error', from: '2026-10-02T08:00:00+08:00', to: '2026-10-03T00:00:00Z', api_key_id: '5', request_id: 'actual-record-id', password: 'must-drop', body: 'must-drop', user_id: '9', api_key: 'must-drop' })
    expect(context).toEqual({ page: '2', status: 'error', from: '2026-10-02T00:00:00.000Z', to: '2026-10-03T00:00:00.000Z', api_key_id: '5', request_id: 'actual-record-id' })
    expect(new URLSearchParams(requestQuery(context)).get('offset')).toBe('50')
    expect(requestContext({ page: '-1', status: 'other', from: 'not-a-time', to: '2026-10-02T08:00:00', api_key_id: 'bad', request_id: 'not an ID' })).toEqual({ page: '1' })
  })

  it('未知、缺价和免积分只看服务端结算状态，不以金额或实际成功推断免费', () => {
    expect(settlementLabel(undefined)).toBe('结算状态未知')
    expect(settlementLabel('unpriced')).toBe('缺价（不等同免费）')
    expect(settlementLabel('failed')).toBe('结算失败')
    expect(settlementLabel('credits_free')).toContain('成本仍独立核算')
    expect(settlementLabel('constructor')).toBe('结算状态未知')
    expect(pricingLabel('__proto__')).toBe('价格状态未知')
    expect(capabilityLabel('constructor')).toBe('constructor')
    expect(safeError({ code: 'constructor' })).toContain('操作失败')
  })

  it('只使用保存的 ID/真实错误 code，不从 trace ID 或本地时间生成诊断标识', () => {
    expect(storedRequestID({ id: 'persisted-actual', request_id: 'other', trace_id: 'untrusted' })).toBe('persisted-actual')
    expect(storedRequestID({ trace_id: 'untrusted' })).toBe('')
    expect(storedErrorCode({ error_type: 'timeout' })).toBe('')
    expect(storedErrorCode({ error_code: 'real_error' })).toBe('real_error')
    expect(safeError({ error: 'private raw upstream response' })).not.toContain('private')
    expect(safeError({ status: 502, error: 'private', code: 'actual_code', request_id: 'actual-id' })).toContain('code: actual_code · 请求 ID: actual-id')
    expect(safeError({ error: 'unknown' })).not.toContain('请求 ID:')
  })

  it('不信任错误类型的能力值、未知协议、矛盾的明确不支持，且拒绝其他密钥响应', () => {
    const access = normalizeAccess({ api_key_id: 5, key_status: 'active', last_used_at: '2026-10-01T00:00:00Z', models: [{ id: 'alias', protocols: ['constructor', 'chat_completions', 'responses'], capabilities: { tools: 'true', streaming: 1, responses: false, chat_completions: true }, ready: true }] }, 5)!
    expect(access.first_success_at).toBe(null)
    expect(access.models[0].capabilities).toEqual({ responses: false, chat_completions: true })
    expect(knownProtocols(access.models[0])).toEqual(['chat_completions'])
    expect(normalizeAccess({ api_key_id: 6, models: [] }, 5)).toBe(null)
  })

  it('未知密码能力保持禁用；本地已启用时不能利用 can_set_without_current 绕过原密码', () => {
    expect(passwordMode({}, {})).toBe('unknown')
    expect(passwordMode({ password_login_enabled: false }, {})).toBe('unknown')
    expect(passwordMode({ password_capabilities: { local_enabled: false, can_set_without_current: 'true' } }, {})).toBe('unknown')
    expect(passwordMode({ password_capabilities: { local_enabled: true, can_set_without_current: true } }, {})).toBe('current')
    expect(passwordMode({}, { password_capabilities: { local_enabled: false, can_set_without_current: true } })).toBe('sso')
  })

  it.each<Protocol>(['chat_completions', 'responses', 'messages', 'embeddings', 'images_generations'])('%s curl 经过 shell/JSON 安全编码，Python 语法有效；测试替换 curl 为本地打印，不发任何 HTTP', (protocol) => {
    // Even quotes, command substitution and newlines in an alias remain DATA.
    const alias = `alias'\";$(printf injected >&2);#\nnext-line`
    const example = snippets('https://gateway.invalid/v1', alias, protocol)
    const curl = spawnSync('bash', ['-c', `curl() { printf '%s\\n' "$@"; }\nexport GATEWAY_API_KEY=placeholder-only\n${example.curl}`], { encoding: 'utf8' })
    expect(curl.status, curl.stderr).toBe(0)
    expect(curl.stderr).toBe('')
    const args = curl.stdout.trim().split('\n')
    expect(args[0]).toBe(example.url)
    expect(args[0]).not.toContain('/v1/v1')
    const body = JSON.parse(args[args.indexOf('--data') + 1])
    expect(body.model).toBe(alias)
    expect(example.curl).toContain('${GATEWAY_API_KEY}')
    expect(example.python).toContain('os.environ["GATEWAY_API_KEY"]')
    expect(example.python).not.toContain(alias)
    const python = spawnSync('python3', ['-c', 'import ast, sys; ast.parse(sys.stdin.read())'], { input: example.python, encoding: 'utf8' })
    expect(python.status, python.stderr).toBe(0)
  })
})
