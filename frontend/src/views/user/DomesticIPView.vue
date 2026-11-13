<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6" data-testid="domestic-ip-page">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div class="mb-2 flex items-center gap-2 text-sm font-medium text-primary-600 dark:text-primary-400">
            <Icon name="globe" size="sm" />
            <span>{{ t('domesticIp.entry') }}</span>
          </div>
          <h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">{{ t('domesticIp.title') }}</h1>
          <p class="mt-2 max-w-3xl text-sm leading-6 text-gray-500 dark:text-dark-400">{{ t('domesticIp.intro') }}</p>
        </div>
        <button type="button" class="btn btn-secondary gap-2" :disabled="loading || saving" @click="loadState">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
          {{ t('domesticIp.refresh') }}
        </button>
      </div>

      <div v-if="loadError" role="alert" class="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
        {{ loadError }}
      </div>
      <div v-if="loading && !state" class="flex justify-center py-20"><LoadingSpinner /></div>
      <div v-else-if="state && !state.enabled" class="card px-6 py-12 text-center">
        <Icon name="globe" size="xl" class="mx-auto mb-4 text-gray-400" />
        <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('domesticIp.unavailable') }}</h2>
        <p class="mt-2 text-sm text-gray-500">{{ t('domesticIp.unavailableHint') }}</p>
      </div>

      <template v-else-if="state">
        <div class="grid gap-5 lg:grid-cols-[minmax(0,1.6fr)_minmax(300px,1fr)]">
          <section class="card overflow-hidden">
            <div class="border-b border-gray-100 bg-gradient-to-br from-primary-50/80 to-white p-6 dark:border-dark-700 dark:from-primary-900/10 dark:to-dark-800 sm:p-7">
              <div class="flex flex-wrap items-center justify-between gap-3">
                <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('domesticIp.currentIP') }}</h2>
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('domesticIp.detected') }}</span>
              </div>
              <p v-if="state.current_ip_valid" data-testid="current-ip" class="my-6 break-all font-mono text-3xl font-semibold tracking-tight text-gray-900 dark:text-white sm:text-4xl">{{ state.current_ip }}</p>
              <p v-else role="status" class="my-6 text-sm leading-6 text-amber-700 dark:text-amber-300">{{ t('domesticIp.invalidIP') }}</p>
              <div v-if="currentRegistered || canAdd" class="flex flex-wrap items-center gap-2 text-sm" :class="currentRegistered && state.eligible ? 'text-emerald-700 dark:text-emerald-400' : 'text-gray-500 dark:text-dark-400'">
                <Icon :name="currentRegistered ? 'checkCircle' : 'infoCircle'" size="sm" />
                <span>{{ currentRegistered ? t('domesticIp.registered') : t('domesticIp.ready') }}</span>
              </div>
            </div>
            <div class="space-y-4 p-6 sm:p-7">
              <p class="text-sm leading-6 text-gray-500 dark:text-dark-400">{{ currentRegistered ? t('domesticIp.registeredHint') : t('domesticIp.readyHint') }}</p>
              <button type="button" data-testid="add-ip" class="btn btn-primary w-full gap-2 sm:w-auto" :disabled="!canAdd" @click="openAction('add')">
                <Icon :name="currentRegistered ? 'check' : 'plus'" size="sm" />
                {{ currentRegistered ? t('domesticIp.registered') : t('domesticIp.add') }}
              </button>
              <p v-if="state.changes_remaining <= 0" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t('domesticIp.dailyLimit') }}</p>
              <p v-else-if="state.entries.length >= state.max_ips && !currentRegistered" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t('domesticIp.full', { limit: state.max_ips }) }}</p>
            </div>
          </section>

          <section class="card flex flex-col p-6 sm:p-7">
            <div class="flex items-center gap-2 text-sm font-medium" :class="state.eligible ? 'text-emerald-700 dark:text-emerald-400' : 'text-amber-700 dark:text-amber-300'">
              <Icon :name="state.eligible ? 'checkCircle' : 'clock'" size="sm" />
              {{ state.eligible ? t('domesticIp.eligible') : t('domesticIp.ineligible') }}
            </div>
            <p class="mt-5 text-sm text-gray-500 dark:text-dark-400">{{ t('domesticIp.paid') }}</p>
            <p class="mt-2 text-3xl font-semibold tabular-nums text-gray-900 dark:text-white">¥{{ money(state.paid_amount_cny) }}</p>
            <div class="mb-2 mt-4 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" role="progressbar" :aria-label="t('domesticIp.paid')" :aria-valuenow="Math.min(200, state.paid_amount_cny)" :aria-valuemin="0" :aria-valuemax="200">
              <div class="h-full rounded-full bg-primary-500 transition-all" :style="{ width: `${Math.min(100, Math.max(0, state.paid_amount_cny / 2))}%` }"></div>
            </div>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('domesticIp.threshold') }}</p>
            <p v-if="!state.eligible" data-testid="shortfall" class="mt-4 text-sm font-medium text-amber-700 dark:text-amber-300">{{ t('domesticIp.shortfall', { amount: money(state.remaining_amount_cny) }) }}</p>
            <RouterLink v-if="!state.eligible && paymentEnabled" to="/purchase" class="btn btn-secondary mt-4 w-full gap-2">{{ t('domesticIp.recharge') }}<Icon name="arrowRight" size="sm" /></RouterLink>
            <p class="mt-auto pt-5 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ t('domesticIp.paidHint') }}</p>
          </section>
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="card flex items-center justify-between gap-4 px-5 py-4">
            <div class="flex items-center gap-3"><span class="rounded-lg bg-primary-50 p-2 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400"><Icon name="globe" size="md" /></span><span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('domesticIp.slots') }}</span></div>
            <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ state.entries.length }} <span class="text-sm font-normal text-gray-400">{{ t('domesticIp.ofLimit', { limit: state.max_ips }) }}</span></p>
          </div>
          <div class="card flex items-center justify-between gap-4 px-5 py-4">
            <div class="flex items-center gap-3"><span class="rounded-lg bg-primary-50 p-2 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400"><Icon name="clock" size="md" /></span><div><p class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('domesticIp.changes') }}</p><p class="mt-1 text-xs text-gray-400">{{ t('domesticIp.resetHint') }}</p></div></div>
            <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ state.changes_remaining }} <span class="text-sm font-normal text-gray-400">{{ t('domesticIp.ofLimit', { limit: state.daily_limit }) }}</span></p>
          </div>
        </div>

        <section class="card overflow-hidden">
          <div class="border-b border-gray-100 px-6 py-5 dark:border-dark-700"><h2 class="font-semibold text-gray-900 dark:text-white">{{ t('domesticIp.listTitle') }}</h2><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('domesticIp.listHint') }}</p></div>
          <div v-if="!state.eligible && state.entries.length" role="status" class="border-b border-amber-100 bg-amber-50 px-6 py-4 text-sm leading-6 text-amber-800 dark:border-amber-900 dark:bg-amber-950/20 dark:text-amber-300">{{ t('domesticIp.pausedHint') }}</div>
          <div v-if="!state.entries.length" class="px-6 py-12 text-center"><Icon name="globe" size="xl" class="mx-auto mb-3 text-gray-300 dark:text-dark-500" /><p class="font-medium text-gray-900 dark:text-white">{{ t('domesticIp.emptyTitle') }}</p><p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('domesticIp.emptyHint') }}</p></div>
          <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
            <li v-for="entry in state.entries" :key="entry.id" class="flex flex-col gap-4 px-6 py-5 sm:flex-row sm:items-center sm:justify-between" :data-testid="`entry-${entry.id}`">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2"><span class="break-all font-mono text-sm font-semibold text-gray-900 dark:text-white">{{ entry.ip }}</span><span v-if="entry.ip === state.current_ip" class="rounded-md bg-primary-50 px-2 py-0.5 text-xs text-primary-700 dark:bg-primary-900/20 dark:text-primary-300">{{ t('domesticIp.current') }}</span><span class="rounded-md px-2 py-0.5 text-xs" :class="state.eligible ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'">{{ state.eligible ? t('domesticIp.active') : t('domesticIp.paused') }}</span></div>
                <p class="mt-2 text-xs text-gray-400">{{ t('domesticIp.registeredAt') }} · {{ formatDate(entry.created_at) }}</p>
              </div>
              <div class="flex shrink-0 flex-wrap items-center gap-3">
                <button v-if="entry.ip !== state.current_ip" type="button" class="btn btn-secondary gap-1.5 text-xs" :disabled="!canReplace" :data-testid="`replace-${entry.id}`" @click="openAction('replace', entry)"><Icon name="sync" size="sm" />{{ t('domesticIp.replace') }}</button>
                <button type="button" class="rounded-lg px-2 py-2 text-xs font-medium text-red-600 hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-40 dark:text-red-400 dark:hover:bg-red-900/20" :disabled="!canRemove" :data-testid="`remove-${entry.id}`" @click="openAction('remove', entry)">{{ t('domesticIp.remove') }}</button>
              </div>
            </li>
          </ul>
        </section>

        <section class="px-1 text-sm leading-6 text-gray-500 dark:text-dark-400">
          <h2 class="mb-2 flex items-center gap-2 font-medium text-gray-700 dark:text-gray-300"><Icon name="infoCircle" size="sm" />{{ t('domesticIp.rulesTitle') }}</h2>
          <ul class="list-disc space-y-1 pl-6"><li>{{ t('domesticIp.ruleChanges') }}</li><li>{{ t('domesticIp.ruleShared') }}</li><li>{{ t('domesticIp.ruleAccess') }}</li></ul>
        </section>
      </template>

      <BaseDialog :show="pending !== null" :title="dialogTitle" width="narrow" :show-close-button="!saving" :close-on-escape="!saving" @close="cancelAction">
        <template v-if="pending">
          <p class="text-sm leading-6 text-gray-500 dark:text-dark-400">{{ dialogDescription }}</p>
          <div class="mt-5 space-y-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div v-if="pending.entry"><p class="text-xs text-gray-400">{{ t('domesticIp.oldIP') }}</p><p class="mt-1 break-all font-mono text-sm text-gray-900 dark:text-white">{{ pending.entry.ip }}</p></div>
            <div v-if="pending.kind !== 'remove'"><p class="text-xs text-gray-400">{{ t('domesticIp.newIP') }}</p><p class="mt-1 break-all font-mono text-lg font-semibold text-gray-900 dark:text-white">{{ pending.expectedIP }}</p></div>
          </div>
          <p v-if="actionError" role="alert" class="mt-4 text-sm text-red-600 dark:text-red-400">{{ actionError }}</p>
        </template>
        <template #footer><div class="flex justify-end gap-3"><button type="button" class="btn btn-secondary" :disabled="saving" @click="cancelAction">{{ t('common.cancel') }}</button><button type="button" data-testid="confirm-action" class="btn gap-2" :class="pending?.kind === 'remove' ? 'btn-danger' : 'btn-primary'" :disabled="saving" @click="confirmAction"><LoadingSpinner v-if="saving" size="sm" color="white" />{{ saving ? t('common.submitting') : t('domesticIp.confirm') }}</button></div></template>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { domesticIPAPI, type DomesticIPGrant, type DomesticIPState } from '@/api/domesticIp'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { Icon } from '@/components/icons'
import { useAppStore } from '@/stores/app'

type Action = 'add' | 'replace' | 'remove'
interface PendingAction { kind: Action; entry?: DomesticIPGrant; expectedIP: string }
const { t, locale } = useI18n()
const appStore = useAppStore()
const state = ref<DomesticIPState | null>(null)
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const actionError = ref('')
const pending = ref<PendingAction | null>(null)
let active = true
onUnmounted(() => { active = false })

const paymentEnabled = computed(() => appStore.cachedPublicSettings?.payment_enabled === true && !appStore.cachedPublicSettings?.payment_balance_disabled)
const currentRegistered = computed(() => Boolean(state.value?.entries.some(entry => entry.ip === state.value?.current_ip)))
const canRemove = computed(() => Boolean(state.value?.enabled && state.value.changes_remaining > 0 && !loading.value && !saving.value && !loadError.value))
const canReplace = computed(() => Boolean(canRemove.value && state.value?.eligible && state.value.current_ip_valid && !currentRegistered.value))
const canAdd = computed(() => Boolean(canReplace.value && state.value && state.value.entries.length < state.value.max_ips))
const dialogTitle = computed(() => t(pending.value?.kind === 'remove' ? 'domesticIp.confirmRemove' : pending.value?.kind === 'replace' ? 'domesticIp.confirmReplace' : 'domesticIp.confirmAdd'))
const dialogDescription = computed(() => t(pending.value?.kind === 'remove' ? 'domesticIp.confirmRemoveHint' : pending.value?.kind === 'replace' ? 'domesticIp.confirmReplaceHint' : 'domesticIp.confirmAddHint'))
const money = (value: number) => value.toFixed(2)
const formatDate = (value: string) => new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Asia/Shanghai' }).format(new Date(value))
function errorMessage(error: unknown, fallback: string): string {
  const message = (error as { message?: string })?.message
  return typeof message === 'string' && message ? message : t(fallback)
}

async function loadState() {
  if (loading.value || saving.value) return
  loading.value = true
  loadError.value = ''
  try {
    const result = await domesticIPAPI.get()
    if (active) state.value = result
  } catch (error) {
    if (active) loadError.value = errorMessage(error, 'domesticIp.loadFailed')
  } finally {
    if (active) loading.value = false
  }
}

function openAction(kind: Action, entry?: DomesticIPGrant) {
  if ((kind === 'add' && !canAdd.value) || (kind === 'replace' && !canReplace.value) || (kind === 'remove' && !canRemove.value)) return
  pending.value = { kind, entry, expectedIP: state.value?.current_ip ?? '' }
  actionError.value = ''
}
function cancelAction() {
  if (!saving.value) { pending.value = null; actionError.value = '' }
}
async function confirmAction() {
  if (saving.value || !pending.value) return
  const action = pending.value
  saving.value = true
  actionError.value = ''
  try {
    const result = action.kind === 'add'
      ? await domesticIPAPI.add(action.expectedIP)
      : action.kind === 'replace'
        ? await domesticIPAPI.replace(action.entry!.id, action.expectedIP)
        : await domesticIPAPI.remove(action.entry!.id)
    if (!active) return
    state.value = result
    pending.value = null
    appStore.showSuccess(t(action.kind === 'add' ? 'domesticIp.added' : action.kind === 'replace' ? 'domesticIp.replaced' : 'domesticIp.removed'))
  } catch (error) {
    if (!active) return
    const err = error as { status?: number; reason?: string; code?: string }
    if (err.status === 409 && (err.reason === 'IP_CHANGED' || err.code === 'IP_CHANGED')) {
      pending.value = null
      appStore.showWarning(t('domesticIp.networkChanged'))
      try {
        const refreshed = await domesticIPAPI.get()
        if (active) state.value = refreshed
      } catch (refreshError) {
        if (active) loadError.value = errorMessage(refreshError, 'domesticIp.loadFailed')
      }
    } else {
      actionError.value = errorMessage(error, 'domesticIp.saveFailed')
    }
  } finally {
    if (active) saving.value = false
  }
}
onMounted(loadState)
</script>
