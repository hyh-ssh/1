<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { api, type ApiResult } from './api'
import { localizeOAuthError } from './errors'
import { cleanContext, personalViews, readRoute, routeHash, type PageContext, type View } from './navigation'
import OperationsPane from './OperationsPane.vue'
import DashboardPane from './DashboardPane.vue'
import OidcProviderSettings from './OidcProviderSettings.vue'
import PersonalPane from './PersonalPane.vue'
import PriceEditor from './PriceEditor.vue'
import { priceClassification } from './priceMath'

const me = ref<any>(null), loading = ref(true), authenticated = ref(false)
const username = ref(''), password = ref(''), loginBusy = ref(false), logoutBusy = ref(false)
const error = ref(''), notice = ref(''), routeWarning = ref(''), oidcEnabled = ref(false), oauthError = ref('')
const view = ref<View>('dashboard'), context = ref<PageContext>({}), mobileNav = ref(false), accountMenu = ref(false)
const rows = ref<any[]>([]), resourceLoading = ref(false), busy = ref(false), showForm = ref(false), editing = ref<number | null>(null), form = ref<Record<string, any>>({})
const optionUsers = ref<any[]>([]), optionKeys = ref<any[]>([]), optionAliases = ref<any[]>([]), modelOptions = ref<{value:string,label:string}[]>([])
const isAdmin = computed(() => me.value?.role === 'admin')
const isPersonal = computed(() => personalViews.includes(view.value as any))
const isOperations = computed(() => ['providers','routing','modelGroups','requests','audit'].includes(view.value))
const labels: Record<string,string> = {dashboard:'仪表盘',users:'用户',keys:'密钥治理',providers:'供应商与模型',routing:'别名与路由',prices:'价格',requests:'请求记录',audit:'审计日志',limits:'限流',settings:'设置',modelGroups:'模型组',account:'账号中心',myKeys:'我的 API 密钥',myRequests:'我的调用记录'}
const title = computed(() => labels[view.value])
const navGroups = computed(() => isAdmin.value ? [
  {label:'概览',items:['dashboard']}, {label:'模型服务',items:['providers','routing','modelGroups','prices']},
  {label:'用量与诊断',items:['requests','audit','limits']}, {label:'账号与密钥',items:['users','keys','account','myKeys','myRequests']}, {label:'系统设置',items:['settings']},
] : [{label:'我的账号',items:['account','myKeys','myRequests']}])
const listOf = (data: any): any[] => Array.isArray(data) ? data : data?.items || []
const endpoints: Record<string,string> = {users:'admin/users',keys:'admin/api-keys',limits:'admin/rate-limits',prices:'admin/prices'}
function failure(result: ApiResult<any>): string {
  return `${result.error || '请求失败'}${result.code ? ` · ${result.code}` : ''}${result.request_id ? ` · 请求 ID：${result.request_id}` : ''}`
}
let generation = 0
function clearForms() { showForm.value = false; editing.value = null; form.value = {}; password.value = ''; notice.value = ''; accountMenu.value = false; mobileNav.value = false }
function allowedRoute(nextView: string, nextContext: PageContext = {}) {
  return readRoute(routeHash(nextView as View, nextContext), isAdmin.value)
}
async function navigate(next: View | {view:string;context?:PageContext}, values: PageContext = {}) {
  const desired = typeof next === 'string' ? {view:next,context:values} : next
  const target = allowedRoute(desired.view, desired.context || {})
  if (target.invalid) { routeWarning.value = '此链接无效或当前账号无权访问；已返回允许的页面。'; return }
  routeWarning.value = ''
  accountMenu.value = false; mobileNav.value = false
  const hash = routeHash(target.view, target.context)
  if (window.location.hash !== hash) {
    history.replaceState({scrollY:window.scrollY}, '', window.location.href)
    history.pushState({scrollY: target.view === view.value ? window.scrollY : 0}, '', hash)
  }
  await applyRoute(target.view, target.context)
}
async function applyRoute(nextView: View, values: PageContext) {
  const changed = view.value !== nextView
  view.value = nextView; context.value = values
  if (changed) {
    generation++; resourceLoading.value = false; rows.value = []; error.value = ''; clearForms(); await loadResource()
    await nextTick(); window.scrollTo?.(0, 0)
  }
}
function changeContext(values: PageContext) { void navigate(view.value, cleanContext(view.value, values)) }
async function restoreRoute() {
  if (!authenticated.value) return
  const route = readRoute(window.location.hash, isAdmin.value)
  routeWarning.value = route.invalid ? '此链接无效或当前账号无权访问；已返回允许的页面。' : ''
  const scrollY = Number(history.state?.scrollY || 0)
  await applyRoute(route.view, route.context)
  await nextTick(); window.scrollTo?.(0, scrollY)
}
async function establish(user: any) {
  me.value = user; authenticated.value = true
  clearForms(); rows.value = []; error.value = ''; generation++
  const route = readRoute(window.location.hash, isAdmin.value)
  view.value = route.view; context.value = route.context
  routeWarning.value = route.invalid ? '此链接无效或当前账号无权访问；已返回允许的页面。' : ''
  history.replaceState({scrollY:0}, '', routeHash(route.view,route.context))
  await loadResource()
}
async function checkAuth() {
  const result = await api.me()
  if (result.data?.user) await establish(result.data.user)
  else {
    if (result.status !== 401 && result.error) error.value = failure(result)
    oauthError.value = localizeOAuthError(new URLSearchParams(location.search).get('oauth_error'))
    const config = await api.oidc.config(); oidcEnabled.value = config.data?.enabled === true
  }
  loading.value = false
}
async function login() {
  if (loginBusy.value) return
  loginBusy.value = true; error.value = ''
  try {
    const result = await api.login(username.value, password.value)
    password.value = ''
    if (result.error) error.value = failure(result)
    else if (result.data?.user) await establish(result.data.user)
  } finally { loginBusy.value = false }
}
async function logout() {
  if (logoutBusy.value) return
  logoutBusy.value = true
  const result = await api.logout()
  if (result.error) { error.value = failure(result); logoutBusy.value = false; return }
  generation++; me.value = null; authenticated.value = false; rows.value = []; context.value = {}; error.value = ''; routeWarning.value = ''; clearForms(); optionUsers.value = []; optionKeys.value = []; optionAliases.value = []; modelOptions.value = []
  history.replaceState({}, '', '#/account')
  oidcEnabled.value = (await api.oidc.config()).data?.enabled === true
  logoutBusy.value = false
}
async function refreshUser() {
  const current = generation, ownerID = me.value?.id
  const result = await api.me()
  if (result.stale || !authenticated.value || current !== generation || ownerID !== me.value?.id) return
  if (result.data?.user) me.value = result.data.user
  else error.value = failure(result)
}
async function loadOptions() {
  const current = generation
  const [users,keys,aliases] = await Promise.all([api.resource('admin/users'),api.resource('admin/api-keys'),api.resource('admin/aliases')])
  if (current !== generation) return
  const failed = [users,keys,aliases].find(r => r.error)
  if (failed) throw new Error(failure(failed))
  optionUsers.value = listOf(users.data); optionKeys.value = listOf(keys.data); optionAliases.value = listOf(aliases.data)
}
async function loadPricesOptions() {
  const current = generation
  const [aliases,providers] = await Promise.all([api.resource('admin/aliases'),api.resource('admin/providers')])
  if (current !== generation) return
  if (aliases.error || providers.error) throw new Error(failure(aliases.error ? aliases : providers))
  const options = listOf(aliases.data).map((a:any) => ({value:a.name,label:`对外别名：${a.name}（上游价优先，别名价回退）`}))
  for (const provider of listOf(providers.data)) {
    const models = await api.resource(`admin/providers/${provider.id}/models`)
    if (current !== generation) return
    if (models.error) throw new Error(failure(models))
    for (const model of listOf(models.data)) options.push({value:model.model_id,label:`上游模型：${model.model_id} · ${provider.name}`})
  }
  modelOptions.value = options.filter((item,i) => options.findIndex(x => x.value === item.value) === i)
}
async function loadResource() {
  if (!authenticated.value || !isAdmin.value || !endpoints[view.value]) return
  const current = ++generation, target = view.value
  resourceLoading.value = true; error.value = ''
  try {
    const result = await api.resource(endpoints[target])
    if (current !== generation) return
    if (result.error) throw new Error(failure(result))
    rows.value = listOf(result.data)
    if (target === 'limits' || target === 'keys') await loadOptions()
    if (target === 'prices') await loadPricesOptions()
  } catch (e) { if (current === generation) error.value = e instanceof Error ? e.message : '读取失败' }
  finally { if (current === generation) resourceLoading.value = false }
}
function editResource(row?: any) {
  notice.value = ''; error.value = ''; editing.value = row ? Number(row.id) : null; showForm.value = true
  form.value = row ? {...row,password:''} : view.value === 'users' ? {username:'',display_name:'',role:'user',active:true,password:''} : {scope:'user',scope_id:me.value?.id || '',model_id:'',rpm:60,tpm:0,concurrency:0,active:true}
  if (view.value === 'limits') scopeChanged()
}
const limitTargets = computed(() => form.value.scope === 'key'
  ? optionKeys.value.map(key => ({id:key.id,name:`${key.name} · ${ownerName(key.user_id)}`}))
  : optionUsers.value.map(user => ({id:user.id,name:user.username})))
function scopeChanged() {
  if (form.value.scope === 'model') { form.value.scope_id = 0; return }
  if (!limitTargets.value.some(item => Number(item.id) === Number(form.value.scope_id))) form.value.scope_id = limitTargets.value[0]?.id || ''
}
function ownerName(id: number) { return optionUsers.value.find(user => user.id === id)?.username || `#${id}` }
function keyState(key: any) { return key.active === false ? '停用' : key.expires_at && Date.parse(key.expires_at) <= Date.now() ? '已过期' : '启用' }
function resourceDetails(row: any) {
  if (view.value === 'users') return `#${row.id} · ${row.role === 'admin' ? '管理员' : '用户'} · ${row.password_login_enabled === false ? '仅 SSO' : '本地密码可用'}`
  if (view.value === 'keys') return `${ownerName(row.user_id)} · ${row.key_prefix || ''}…${row.key_suffix || ''} · ${row.model_allow?.length ? row.model_allow.join(', ') : '全部启用模型'} · 过期 ${row.expires_at ? time(row.expires_at) : '永不过期'} · 最后使用 ${row.last_used_at ? time(row.last_used_at) : '从未使用'}`
  if (row.scope === 'model') return `模型 ${row.model_id || '全部'} · RPM ${row.rpm} · TPM ${row.tpm} · 并发 ${row.concurrency}`
  const object = row.scope === 'key' ? optionKeys.value.find(k => k.id === row.scope_id)?.name || `#${row.scope_id}` : ownerName(row.scope_id)
  return `${row.scope === 'key' ? '密钥' : '用户'} ${object} · ${row.model_id || '全部模型'} · RPM ${row.rpm} · TPM ${row.tpm} · 并发 ${row.concurrency}`
}
async function mutate(path:string, method:string, body?: any) {
  const result = await api.resource(path,{method,...(body ? {body:JSON.stringify(body)} : {})})
  if (result.error) throw new Error(failure(result))
  return result.data
}
async function saveResource() {
  if (busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const payload = {...form.value}; delete payload.id
    if (view.value === 'users') {
      if (!editing.value && (!payload.username?.trim() || !payload.password)) throw new Error('用户名和密码不能为空')
      if (editing.value) delete payload.username
      if (!payload.password) delete payload.password
    } else {
      payload.model_id = String(payload.model_id || '').trim()
      if (payload.scope === 'model') payload.scope_id = 0
      else if (!Number(payload.scope_id)) throw new Error('请选择作用域对象')
    }
    await mutate(`${endpoints[view.value]}${editing.value ? `/${editing.value}` : ''}`,editing.value ? 'PATCH':'POST',payload)
    clearForms(); await loadResource(); notice.value = '修改已保存。'
  } catch(e) { error.value = e instanceof Error ? e.message : '保存失败' }
  finally { busy.value = false }
}
async function toggleResource(row:any) {
  if (busy.value) return
  if (view.value === 'keys' && (row.user_id === me.value?.id || row.active === false)) return
  if (!window.confirm(view.value === 'keys' ? `停用「${row.name}」将使其客户端无法继续调用；确认停用？` : `确认${row.active === false ? '启用':'停用'}「${row.name || row.username || row.id}」？`)) return
  busy.value = true; error.value = ''
  try { await mutate(`${endpoints[view.value]}/${row.id}`,'PATCH',{active:row.active === false}); await loadResource(); notice.value = '状态已更新。' }
  catch(e) { error.value = e instanceof Error ? e.message : '更新失败' } finally { busy.value = false }
}
async function removeLimit(row:any) {
  if (busy.value || !confirm(`确定删除限流规则 #${row.id}？`)) return
  busy.value = true; error.value = ''
  try { await mutate(`admin/rate-limits/${row.id}`,'DELETE'); await loadResource(); notice.value = '规则已删除。' }
  catch(e) { error.value = e instanceof Error ? e.message : '删除失败' } finally { busy.value = false }
}
function priceState(price:any) {
  const normalize = (rates:any) => rates && typeof rates === 'object' && !Array.isArray(rates)
    ? Object.fromEntries(Object.entries(rates).map(([key,value]) => [key,typeof value === 'number' && Number.isFinite(value) ? String(value) : value])) : rates
  const state = priceClassification({cost:normalize(price.cost),credits:normalize(price.credits)})
  return {free:'免费（成本和积分均为 0）',credits_free:'免积分（仍记录上游成本）',priced:'独立双账定价',unpriced:'未定价',invalid:'配置不完整'}[state]
}
function time(value:string) { return value ? `${new Date(value).toISOString().replace('T',' ').replace('Z',' UTC')}` : '—' }
async function priceSaved() { showForm.value = false; await loadResource(); notice.value = '价格版本已创建；历史价格和账本保持不变。' }
function globalKey(e:KeyboardEvent) { if (e.key === 'Escape') { accountMenu.value = false; mobileNav.value = false } }
onMounted(() => { window.addEventListener('popstate',restoreRoute); window.addEventListener('hashchange',restoreRoute); window.addEventListener('keydown',globalKey); void checkAuth() })
onUnmounted(() => { generation++; password.value = ''; form.value = {}; window.removeEventListener('popstate',restoreRoute); window.removeEventListener('hashchange',restoreRoute); window.removeEventListener('keydown',globalKey) })
</script>

<template>
  <div v-if="loading" class="center" role="status">正在加载网关…</div>
  <div v-else-if="!authenticated" class="login-page">
    <section class="login-hero"><div class="brand"><span class="logo">✦</span><div><strong>omni-gateway</strong><small>管理控制台</small></div></div><h1>一个入口，管好全部模型流量</h1><p class="tagline">统一接入上游供应商，集中治理密钥与限流，每次调用都留痕可审计。</p><ul class="hero-points"><li><span class="point-icon">⇄</span><div><b>多上游路由</b>按优先级调用可用供应商，输出开始后不再切换。</div></li><li><span class="point-icon">⌁</span><div><b>本人密钥</b>安全签发自己的密钥，按模型白名单接入。</div></li><li><span class="point-icon">≋</span><div><b>完整计量</b>免费和收费调用均保留用量，成本与积分独立核算。</div></li></ul></section>
    <section class="login-panel"><div class="login-card"><h1>登录网关</h1><p class="muted">管理员和普通用户均可登录，进入各自允许的视图。</p><form @submit.prevent="login"><label>用户名<input v-model="username" autocomplete="username" required /></label><label>密码<input v-model="password" type="password" autocomplete="current-password" required /></label><p v-if="error" class="error" role="alert">{{ error }}</p><p v-if="oauthError" class="error" role="alert">{{ oauthError }}</p><button class="primary" :disabled="loginBusy">{{ loginBusy ? '登录中…' : '登录' }}</button></form><template v-if="oidcEnabled"><div class="oauth-divider">或使用统一身份认证</div><a class="oauth-button" :href="api.oidc.startUrl()">使用企业账号登录</a></template></div></section>
  </div>
  <div v-else class="shell">
    <aside :class="{ 'mobile-open':mobileNav }" id="task-nav"><div class="brand side-brand"><span class="logo">✦</span><div><strong>omni-gateway</strong><small>{{ isAdmin ? '管理控制台':'账号中心' }}</small></div></div><nav aria-label="任务导航"><section v-for="group in navGroups" :key="group.label" class="nav-group"><h2>{{ group.label }}</h2><button v-for="item in group.items" :key="item" :aria-label="labels[item]" :title="labels[item]" :class="{active:view===item}" @click="navigate(item as View)">{{ labels[item] }}</button></section></nav><div class="side-foot"><span class="status-dot" /> 已登录<button class="logout" :disabled="logoutBusy" @click="logout">退出登录</button></div></aside>
    <main><header><div class="page-title"><button class="secondary nav-toggle" aria-controls="task-nav" :aria-expanded="mobileNav" @click="mobileNav=!mobileNav">菜单</button><div><p class="eyebrow">{{ isAdmin ? '管理控制台':'账号中心' }}</p><h1>{{ title }}</h1></div></div><div class="header-actions"><span class="pill">{{ isAdmin ? '管理会话':'普通用户' }}</span><div class="account-entry"><button class="secondary username-entry" :aria-expanded="accountMenu" aria-controls="account-menu" @click="accountMenu=!accountMenu">{{ me.username }}</button><div v-if="accountMenu" id="account-menu" class="account-menu"><button @click="navigate('account')">账号中心</button><button @click="navigate('myKeys')">我的密钥</button><button :disabled="logoutBusy" @click="logout">退出登录</button></div></div></div></header>
      <section class="content"><p v-if="routeWarning" class="alert" role="alert">{{ routeWarning }}</p><p v-if="error" class="alert" role="alert">{{ error }} <button @click="loadResource">重试读取</button></p><p v-if="notice" class="notice" role="status">{{ notice }}</p>
        <PersonalPane v-if="isPersonal" :key="`${me.id}-${view}`" :user="me" :view="view as 'account'|'myKeys'|'myRequests'" :context="context" @user-refresh="refreshUser" @context-change="changeContext" @navigate="navigate" />
        <DashboardPane v-else-if="view==='dashboard'" :context="context" @context-change="changeContext" @navigate="navigate" />
        <OperationsPane v-else-if="isOperations" :key="`${me.id}-${view}`" :view="view" :context="context" @context-change="changeContext" @navigate="navigate" />
        <OidcProviderSettings v-else-if="view==='settings'" />
        <template v-else>
          <div class="toolbar"><div><h2>{{ title }}</h2><p class="muted">{{ view==='keys' ? '只能查看安全信息和停用他人密钥；本人密钥请进入「我的 API 密钥」。' : view==='prices' ? '价格版本不可变；上游模型名优先匹配，别名价格只作回退。' : '按名称维护账号与速率策略。' }}</p></div><div class="toolbar-actions"><button v-if="view!=='keys'" class="primary compact" :disabled="busy || resourceLoading" @click="view==='prices' ? showForm=!showForm : editResource()">{{ showForm ? '关闭':' + 新建' }}</button><button class="secondary" :disabled="busy || resourceLoading" @click="loadResource">↻ 刷新</button></div></div>
          <p v-if="resourceLoading" role="status" class="muted">加载中…</p>
          <PriceEditor v-if="view==='prices' && showForm" :prices="rows" :model-options="modelOptions" :initial-model="context.model || ''" @saved="priceSaved" @cancel="showForm=false" />
          <form v-else-if="showForm" class="panel form-panel" @submit.prevent="saveResource"><h3>{{ editing ? '编辑':'新建' }}{{ title }}</h3><div class="form-grid">
            <template v-if="view==='users'"><label>用户名<input v-model="form.username" :disabled="editing!==null" required /></label><label>显示名称<input v-model="form.display_name" /></label><label>角色<select v-model="form.role"><option value="user">用户</option><option value="admin">管理员</option></select></label><label>密码<input v-model="form.password" type="password" autocomplete="new-password" :placeholder="editing ? '留空保持原密码':''" /></label></template>
            <template v-else-if="view==='limits'"><label>作用域<select v-model="form.scope" @change="scopeChanged"><option value="user">用户</option><option value="key">API 密钥</option><option value="model">模型</option></select></label><label v-if="form.scope!=='model'">作用域对象<select v-model.number="form.scope_id"><option v-for="item in limitTargets" :key="item.id" :value="item.id">{{ item.name }} · #{{ item.id }}</option><option v-if="form.scope_id && !limitTargets.some(x=>Number(x.id)===Number(form.scope_id))" :value="form.scope_id">#{{ form.scope_id }}（对象已不存在）</option></select></label><label>限定模型<select v-model="form.model_id"><option value="">全部模型</option><option v-for="alias in optionAliases" :key="alias.id" :value="alias.name">{{ alias.name }}</option></select></label><label>每分钟请求数（RPM）<input v-model.number="form.rpm" type="number" min="0" /></label><label>每分钟 Token 数（TPM）<input v-model.number="form.tpm" type="number" min="0" /></label><label>并发数<input v-model.number="form.concurrency" type="number" min="0" /></label><p class="muted wide">0 表示不限制对应维度，限流不依赖用户组或历史预算。</p></template>
            <label class="check"><input v-model="form.active" type="checkbox" />启用</label></div><div class="toolbar-actions"><button class="primary compact" :disabled="busy">{{ busy ? '保存中…':'保存' }}</button><button type="button" class="secondary" :disabled="busy" @click="clearForms">取消</button></div></form>
          <article v-if="!resourceLoading && !error && view==='prices'" class="panel price-list"><h3>价格版本</h3><p class="muted">新版本只影响后续结算，不重算历史。金额为零并不代表缺价请求免费。</p><div v-for="row in rows" :key="row.id" class="price-row"><strong>{{ row.model_id }} · 版本 {{ row.version }}</strong><span class="tag">{{ priceState(row) }}</span><details><summary>查看准确每单位费率</summary><p>成本（CNY）</p><pre>{{ JSON.stringify(row.cost,null,2) }}</pre><p>积分</p><pre>{{ JSON.stringify(row.credits,null,2) }}</pre></details></div><p v-if="!rows.length" class="empty">暂无价格版本；有意免费请显式创建零费率版本。</p></article>
          <article v-else-if="!resourceLoading && !error" class="panel table-panel"><div v-if="rows.length" class="table"><div class="table-head"><span>名称</span><span>状态</span><span>详情</span><span>操作</span></div><div v-for="row in rows" :key="row.id" class="table-row"><strong>{{ row.name || row.username || `#${row.id}` }}</strong><span class="tag" :class="{inactive:row.active===false}">{{ view==='keys' ? keyState(row) : row.active===false ? '停用':'启用' }}</span><span class="muted">{{ resourceDetails(row) }}</span><span class="row-actions"><template v-if="view==='keys'"><button v-if="row.user_id===me.id" class="secondary compact" @click="navigate('myKeys',{key:String(row.id)})">本人管理</button><button v-else-if="row.active!==false" class="secondary compact danger" :disabled="busy" @click="toggleResource(row)">停用</button><span v-else class="muted">仅查看</span></template><template v-else><button class="secondary compact" :disabled="busy" @click="editResource(row)">编辑</button><button class="secondary compact" :disabled="busy" @click="toggleResource(row)">{{ row.active===false ? '启用':'停用' }}</button><button v-if="view==='limits'" class="secondary compact danger" :disabled="busy" @click="removeLimit(row)">删除</button></template></span></div></div><div v-else class="empty">暂无记录</div></article>
        </template>
      </section><footer>omni-gateway · 成本与积分独立计量 · <a href="/healthz" target="_blank" rel="noopener">健康检查</a></footer>
    </main>
  </div>
</template>
