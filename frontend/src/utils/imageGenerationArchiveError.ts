import { extractApiErrorCode } from './apiError'

const archiveErrorKeys: Record<string, string> = {
  IMAGE_GENERATION_HISTORY_UPLOAD_INCOMPLETE: 'imageGen.history.archiveUploadIncomplete',
  IMAGE_GENERATION_HISTORY_UPLOAD_INVALID: 'imageGen.history.archiveUploadInvalid',
  IMAGE_GENERATION_HISTORY_TOO_LARGE: 'imageGen.history.archiveTooLarge',
  IMAGE_GENERATION_HISTORY_KEY_INVALID: 'imageGen.history.archiveKeyInvalid',
  IMAGE_GENERATION_HISTORY_INVALID: 'imageGen.history.archiveInvalid',
  IMAGE_GENERATION_HISTORY_FORBIDDEN: 'imageGen.history.archiveForbidden',
  ECONNABORTED: 'imageGen.history.archiveTimeout',
  ETIMEDOUT: 'imageGen.history.archiveTimeout',
  ERR_NETWORK: 'imageGen.history.archiveNetwork',
}

/** Only known reasons and numeric HTTP status are safe to display; server messages may contain internal details. */
export function imageGenerationArchiveErrorMessage(
  error: unknown,
  t: (key: string, params?: Record<string, unknown>) => string,
): string {
  const code = extractApiErrorCode(error)
  const response = error as { status?: unknown; response?: { status?: unknown } } | null | undefined
  const rawStatus = response?.status ?? response?.response?.status
  const status = typeof rawStatus === 'number' && Number.isInteger(rawStatus) ? rawStatus : undefined
  let key = code && Object.prototype.hasOwnProperty.call(archiveErrorKeys, code) ? archiveErrorKeys[code] : undefined
  if (!key) {
    if (status === 0) key = 'imageGen.history.archiveNetwork'
    else if (status === 408 || status === 504) key = 'imageGen.history.archiveTimeout'
    else if (status === 413) key = 'imageGen.history.archiveTooLarge'
    else if (status === 403) key = 'imageGen.history.archiveForbidden'
    else if (status === 401) key = 'imageGen.history.archiveUnauthorized'
  }
  const detail = key
    ? t(key)
    : status !== undefined && status >= 100 && status <= 599
      ? t('imageGen.history.archiveFailedStatus', { status })
      : t('imageGen.history.archiveFailed')
  return `${detail} ${t('imageGen.history.archiveRetry')}`
}
