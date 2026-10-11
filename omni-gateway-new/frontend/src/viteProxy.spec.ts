// @vitest-environment node
import { describe, expect, it, vi } from 'vitest'
import type { UserConfig } from 'vite'

// Node-only configuration is loaded at runtime, outside the browser TypeScript graph.
const { default: config } = await vi.importActual<{ default: UserConfig }>('../vite.config')

describe('控制台接入 Base URL 的运行时代理', () => {
  it('开发模式将 /v1 与会话 API 代理到同一个网关，不返回 SPA HTML', () => {
    const proxy = config.server?.proxy ?? {}
    expect(proxy['/v1']).toBeDefined()
    expect(proxy['/v1']).toEqual(proxy['/api'])
    expect(proxy['/healthz']).toEqual(proxy['/api'])
    expect(proxy['/readyz']).toEqual(proxy['/api'])
  })

  it('独立 preview 同样代理 /v1，同时保留会话和健康检查端点', () => {
    const proxy = config.preview?.proxy ?? {}
    expect(proxy['/v1']).toBeDefined()
    expect(proxy['/v1']).toEqual(proxy['/api'])
    expect(proxy['/healthz']).toEqual(proxy['/api'])
    expect(proxy['/readyz']).toEqual(proxy['/api'])
  })
})
