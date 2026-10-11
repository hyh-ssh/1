<script setup lang="ts">
import { computed } from 'vue'
import {
  meterLabel, priceClassification, priceMapsToRows, rateUnit, validatePriceMaps,
  type PriceClassification,
} from './priceMath'

// Presentation only: the parent owns list loading/errors, no reads or writes here.
const props = defineProps<{ prices?: any[] }>()
const labels: Record<PriceClassification, string> = {
  free: '免费', credits_free: '免积分（仍计成本）', priced: '已定价', unpriced: '未定价（不是免费）', invalid: '配置无效',
}
const rows = computed(() => (props.prices || []).map(price => {
  const classification = priceClassification(price)
  try {
    return { price, classification, meters: priceMapsToRows(validatePriceMaps(price.cost, price.credits)), error: '' }
  } catch {
    return { price, classification, meters: [], error: '配置无效，不可据此推断为免费；请核对完整的成本与积分费率，另建新版本。' }
  }
}))
</script>

<template>
  <section class="price-list" aria-label="不可变价格版本列表">
    <div v-if="!rows.length" class="panel empty">
      <p>暂无价格版本</p><small>未定价不等于免费；请新建价格版本，显式配置两账费率。</small>
    </div>
    <article v-for="(row, index) in rows" :key="row.price?.id ?? index" class="panel price-snapshot">
      <div class="snapshot-head">
        <h3>{{ row.price?.model_id || '模型未知' }} · v{{ row.price?.version ?? '未知' }}</h3>
        <span class="tag" :class="{ inactive: ['invalid', 'unpriced'].includes(row.classification) }" data-testid="price-classification">{{ labels[row.classification] }}</span>
      </div>
      <p v-if="row.error" role="status" class="muted">{{ row.error }}</p>
      <div v-else class="price-scroll" tabindex="0" role="region" :aria-label="`${row.price.model_id} v${row.price.version} 费率，可横向滚动`">
        <table>
          <caption>每单位费率的可读展示；价格版本不可变，不改写历史账本。</caption>
          <thead><tr><th scope="col">计量项 / 单位</th><th scope="col">成本（CNY）</th><th scope="col">积分费率（积分）</th></tr></thead>
          <tbody><tr v-for="meter in row.meters" :key="meter.meter" :data-meter="meter.meter">
            <th scope="row">{{ meterLabel(meter.meter) }}<small>{{ rateUnit(meter.meter) }}</small></th>
            <td><template v-if="meter.cost !== ''">¥{{ meter.cost }} CNY</template><span v-else class="muted">未配置（不是免费）</span></td>
            <td><template v-if="meter.credits !== ''">{{ meter.credits }} 积分</template><span v-else class="muted">未配置（不是免费）</span></td>
          </tr></tbody>
        </table>
      </div>
    </article>
  </section>
</template>

<style scoped>
.price-list { display: grid; gap: 14px; min-width: 0; }
.price-snapshot { min-width: 0; }
.snapshot-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.snapshot-head h3 { overflow-wrap: anywhere; }
.price-scroll { overflow-x: auto; margin-top: 12px; }
.price-scroll:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
table { width: 100%; min-width: 530px; border-collapse: collapse; text-align: left; }
caption { text-align: left; color: var(--text-muted); font-size: 12px; margin-bottom: 10px; }
th, td { padding: 12px; border-bottom: 1px solid var(--border); font-size: 13px; overflow-wrap: anywhere; }
thead { background: var(--surface-muted); color: var(--text-muted); }
tbody th { width: 32%; font-weight: 500; }
th small { display: block; color: var(--text-muted); font-weight: 400; margin-top: 5px; }
td { min-width: 150px; max-width: 360px; }
tr:last-child th, tr:last-child td { border-bottom: 0; }
</style>
