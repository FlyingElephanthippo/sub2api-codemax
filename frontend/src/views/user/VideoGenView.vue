<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <header>
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('videoGen.title') }}</h1>
        <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ t('videoGen.description') }}</p>
      </header>
      <div class="grid gap-6 lg:grid-cols-2">
        <form class="card space-y-5 p-5" @submit.prevent="submit">
          <div>
            <label for="video-key" class="input-label">{{ t('videoGen.apiKey') }}</label>
            <select id="video-key" v-model="selectedKeyID" class="input" :disabled="loadingKeys">
              <option value="">{{ t('videoGen.selectKey') }}</option>
              <option v-for="key in keys" :key="key.id" :value="key.id">{{ key.name }}{{ key.group?.name ? ` · ${key.group.name}` : '' }}</option>
            </select>
            <p v-if="!loadingKeys && !keys.length" class="input-hint mt-2">{{ t('videoGen.noKeys') }}</p>
          </div>
          <div>
            <label for="video-model" class="input-label">{{ t('videoGen.model') }}</label>
            <select id="video-model" v-model="model" class="input" :disabled="loadingModels || !models.length || busy">
              <option value="">{{ loadingModels ? t('videoGen.loadingModels') : t('videoGen.selectModel') }}</option>
              <option v-for="id in models" :key="id" :value="id">{{ id }}</option>
            </select>
            <p v-if="selectedKey && !loadingModels && !models.length && !modelError" class="input-hint mt-2">{{ t('videoGen.noModels') }}</p>
            <p v-if="modelError" role="alert" class="mt-2 text-sm text-red-600 dark:text-red-400">{{ modelError }}</p>
            <button v-if="modelError" class="mt-1 text-sm text-primary-600 underline" type="button" @click="loadModels">{{ t('videoGen.retry') }}</button>
          </div>
          <div>
            <label for="video-prompt" class="input-label">{{ t('videoGen.prompt') }}</label>
            <textarea id="video-prompt" v-model="prompt" class="input" rows="5" maxlength="10000" :disabled="busy" :placeholder="t('videoGen.promptPlaceholder')" />
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label for="video-duration" class="input-label">{{ t('videoGen.duration') }}</label>
              <select id="video-duration" v-model.number="duration" class="input" :disabled="busy">
                <option v-for="seconds in durations" :key="seconds" :value="seconds">{{ t('videoGen.seconds', { count: seconds }) }}</option>
              </select>
            </div>
            <div>
              <label for="video-aspect" class="input-label">{{ t('videoGen.aspectRatio') }}</label>
              <select id="video-aspect" v-model="aspectRatio" class="input" :disabled="busy">
                <option value="16:9">{{ t('videoGen.landscape') }}</option>
                <option value="9:16">{{ t('videoGen.portrait') }}</option>
              </select>
            </div>
          </div>
          <div>
            <label for="video-reference" class="input-label">{{ t('videoGen.reference') }}</label>
            <input id="video-reference" class="input" type="file" accept="image/png,image/jpeg,image/webp" :disabled="busy || readingReference" @change="selectReference" />
            <p class="input-hint mt-2">{{ t('videoGen.referenceHint') }}</p>
            <div v-if="reference" class="mt-3 flex items-center gap-3">
              <img :src="reference" class="h-20 w-20 rounded-lg object-cover" :alt="t('videoGen.reference')" />
              <button type="button" class="text-sm text-red-600" :disabled="busy" @click="clearReference">{{ t('videoGen.removeReference') }}</button>
            </div>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('videoGen.pricingNotice') }}</p>
          <button type="submit" class="btn btn-primary w-full" :disabled="!canSubmit">{{ loadingContent ? t('videoGen.loadingVideo') : busy ? t('videoGen.generating') : t('videoGen.generate') }}</button>
        </form>
        <section class="card space-y-4 p-5" :aria-label="t('videoGen.result')">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('videoGen.result') }}</h2>
          <div v-if="!task && !busy && !error" class="flex min-h-64 items-center justify-center rounded-xl border border-dashed border-gray-300 p-8 text-center text-gray-500 dark:border-dark-600">
            {{ t('videoGen.empty') }}
          </div>
          <div v-if="busy || task" class="space-y-3" aria-live="polite">
            <p>{{ statusLabel }}</p>
            <p v-if="loadingContent" class="text-sm text-gray-500">{{ downloadProgressLabel }}</p>
            <div v-if="task && task.status === 'pending'" class="h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
              <div class="h-full bg-primary-500 transition-all" :style="{ width: `${Math.max(5, task.progress)}%` }" />
            </div>
            <p v-if="task" class="break-all text-xs text-gray-500">{{ t('videoGen.taskID') }}: {{ task.id }}</p>
            <button v-if="busy && task" type="button" class="btn btn-secondary" @click="pause">{{ loadingContent ? t('videoGen.stopLoading') : t('videoGen.pause') }}</button>
            <button v-if="task?.status === 'pending' && !busy" type="button" class="btn btn-primary" @click="resume">{{ t('videoGen.resume') }}</button>
            <p v-if="task?.status === 'pending'" class="text-xs text-gray-500">{{ t('videoGen.pauseHint') }}</p>
          </div>
          <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
          <video v-if="videoURL" :src="videoURL" class="w-full rounded-xl bg-black" controls playsinline preload="metadata" />
          <a v-if="videoURL" class="btn btn-primary inline-flex" :href="videoURL" :download="`${task?.id || 'video'}.mp4`">{{ t('videoGen.download') }}</a>
          <button v-if="task?.status === 'done' && !videoURL && !busy" type="button" class="btn btn-secondary" @click="resume">{{ t('videoGen.loadVideo') }}</button>
          <details class="rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-700">
            <summary class="cursor-pointer font-medium">{{ t('videoGen.apiUsage') }}</summary>
            <p class="my-3 text-gray-500">{{ t('videoGen.apiHint') }}</p>
            <pre class="overflow-x-auto rounded-lg bg-gray-100 p-3 text-xs dark:bg-dark-800">{{ apiExample }}</pre>
          </details>
        </section>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { keysAPI } from '@/api/keys'
import { buildGatewayUrl } from '@/api/url'
import { createVideo, fetchVideoContent, getVideoTask, listVideoModels, normalizeVideoTask, readVideoReference, videoPollDelay, type VideoTask } from '@/api/videoGateway'
import type { ApiKey } from '@/types'

const { t } = useI18n()
const auth = useAuthStore()
const keys = ref<ApiKey[]>([])
const selectedKeyID = ref<number | ''>('')
const selectedKey = computed(() => keys.value.find(key => key.id === selectedKeyID.value))
const models = ref<string[]>([])
const model = ref('')
const modelError = ref('')
const loadingKeys = ref(false)
const loadingModels = ref(false)
const prompt = ref('')
const duration = ref(6)
const aspectRatio = ref('16:9')
const reference = ref('')
const readingReference = ref(false)
const task = ref<VideoTask | null>(null)
const videoURL = ref('')
const error = ref('')
const busy = ref(false)
const loadingContent = ref(false)
const downloadBytes = ref(0)
const downloadTotal = ref<number | undefined>()
const durations = computed(() => model.value === 'muse-video' ? [5, 6, 8, 10] : [5, 6, 8, 10, 15])
const canSubmit = computed(() => !!selectedKey.value && !!prompt.value.trim() && models.value.includes(model.value)
  && !busy.value && !readingReference.value && !loadingModels.value && task.value?.status !== 'pending')
const statusLabel = computed(() => loadingContent.value ? t('videoGen.loadingCompletedVideo') : !task.value ? t('videoGen.submitting') : task.value.status === 'done'
  ? t('videoGen.completed') : task.value.status === 'failed' ? t('videoGen.failed') : busy.value
    ? t('videoGen.processing') : t('videoGen.paused'))
const downloadProgressLabel = computed(() => t(downloadTotal.value ? 'videoGen.downloadProgress' : 'videoGen.downloadReceived', {
  received: (downloadBytes.value / 1024 / 1024).toFixed(1),
  total: ((downloadTotal.value || 0) / 1024 / 1024).toFixed(1),
}))
const apiExample = computed(() => `POST ${buildGatewayUrl('/v1/videos')}\nAuthorization: Bearer <YOUR_SUB2API_KEY>\nContent-Type: application/json\n\n${JSON.stringify({ model: model.value || 'muse-video', prompt: 'A cat walking through a garden', duration: duration.value, aspect_ratio: aspectRatio.value }, null, 2)}\n\nGET /v1/videos/<task_id>\nGET /v1/videos/<task_id>/content`)

let disposed = false
let session = 0
let keyLoad = 0
let modelLoad = 0
let referenceLoad = 0
let controller: AbortController | null = null
let modelController: AbortController | null = null
let recoveryAttempted = false

function clearVideo(): void {
  if (videoURL.value) URL.revokeObjectURL(videoURL.value)
  videoURL.value = ''
}
function pause(): void {
  session += 1
  controller?.abort()
  controller = null
  busy.value = false
  loadingContent.value = false
}
function resetTask(): void {
  pause()
  task.value = null
  error.value = ''
  clearVideo()
}
function clearReference(): void {
  referenceLoad += 1
  readingReference.value = false
  reference.value = ''
}
async function selectReference(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  clearReference()
  if (!file) return
  const token = ++referenceLoad
  readingReference.value = true
  error.value = ''
  try {
    const value = await readVideoReference(file)
    if (!disposed && token === referenceLoad) reference.value = value
  } catch {
    if (!disposed && token === referenceLoad) error.value = t('videoGen.referenceError')
  } finally {
    if (token === referenceLoad) readingReference.value = false
  }
}
async function loadKeys(): Promise<void> {
  const token = ++keyLoad
  loadingKeys.value = true
  try {
    const items: ApiKey[] = []
    let page = 1
    while (true) {
      const response = await keysAPI.list(page, 100, { status: 'active' })
      if (disposed || token !== keyLoad) return
      items.push(...(response.items || []))
      if (page >= (response.pages || 1) || !response.items?.length) break
      page += 1
    }
    keys.value = items.filter(key => key.status === 'active' && ['grok', 'composite'].includes(key.group?.platform || '')
      && (!key.expires_at || Date.parse(key.expires_at) > Date.now()) && !(key.quota > 0 && key.quota_used >= key.quota))
    if (!keys.value.some(key => key.id === selectedKeyID.value)) selectedKeyID.value = keys.value[0]?.id ?? ''
    restoreTask()
  } catch (cause) {
    if (!disposed && token === keyLoad) { keys.value = []; error.value = message(cause) }
  } finally { if (token === keyLoad) loadingKeys.value = false }
}
async function loadModels(): Promise<void> {
  const token = ++modelLoad
  modelController?.abort()
  models.value = []
  model.value = ''
  modelError.value = ''
  const key = selectedKey.value
  loadingModels.value = !!key
  if (!key) return
  const current = new AbortController()
  modelController = current
  try {
    const ids = await listVideoModels(key.key, current.signal)
    if (disposed || token !== modelLoad) return
    models.value = ids
    model.value = ids[0] || ''
  } catch (cause) {
    if (!disposed && token === modelLoad) modelError.value = message(cause)
  } finally { if (token === modelLoad) loadingModels.value = false }
}
function restoreTask(): void {
  if (recoveryAttempted) return
  recoveryAttempted = true
  const query = new URLSearchParams(window.location.search)
  if (!query.has('task_id') && !query.has('key_id')) return
  const rawKey = query.get('key_id') || ''
  const keyID = Number(rawKey)
  try {
    if (query.getAll('task_id').length !== 1 || query.getAll('key_id').length !== 1
      || !/^[1-9]\d*$/.test(rawKey) || !Number.isSafeInteger(keyID)
      || !keys.value.some(key => key.id === keyID)) throw new Error('Invalid recovery link')
    const recovered = normalizeVideoTask({ id: query.get('task_id'), status: 'pending' })
    selectedKeyID.value = keyID
    resetTask()
    task.value = recovered
    void run(false)
  } catch { error.value = t('videoGen.recoveryError') }
}
function message(cause: unknown): string {
  return cause instanceof Error ? cause.message.startsWith('videoGen.') ? t(cause.message) : cause.message : t('videoGen.requestFailed')
}
async function observe(key: string, id: string, token: number, current: AbortController): Promise<void> {
  const deadline = Date.now() + 15 * 60 * 1000
  while (!disposed && token === session && !current.signal.aborted) {
    if (task.value?.status === 'done') {
      loadingContent.value = true
      downloadBytes.value = 0
      downloadTotal.value = undefined
      const blob = await fetchVideoContent(key, id, current.signal, progress => {
        if (disposed || token !== session || current.signal.aborted) return
        downloadBytes.value = progress.received
        downloadTotal.value = progress.total
      })
      if (disposed || token !== session || current.signal.aborted) return
      clearVideo()
      videoURL.value = URL.createObjectURL(blob)
      return
    }
    if (task.value?.status === 'failed') throw new Error(task.value.error || t('videoGen.failed'))
    if (Date.now() > deadline) throw new Error(t('videoGen.timeout'))
    await videoPollDelay(current.signal)
    const next = await getVideoTask(key, id, current.signal)
    if (disposed || token !== session || current.signal.aborted) return
    task.value = next
  }
}
async function run(create: boolean): Promise<void> {
  const key = selectedKey.value
  if (!key || busy.value || (create && !canSubmit.value) || (!create && !task.value)) return
  if (create) resetTask()
  const token = ++session
  const current = new AbortController()
  controller = current
  busy.value = true
  error.value = ''
  try {
    if (create) {
      const accepted = await createVideo(key.key, {
        model: model.value, prompt: prompt.value.trim(), duration: duration.value,
        aspect_ratio: aspectRatio.value,
        ...(model.value === 'muse-video' ? {} : { resolution: '720p' }),
        ...(reference.value ? { image: { url: reference.value } } : {}),
      }, current.signal)
      if (disposed || token !== session || current.signal.aborted) return
      task.value = accepted
    }
    await observe(key.key, task.value!.id, token, current)
  } catch (cause) {
    if (!disposed && token === session && !current.signal.aborted) error.value = message(cause)
  } finally { if (token === session) { busy.value = false; loadingContent.value = false } }
}
function submit(): void { void run(true) }
function resume(): void { void run(false) }

watch(selectedKeyID, () => { resetTask(); void loadModels() }, { flush: 'sync' })
watch(model, () => { if (!durations.value.includes(duration.value)) duration.value = 6 })
watch(() => auth.isAuthenticated ? auth.user?.id : null, () => {
  keyLoad += 1
  modelLoad += 1
  modelController?.abort()
  resetTask()
  clearReference()
  prompt.value = ''
  keys.value = []
  selectedKeyID.value = ''
  if (auth.isAuthenticated) void loadKeys()
}, { flush: 'sync' })
onMounted(() => { void loadKeys() })
onUnmounted(() => {
  disposed = true
  keyLoad += 1
  modelLoad += 1
  modelController?.abort()
  resetTask()
  clearReference()
})
</script>
