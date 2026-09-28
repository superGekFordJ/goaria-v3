import { isInsufficientDiskSpaceFailure } from './diskSpaceError'

export type TaskErrorExplainKey =
  | 'timeout'
  | 'notFound'
  | 'tooSlow'
  | 'network'
  | 'noResume'
  | 'diskFull'
  | 'fileExists'
  | 'writeFailed'
  | 'dnsFailed'
  | 'badResponse'
  | 'tooManyRedirects'
  | 'authFailed'
  | 'serverBusy'
  | 'unknown'

// aria2c EXIT STATUS codes (HTTP/FTP relevant subset; BT codes unmapped).
const ARIA2_EXIT_CODE_KEYS: Readonly<Record<string, TaskErrorExplainKey>> = {
  '2': 'timeout',
  '3': 'notFound',
  '4': 'notFound',
  '5': 'tooSlow',
  '6': 'network',
  '8': 'noResume',
  '9': 'diskFull',
  '13': 'fileExists',
  '16': 'writeFailed',
  '17': 'writeFailed',
  '18': 'writeFailed',
  '19': 'dnsFailed',
  '22': 'badResponse',
  '23': 'tooManyRedirects',
  '24': 'authFailed',
  '29': 'serverBusy',
}

export function explainTaskErrorCode(
  code?: string | null,
  message?: string | null,
): TaskErrorExplainKey {
  if (isInsufficientDiskSpaceFailure(code, message)) return 'diskFull'
  const normalized = (code ?? '').trim()
  return ARIA2_EXIT_CODE_KEYS[normalized] ?? 'unknown'
}

/** i18n key for the plain-language error line. */
export function explainTaskErrorKey(code?: string | null, message?: string | null): string {
  return `taskDetail.errors.${explainTaskErrorCode(code, message)}`
}
