<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6" data-testid="admin-ip-grants">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">{{ t('adminDomesticIp.title') }}</h1>
          <p class="mt-2 text-sm leading-6 text-gray-500 dark:text-dark-400">{{ t('adminDomesticIp.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary gap-2" :disabled="loading || Boolean(saving)" @click="loadList">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />{{ t('common.refresh') }}
        </button>
      </div>

      <div class="rounded-xl border border-primary-100 bg-primary-50/70 p-4 text-sm leading-6 dark:border-primary-900 dark:bg-primary-900/10">
        <p class="font-medium text-primary-800 dark:text-primary-300">{{ t('adminDomesticIp.immediate') }}</p>
        <p class="mt-1 text-gray-600 dark:text-dark-300">{{ t('adminDomesticIp.scope') }}</p>
      </div>
      <p v-if="state && !state.enabled" role="status" class="rounded-xl bg-amber-50 p-4 text-sm leading-6 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ t('adminDomesticIp.disabled') }}</p>
      <p v-if="loadError" role="alert" data-testid="load-error" class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ loadError }}</p>

      <section class="card p-5 sm:p-6">
        <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('adminDomesticIp.addTitle') }}</h2>
        <form class="mt-4 space-y-4" data-testid="add-form" @submit.prevent="addIP">
          <div class="grid gap-4 md:grid-cols-2">
            <div>
              <label for="admin-grant-ip" class="input-label">{{ t('adminDomesticIp.ipLabel') }}</label>
              <input id="admin-grant-ip" v-model="ip" data-testid="ip-input" class="input font-mono" type="text" required maxlength="45" autocomplete="off" spellcheck="false" :placeholder="t('adminDomesticIp.ipPlaceholder')" :disabled="Boolean(saving) || !state?.enabled" aria-describedby="admin-grant-ip-hint" />
              <p id="admin-grant-ip-hint" class="mt-2 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ t('adminDomesticIp.ipHint') }}</p>
            </div>
            <div>
              <label for="admin-grant-note" class="input-label">{{ t('adminDomesticIp.noteLabel') }}</label>
              <input id="admin-grant-note" v-model="note" data-testid="note-input" class="input" type="text" maxlength="500" :placeholder="t('adminDomesticIp.notePlaceholder')" :disabled="Boolean(saving) || !state?.enabled" />
            </div>
          </div>
          <p v-if="addError" role="alert" data-testid="add-error" class="text-sm text-red-600 dark:text-red-400">{{ addError }}</p>
          <button type="submit" class="btn btn-primary gap-2" data-testid="add-ip" :disabled="!canAdd">
            <Icon name="plus" size="sm" />{{ saving === 'add' ? t('adminDomesticIp.adding') : t('adminDomesticIp.add') }}
          </button>
        </form>
      </section>

      <section class="card overflow-hidden" :aria-busy="loading">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-5 dark:border-dark-700 sm:px-6">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('adminDomesticIp.listTitle') }}</h2>
            <p v-if="state" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('adminDomesticIp.total', { count: state.total }) }}</p>
          </div>
          <form class="flex w-full gap-2 sm:w-auto" data-testid="search-form" @submit.prevent="searchList">
            <label for="admin-grant-search" class="sr-only">{{ t('adminDomesticIp.searchLabel') }}</label>
            <input id="admin-grant-search" v-model="search" class="input min-w-0 sm:w-60" type="search" maxlength="200" :placeholder="t('adminDomesticIp.searchPlaceholder')" :disabled="Boolean(saving)" />
            <button class="btn btn-secondary shrink-0" type="submit" :disabled="loading || Boolean(saving)">{{ t('common.search') }}</button>
          </form>
        </div>
        <div v-if="loading" class="flex justify-center py-12"><LoadingSpinner /></div>
        <template v-else-if="state">
          <div v-if="state.items.length" class="overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-900/40 dark:text-dark-400">
                <tr>
                  <th scope="col" class="px-5 py-3 font-medium sm:px-6">{{ t('adminDomesticIp.ipLabel') }}</th>
                  <th scope="col" class="px-5 py-3 font-medium">{{ t('adminDomesticIp.note') }}</th>
                  <th scope="col" class="px-5 py-3 font-medium">{{ t('adminDomesticIp.createdAt') }}</th>
                  <th scope="col" class="px-5 py-3 text-right font-medium sm:px-6">{{ t('adminDomesticIp.action') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="entry in state.items" :key="entry.id" :data-testid="`grant-${entry.id}`">
                  <td class="px-5 py-4 font-mono text-gray-900 dark:text-white sm:px-6">{{ entry.ip }}</td>
                  <td class="min-w-32 max-w-xs break-words px-5 py-4 text-gray-600 dark:text-dark-300">{{ entry.note || '—' }}</td>
                  <td class="whitespace-nowrap px-5 py-4 text-gray-500 dark:text-dark-400">{{ formatDateTime(entry.created_at) }}</td>
                  <td class="px-5 py-4 text-right sm:px-6">
                    <button type="button" class="rounded-lg px-3 py-1.5 font-medium text-red-600 transition-colors hover:bg-red-50 disabled:opacity-50 dark:text-red-400 dark:hover:bg-red-900/20" :data-testid="`remove-${entry.id}`" :disabled="Boolean(saving)" @click="openRemove(entry)">{{ t('adminDomesticIp.remove') }}</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="px-5 py-12 text-center text-sm text-gray-500 dark:text-dark-400">{{ appliedSearch ? t('adminDomesticIp.noMatches') : t('adminDomesticIp.empty') }}</p>
          <fieldset v-if="state.total > 0" :disabled="Boolean(saving)">
            <Pagination :page="state.page" :page-size="state.page_size" :total="state.total" @update:page="changePage" @update:pageSize="changePageSize" />
          </fieldset>
        </template>
      </section>

      <section v-if="state" class="card p-5 sm:p-6" data-testid="fixed-grants">
        <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('adminDomesticIp.staticTitle') }}</h2>
        <p class="mt-2 text-sm leading-6 text-gray-500 dark:text-dark-400">{{ t('adminDomesticIp.staticHint') }}</p>
        <ul v-if="state.static_ips.length" class="mt-4 flex flex-wrap gap-2">
          <li v-for="address in state.static_ips" :key="address" class="max-w-full break-all rounded-lg bg-gray-100 px-3 py-2 font-mono text-sm text-gray-700 dark:bg-dark-700 dark:text-dark-200">{{ address }}</li>
        </ul>
        <p v-else class="mt-3 text-sm text-gray-400">{{ t('adminDomesticIp.staticEmpty') }}</p>
      </section>
    </div>

    <BaseDialog :show="Boolean(pendingRemove)" :title="t('adminDomesticIp.removeTitle')" width="narrow" :show-close-button="!saving" :close-on-escape="!saving" @close="closeRemove">
      <p class="break-words text-sm leading-6 text-gray-700 dark:text-dark-200">{{ t('adminDomesticIp.removeMessage', { ip: pendingRemove?.ip || '' }) }}</p>
      <p class="mt-3 text-sm leading-6 text-gray-500 dark:text-dark-400">{{ t('adminDomesticIp.removeHint') }}</p>
      <p v-if="removeError" role="alert" data-testid="remove-error" class="mt-3 text-sm text-red-600 dark:text-red-400">{{ removeError }}</p>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="Boolean(saving)" @click="closeRemove">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn-danger" data-testid="confirm-remove" :disabled="Boolean(saving)" @click="removeIP">{{ saving === 'remove' ? t('adminDomesticIp.removing') : t('adminDomesticIp.remove') }}</button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminDomesticIPAPI, type AdminDomesticIPGrant, type AdminDomesticIPGrantList } from '@/api/admin/domesticIp'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
const state = ref<AdminDomesticIPGrantList | null>(null)
const loading = ref(false)
const saving = ref<'add' | 'remove' | null>(null)
const loadError = ref('')
const addError = ref('')
const removeError = ref('')
const ip = ref('')
const note = ref('')
const search = ref('')
const appliedSearch = ref('')
const page = ref(1)
const pageSize = ref(20)
const pendingRemove = ref<AdminDomesticIPGrant | null>(null)
const canAdd = computed(() => Boolean(state.value?.enabled && !loading.value && !saving.value && ip.value.trim()))
let active = true
let request = 0

function errorMessage(error: unknown, fallback: string): string {
  const message = (error as { message?: unknown })?.message
  return typeof message === 'string' && message ? message : t(fallback)
}

async function loadList() {
  const current = ++request
  loading.value = true
  loadError.value = ''
  try {
    let result = await adminDomesticIPAPI.list(page.value, pageSize.value, appliedSearch.value)
    if (!active || current !== request) return
    const lastPage = Math.max(1, Math.ceil(result.total / result.page_size))
    if (result.page > lastPage) {
      page.value = lastPage
      result = await adminDomesticIPAPI.list(page.value, pageSize.value, appliedSearch.value)
      if (!active || current !== request) return
    }
    state.value = result
    page.value = result.page
    pageSize.value = result.page_size
  } catch (error) {
    if (active && current === request) loadError.value = errorMessage(error, 'adminDomesticIp.loadFailed')
  } finally {
    if (active && current === request) loading.value = false
  }
}

function searchList() {
  if (saving.value) return
  appliedSearch.value = search.value.trim()
  page.value = 1
  void loadList()
}
function changePage(value: number) {
  if (saving.value) return
  page.value = value
  void loadList()
}
function changePageSize(value: number) {
  if (saving.value) return
  pageSize.value = value
  page.value = 1
  void loadList()
}
async function addIP() {
  if (!canAdd.value) return
  const address = ip.value.trim()
  addError.value = ''
  if (!/^[\da-fA-F:.]+$/.test(address) || (!address.includes(':') && !address.includes('.'))) {
    addError.value = t('adminDomesticIp.invalidIP')
    return
  }
  saving.value = 'add'
  try {
    await adminDomesticIPAPI.add(address, note.value.trim())
    if (!active) return
    ip.value = ''
    note.value = ''
    search.value = ''
    appliedSearch.value = ''
    page.value = 1
    appStore.showSuccess(t('adminDomesticIp.added'))
    await loadList()
  } catch (error) {
    if (active) addError.value = errorMessage(error, 'adminDomesticIp.addFailed')
  } finally {
    if (active) saving.value = null
  }
}
function openRemove(entry: AdminDomesticIPGrant) {
  if (saving.value) return
  pendingRemove.value = entry
  removeError.value = ''
}
function closeRemove() {
  if (!saving.value) pendingRemove.value = null
}
async function removeIP() {
  if (saving.value || !pendingRemove.value) return
  saving.value = 'remove'
  removeError.value = ''
  try {
    await adminDomesticIPAPI.remove(pendingRemove.value.id)
    if (!active) return
    pendingRemove.value = null
    appStore.showSuccess(t('adminDomesticIp.removed'))
    await loadList()
  } catch (error) {
    if (active) removeError.value = errorMessage(error, 'adminDomesticIp.removeFailed')
  } finally {
    if (active) saving.value = null
  }
}
onMounted(loadList)
onUnmounted(() => { active = false; request++ })
</script>
