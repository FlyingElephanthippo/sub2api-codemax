import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/imageGen'
import zh from '@/i18n/locales/zh/imageGen'
import { imageGenerationArchiveErrorMessage } from '../imageGenerationArchiveError'

describe.each(['zh', 'en'] as const)('safe image archive errors in %s', locale => {
  const messages = locale === 'zh' ? zh.imageGen.history : en.imageGen.history
  const translate = (key: string, params?: Record<string, unknown>) => {
    const message = messages[key.replace('imageGen.history.', '') as keyof typeof messages]
    if (!message) throw new Error(`Missing ${locale} message: ${key}`)
    return message.replace('{status}', String(params?.status ?? ''))
  }

  it.each([
    ['IMAGE_GENERATION_HISTORY_UPLOAD_INCOMPLETE', 'archiveUploadIncomplete'],
    ['IMAGE_GENERATION_HISTORY_UPLOAD_INVALID', 'archiveUploadInvalid'],
    ['IMAGE_GENERATION_HISTORY_TOO_LARGE', 'archiveTooLarge'],
    ['IMAGE_GENERATION_HISTORY_KEY_INVALID', 'archiveKeyInvalid'],
    ['IMAGE_GENERATION_HISTORY_INVALID', 'archiveInvalid'],
    ['IMAGE_GENERATION_HISTORY_FORBIDDEN', 'archiveForbidden'],
    ['ECONNABORTED', 'archiveTimeout'], ['ETIMEDOUT', 'archiveTimeout'],
    ['ERR_NETWORK', 'archiveNetwork'],
  ] as const)('maps known code %s without exposing backend messages', (code, key) => {
    const message = imageGenerationArchiveErrorMessage({ status: 400, reason: code, code: 400,
      message: 'private SQL error Authorization: Bearer secret' }, translate)

    expect(message).toBe(`${messages[key]} ${messages.archiveRetry}`)
    expect(message).not.toContain('private')
    expect(message).not.toContain('secret')
  })

  it.each([
    [0, 'archiveNetwork'], [408, 'archiveTimeout'], [504, 'archiveTimeout'],
    [413, 'archiveTooLarge'], [403, 'archiveForbidden'], [401, 'archiveUnauthorized'],
  ] as const)('provides a safe fallback for status %i', (status, key) => {
    expect(imageGenerationArchiveErrorMessage({ status, message: 'hidden transport detail' }, translate))
      .toBe(`${messages[key]} ${messages.archiveRetry}`)
  })

  it('supports raw Axios transport errors as well as normalized response errors', () => {
    expect(imageGenerationArchiveErrorMessage({ code: 'ECONNABORTED', message: 'private detail' }, translate))
      .toBe(`${messages.archiveTimeout} ${messages.archiveRetry}`)
  })

  it.each([undefined, null, 'unsafe server message', new Error('unsafe SQL error'),
    { code: 'constructor', message: 'secret' }, { reason: 'Bearer secret', status: 'secret' }])(
    'does not display unknown error data: %j', error => {
      expect(imageGenerationArchiveErrorMessage(error, translate)).toBe(`${messages.archiveFailed} ${messages.archiveRetry}`)
    },
  )

  it('shows only a numeric HTTP status for unrecognized server errors', () => {
    const result = imageGenerationArchiveErrorMessage({ response: { status: 500 }, reason: 'SQL_ERROR',
      message: 'database_password=secret' }, translate)

    expect(result).toBe(`${translate('imageGen.history.archiveFailedStatus', { status: 500 })} ${messages.archiveRetry}`)
    expect(result).not.toContain('SQL')
    expect(result).not.toContain('secret')
  })
})
