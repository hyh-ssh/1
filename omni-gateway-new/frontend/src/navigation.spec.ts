import { describe, expect, it } from 'vitest'
import { cleanContext, readRoute, routeHash } from './navigation'

describe('task URL context', () => {
  it('roundtrips allowed filters, page and detail while dropping secrets', () => {
    const hash = routeHash('requests', {page:'2', model:'model-a', detail:'record-a', api_key_id:'7', password:'never-store', secret:'never-store', content:'private'})
    expect(hash).toBe('#/requests?page=2&api_key_id=7&model=model-a&detail=record-a')
    expect(readRoute(hash, true)).toEqual({view:'requests',context:{page:'2',api_key_id:'7',model:'model-a',detail:'record-a'}})
    expect(hash).not.toContain('never-store')
  })
  it('refuses retired and unauthorized pages and malformed object IDs', () => {
    expect(readRoute('#/userGroups', true).invalid).toBe(true)
    expect(readRoute('#/budgets', true).invalid).toBe(true)
    expect(readRoute('#/settings?secret=x', false)).toEqual({view:'account',context:{},invalid:true})
    expect(cleanContext('myRequests', {page:'0',api_key_id:'-1',from:'bad-date',status:'error',user_id:'20'})).toEqual({status:'error'})
  })
  it('allows admin personal pages and restores times and readonly record links', () => {
    expect(readRoute('#/myKeys?key=8', true)).toEqual({view:'myKeys',context:{key:'8'}})
    expect(readRoute('#/myRequests?from=2026-10-01T00%3A00%3A00Z&status=success&page=3', false).context).toEqual({page:'3',status:'success',from:'2026-10-01T00:00:00Z'})
  })
})
