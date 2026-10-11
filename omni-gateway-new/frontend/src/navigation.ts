export const adminViews = ['dashboard', 'users', 'keys', 'providers', 'routing', 'prices', 'requests', 'audit', 'limits', 'settings', 'modelGroups'] as const
export const personalViews = ['account', 'myKeys', 'myRequests'] as const
export type View = typeof adminViews[number] | typeof personalViews[number]
export type PageContext = Record<string, string>
export type Route = { view: View; context: PageContext; invalid?: boolean }

const filterKeys: Record<string, string[]> = {
  requests: ['page', 'from', 'to', 'request_id', 'user_id', 'api_key_id', 'model', 'api', 'provider_id', 'status', 'settlement_status', 'model_group_id', 'detail'],
  myRequests: ['page', 'status', 'from', 'to', 'request_id', 'api_key_id'],
  audit: ['page', 'from', 'to', 'actor_id', 'resource', 'action', 'result'],
  dashboard: ['period', 'api', 'user_id', 'provider_id', 'model_group_id'],
  routing: ['detail'],
  providers: ['detail'],
  prices: ['model'],
  myKeys: ['key'],
}

// URL is task context, never a persistence layer for editable or sensitive fields.
export function cleanContext(view: View, input: PageContext): PageContext {
  const context: PageContext = {}
  for (const key of filterKeys[view] || []) {
    const value = input[key]
    if (typeof value !== 'string' || !value.trim() || value.length > 200) continue
    if (['page', 'user_id', 'api_key_id', 'provider_id', 'actor_id', 'model_group_id', 'key'].includes(key) && !/^[1-9]\d{0,9}$/.test(value)) continue
    if (key === 'period' && !['24', '168', '720'].includes(value)) continue
    if (['from', 'to'].includes(key) && Number.isNaN(Date.parse(value))) continue
    context[key] = value
  }
  return context
}

export function readRoute(hash: string, admin: boolean): Route {
  const fallback: View = admin ? 'dashboard' : 'account'
  if (!hash || hash === '#' || hash === '#/') return { view: fallback, context: {} }
  const [rawView, rawQuery = ''] = hash.replace(/^#\/?/, '').split('?')
  const known = [...adminViews, ...personalViews].includes(rawView as View)
  if (!known || (!admin && !personalViews.includes(rawView as typeof personalViews[number]))) return { view: fallback, context: {}, invalid: true }
  const view = rawView as View
  const query = Object.fromEntries(new URLSearchParams(rawQuery))
  return { view, context: cleanContext(view, query) }
}

export function routeHash(view: View, input: PageContext = {}): string {
  const query = new URLSearchParams(cleanContext(view, input)).toString()
  return `#/${view}${query ? `?${query}` : ''}`
}
