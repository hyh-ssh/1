// Decimal coefficients/exponents and all amount arithmetic stay in BigInt.
// Number is used only for bounded string positions, display scales and versions.
export type RateMap = Record<string, string>
export type PriceMaps = { cost: RateMap; credits: RateMap }
export type PriceTableRow = {
  meter: string
  cost: string
  credits: string
  originalCost?: string
  originalCredits?: string
}
export type PriceClassification = 'free' | 'credits_free' | 'priced' | 'unpriced' | 'invalid'
export const TOKEN_METERS = ['input', 'output', 'cache_input', 'cache_creation', 'reasoning', 'total'] as const
export const CREDITS_SCALE = 3 // backend/internal/model/money.go: rounded per meter, not per total.
export const CNY_DISPLAY_SCALE = 6 // Display only: upstream CNY amounts are NOT quantized by settlement.
const DECIMAL = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:[eE]([+-]?\d+))?$/
const own = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key)
type Decimal = { coefficient: bigint; exponent: bigint }
const ZERO: Decimal = { coefficient: 0n, exponent: 0n }

export function decimalSign(value: unknown): -1 | 0 | 1 | null {
  if (typeof value !== 'string') return null
  const match = DECIMAL.exec(value)
  if (!match) return null
  const digits = (match[2] || '') + (match[3] || match[4] || '')
  if (!/[1-9]/.test(digits)) return 0 // -0 and exponent zero are legitimate exact zero.
  return match[1] === '-' ? -1 : 1
}

export function validateRate(value: unknown, label = '费率'): string {
  if (typeof value !== 'string') throw new Error(`${label}：费率必须使用十进制字符串，例如 "0" 或 "1e-6"。`)
  const sign = decimalSign(value)
  if (sign === null || sign === -1) throw new Error(`${label}：请输入有效的非负数费率（可用小数或指数形式）。`)
  return value
}

function normalize(value: Decimal): Decimal {
  if (value.coefficient === 0n) return ZERO
  const digits = value.coefficient.toString()
  const trimmed = digits.replace(/0+$/, '')
  return { coefficient: BigInt(trimmed), exponent: value.exponent + BigInt(digits.length - trimmed.length) }
}

function decimal(value: string): Decimal {
  validateRate(value)
  const match = DECIMAL.exec(value)!
  const fraction = match[3] || match[4] || ''
  return normalize({
    coefficient: BigInt((match[2] || '0') + fraction),
    exponent: BigInt(match[5] || '0') - BigInt(fraction.length),
  })
}

function decimalString(value: Decimal): string {
  if (value.coefficient === 0n) return '0'
  const digits = value.coefficient.toString()
  const point = BigInt(digits.length) + value.exponent
  // Scientific notation is still exact and avoids allocating billions of zeroes.
  if (point > 4096n || point < -4096n) return `${digits}e${value.exponent}`
  if (point <= 0n) return `0.${'0'.repeat(Number(-point))}${digits}`
  if (point >= BigInt(digits.length)) return digits + '0'.repeat(Number(point - BigInt(digits.length)))
  return `${digits.slice(0, Number(point))}.${digits.slice(Number(point))}`
}

export function scaleDecimal(value: string, places: number): string {
  if (!Number.isSafeInteger(places)) throw new Error('十进制缩放位数必须为整数。')
  const parsed = decimal(value)
  return decimalString({ ...parsed, exponent: parsed.exponent + BigInt(places) })
}

export function meterIsToken(meter: string): boolean {
  return (TOKEN_METERS as readonly string[]).includes(meter)
}

export function meterLabel(meter: string): string {
  const labels: Record<string, string> = {
    input: '输入', output: '输出', cache_input: '缓存输入', cache_creation: '缓存创建', reasoning: '推理', total: '聚合 Token',
  }
  return own(labels, meter) ? `${labels[meter]}（${meter}）` : meter
}

export function rateUnit(meter: string): string {
  return meterIsToken(meter) ? '每百万 Token' : '每 1 实际计量单位'
}

export function rateToDisplay(meter: string, rate: string): string {
  validateRate(rate)
  return meterIsToken(meter) ? scaleDecimal(rate, 6) : rate
}

export function displayToRate(meter: string, rate: string): string {
  validateRate(rate)
  return meterIsToken(meter) ? scaleDecimal(rate, -6) : rate
}

export function parseRateMap(value: unknown, label: string, allowEmpty = false): RateMap {
  if (typeof value === 'string') {
    try { value = JSON.parse(value) } catch { throw new Error(`${label} JSON 格式无效，请填写计量项到十进制字符串的对象。`) }
  }
  if (value === undefined || value === null) throw new Error(`${label}必须显式提供费率表。`)
  if (typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label}费率表必须是 JSON 对象。`)
  const entries = Object.entries(value)
  if (!allowEmpty && !entries.length) throw new Error(`${label}费率表必须至少声明一个计量项。`)
  return Object.fromEntries(entries.map(([meter, rate]) => {
    if (!meter.trim()) throw new Error(`${label}计量项名称不能为空。`)
    return [meter, validateRate(rate, `${label} / ${meter}`)]
  }))
}

export function validatePriceMaps(cost: unknown, credits: unknown): PriceMaps {
  const maps = { cost: parseRateMap(cost, '成本'), credits: parseRateMap(credits, '积分', true) }
  for (const meter of Object.keys(maps.cost)) {
    if (!own(maps.credits, meter)) throw new Error(`积分 / ${meter}：成本已配置，必须显式填写积分费率；免积分请填 "0"。`)
  }
  if (!Object.keys(maps.credits).length) throw new Error('积分费率表必须至少声明一个计量项。')
  return maps
}

export function priceMapsToRows(maps: PriceMaps): PriceTableRow[] {
  const valid = validatePriceMaps(maps.cost, maps.credits)
  return [...new Set([...Object.keys(valid.cost), ...Object.keys(valid.credits)])].map(meter => ({
    meter,
    cost: own(valid.cost, meter) ? rateToDisplay(meter, valid.cost[meter]!) : '',
    credits: own(valid.credits, meter) ? rateToDisplay(meter, valid.credits[meter]!) : '',
    originalCost: own(valid.cost, meter) ? valid.cost[meter] : undefined,
    originalCredits: own(valid.credits, meter) ? valid.credits[meter] : undefined,
  }))
}

export function priceRowsToMaps(rows: PriceTableRow[], allowIncomplete = false): PriceMaps {
  const maps: PriceMaps = { cost: Object.create(null), credits: Object.create(null) }
  const meters = new Set<string>()
  for (const row of rows) {
    if (!row.meter.trim()) throw new Error('计量项名称不能为空。')
    if (meters.has(row.meter)) throw new Error(`计量项 ${row.meter} 重复。`)
    meters.add(row.meter)
    for (const ledger of ['cost', 'credits'] as const) {
      const display = row[ledger]
      if (display === '') continue // Blank means missing, never an implicit zero.
      validateRate(display, `${ledger === 'cost' ? '成本' : '积分'} / ${row.meter}`)
      const original = ledger === 'cost' ? row.originalCost : row.originalCredits
      maps[ledger][row.meter] = original !== undefined && display === rateToDisplay(row.meter, original)
        ? original : displayToRate(row.meter, display)
    }
  }
  return allowIncomplete ? maps : validatePriceMaps(maps.cost, maps.credits)
}

// Validate both inputs before changing anything; the shortcuts never save.
export function freePriceMaps(cost: unknown, credits: unknown, creditsOnly = false): PriceMaps {
  const validCost = parseRateMap(cost, '成本', true)
  const validCredits = parseRateMap(credits, '积分', true)
  const meters = new Set(['input', 'output', ...Object.keys(validCost), ...Object.keys(validCredits)])
  const zeroes = Object.fromEntries([...meters].map(meter => [meter, '0']))
  return { cost: creditsOnly ? validCost : { ...zeroes }, credits: { ...zeroes } }
}

export function priceClassification(row: unknown): PriceClassification {
  if (!row || typeof row !== 'object' || (!own(row, 'cost') && !own(row, 'credits'))) return 'unpriced'
  try {
    const candidate = row as { cost?: unknown; credits?: unknown }
    const maps = validatePriceMaps(candidate.cost, candidate.credits)
    const costZero = Object.values(maps.cost).every(rate => decimalSign(rate) === 0)
    const creditsZero = Object.values(maps.credits).every(rate => decimalSign(rate) === 0)
    return costZero && creditsZero ? 'free' : creditsZero ? 'credits_free' : 'priced'
  } catch { return 'invalid' }
}

export const isExplicitFreePrice = (row: unknown): boolean => priceClassification(row) === 'free'
export const isCreditsWaivedPrice = (row: unknown): boolean => priceClassification(row) === 'credits_free'

function power(places: bigint): bigint {
  // This only bounds expanded local arithmetic, not accepted/stored rate precision.
  if (places > 100000n) throw new Error('精度跨度过大，本地试算无法展开；费率不会被修改。')
  return 10n ** places
}

function add(left: Decimal, right: Decimal): Decimal {
  if (left.coefficient === 0n) return right
  if (right.coefficient === 0n) return left
  const exponent = left.exponent < right.exponent ? left.exponent : right.exponent
  return normalize({
    coefficient: left.coefficient * power(left.exponent - exponent) + right.coefficient * power(right.exponent - exponent),
    exponent,
  })
}

function round(value: Decimal, places: number): Decimal {
  const exponent = -BigInt(places)
  if (value.exponent >= exponent || value.coefficient === 0n) return value
  const shift = exponent - value.exponent
  if (shift > BigInt(value.coefficient.toString().length)) return ZERO
  const divisor = power(shift)
  const quotient = value.coefficient / divisor
  const remainder = value.coefficient % divisor
  return normalize({ coefficient: quotient + (remainder * 2n >= divisor ? 1n : 0n), exponent })
}

function fixed(value: Decimal, places: number): string {
  const text = decimalString(round(value, places))
  if (text.includes('e')) return text
  const [whole, fraction = ''] = text.split('.')
  return places ? `${whole}.${fraction.padEnd(places, '0')}` : whole!
}

export type PriceQuote = {
  costExact: string
  costDisplay: string
  creditsExact: string
  credits: string
  meters: { meter: string; quantity: string; costExact: string; creditsExact: string; credits: string }[]
}

// One successful attempt: cost retains precision; credits round each meter to 3
// places before summing, matching billing.Settle. Quantities are actual integers.
export function quotePrice(maps: PriceMaps, quantities: Record<string, string>): PriceQuote {
  const valid = validatePriceMaps(maps.cost, maps.credits)
  let cost = ZERO, creditsExact = ZERO, credits = ZERO
  const meters = [...new Set([...Object.keys(valid.cost), ...Object.keys(valid.credits)])].map(meter => {
    const entered = quantities[meter] // Track new reactive quantity fields as well as existing ones.
    const quantity = own(quantities, meter) ? entered! : '0'
    if (typeof quantity !== 'string' || !/^\d+$/.test(quantity)) throw new Error(`${meter} 数量必须为非负整数，单位是实际 Token / 实际计量单位。`)
    const count = BigInt(quantity)
    const costUnit = decimal(own(valid.cost, meter) ? valid.cost[meter]! : '0')
    const creditUnit = decimal(valid.credits[meter]!)
    const costAmount = normalize({ ...costUnit, coefficient: costUnit.coefficient * count })
    const creditAmount = normalize({ ...creditUnit, coefficient: creditUnit.coefficient * count })
    const creditRounded = round(creditAmount, CREDITS_SCALE)
    cost = add(cost, costAmount)
    creditsExact = add(creditsExact, creditAmount)
    credits = add(credits, creditRounded)
    return { meter, quantity, costExact: decimalString(costAmount), creditsExact: decimalString(creditAmount), credits: fixed(creditRounded, CREDITS_SCALE) }
  })
  return { costExact: decimalString(cost), costDisplay: fixed(cost, CNY_DISPLAY_SCALE), creditsExact: decimalString(creditsExact), credits: fixed(credits, CREDITS_SCALE), meters }
}

function latestPrice(prices: any[], model: string): any | null {
  return prices.reduce<any | null>((latest, price) => {
    if (!model || price?.model_id !== model || !Number.isSafeInteger(price.version) || price.version <= 0) return latest
    return !latest || price.version > latest.version ? price : latest
  }, null)
}

export function suggestNextVersion(prices: any[], model: string): number {
  return (latestPrice(prices, model)?.version || 0) + 1
}

export function resolvePrice(prices: any[], providerModel: string, alias: string): { source: 'upstream' | 'alias' | 'none'; price: any | null } {
  const upstream = latestPrice(prices, providerModel)
  if (upstream) return { source: 'upstream', price: upstream }
  const fallback = latestPrice(prices, alias)
  return fallback ? { source: 'alias', price: fallback } : { source: 'none', price: null }
}
