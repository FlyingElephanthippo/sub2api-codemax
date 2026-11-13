<template>
  <div>
    <label class="input-label" :for="id">{{ t('admin.accounts.mediaProvider.title') }}</label>
    <select :id="id" class="input" :value="modelValue" data-testid="media-provider" @change="onChange">
      <option value="grok">{{ t('admin.accounts.mediaProvider.grok') }}</option>
      <option value="muse">{{ t('admin.accounts.mediaProvider.muse') }}</option>
    </select>
    <p v-if="modelValue === 'muse'" class="input-hint">{{ t('admin.accounts.mediaProvider.hint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

withDefaults(defineProps<{ modelValue: 'grok' | 'muse'; id?: string }>(), { id: 'media-provider' })
const emit = defineEmits<{ 'update:modelValue': [value: 'grok' | 'muse'] }>()
const { t } = useI18n()

function onChange(event: Event) {
  emit('update:modelValue', (event.target as HTMLSelectElement).value === 'muse' ? 'muse' : 'grok')
}
</script>
