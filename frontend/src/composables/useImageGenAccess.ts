import { computed, ref } from 'vue'
import { keysAPI } from '@/api/keys'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

/**
 * 生图入口是否可见：至少有一把可用于生图的 Key。
 * 只要分组开了 allow_image_generation 就算可用，具体模型是否支持交给页面判断。
 */

const loaded = ref(false)
const loading = ref(false)
const hasUsableKey = ref(false)
let pendingLoad: Promise<boolean> | null = null
const pageSize = 100

function keyAllowsImageGeneration(key: ApiKey): boolean {
  return key.status === 'active' && key.group?.allow_image_generation === true
}

async function loadImageGenAccess(force = false): Promise<boolean> {
  const authStore = useAuthStore()
  if (!authStore.isAuthenticated) {
    loaded.value = true
    hasUsableKey.value = false
    return false
  }

  if (loaded.value && !force) return hasUsableKey.value
  if (pendingLoad && !force) return pendingLoad

  loading.value = true
  pendingLoad = (async () => {
    let page = 1
    while (true) {
      const response = await keysAPI.list(page, pageSize, {
        status: 'active',
        sort_by: 'created_at',
        sort_order: 'desc',
      })

      if ((response.items || []).some(keyAllowsImageGeneration)) {
        hasUsableKey.value = true
        loaded.value = true
        return true
      }

      if (page >= response.pages || (response.items || []).length === 0) {
        hasUsableKey.value = false
        loaded.value = true
        return false
      }

      page += 1
    }
  })()
    .catch(() => {
      hasUsableKey.value = false
      loaded.value = false
      return false
    })
    .finally(() => {
      loading.value = false
      pendingLoad = null
    })

  return pendingLoad
}

export function useImageGenAccess() {
  return {
    canUseImageGen: computed(() => hasUsableKey.value),
    imageGenAccessLoaded: computed(() => loaded.value),
    imageGenAccessLoading: computed(() => loading.value),
    refreshImageGenAccess: loadImageGenAccess,
  }
}

/**
 * 页面自己选 Key 时用这个：只留下「可用」的 Key，跟导航项的判断保持一致。
 */
export function selectableImageKeys(keys: ApiKey[]): ApiKey[] {
  return keys.filter(
    key =>
      key.status === 'active' &&
      (!key.expires_at || Date.parse(key.expires_at) > Date.now()) &&
      !(key.quota > 0 && key.quota_used >= key.quota)
  )
}
