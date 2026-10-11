<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, onUpdated, ref } from 'vue'

defineProps<{ title: string; labelId: string }>()
const emit = defineEmits<{ close: [] }>()
const panel = ref<HTMLElement | null>(null)
let returnFocus: HTMLElement | null = null
let alive = true

function focusables(): HTMLElement[] {
  return Array.from(panel.value?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex="0"]') || [])
}

function keepFocus(event: FocusEvent) {
  if (alive && panel.value && event.target instanceof Node && !panel.value.contains(event.target)) (focusables()[0] || panel.value).focus()
}

function keyboard(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    emit('close')
  } else if (event.key === 'Tab') {
    const elements = focusables()
    const first = elements[0]
    const last = elements[elements.length - 1]
    if (!first) { event.preventDefault(); panel.value?.focus() }
    else if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value)) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
}

onMounted(async () => {
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  document.addEventListener('focusin', keepFocus)
  await nextTick()
  if (alive) (panel.value?.querySelector<HTMLElement>('[data-autofocus]') || focusables()[0] || panel.value)?.focus()
})
onUpdated(() => {
  // Slot changes (e.g. discard confirmation) can remove the focused button.
  if (alive && panel.value && !panel.value.contains(document.activeElement)) (focusables()[0] || panel.value).focus()
})
onBeforeUnmount(() => {
  alive = false
  document.removeEventListener('focusin', keepFocus)
  if (returnFocus?.isConnected) returnFocus.focus()
  returnFocus = null
})
</script>

<template>
  <div class="modal-backdrop personal-backdrop" @click.self="emit('close')">
    <section ref="panel" class="modal personal-dialog" role="dialog" aria-modal="true" :aria-labelledby="labelId" tabindex="-1" @keydown="keyboard">
      <div class="dialog-heading"><h3 :id="labelId">{{ title }}</h3><button type="button" class="secondary" aria-label="关闭弹窗" @click="emit('close')">×</button></div>
      <slot />
    </section>
  </div>
</template>

<style scoped>
.personal-backdrop { z-index: 80; }
.personal-dialog { width: min(620px, 100%); overscroll-behavior: contain; }
.dialog-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 14px; }
.dialog-heading button { flex: 0 0 auto; min-width: 36px; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
@media (max-width: 600px) { .personal-backdrop { padding: 12px; } .personal-dialog { max-height: calc(100dvh - 24px); padding: 18px; } }
</style>
