import { describe, expect, it } from 'vitest'
import { explainTaskErrorCode, explainTaskErrorKey } from './taskErrorExplain'

describe('explainTaskErrorCode', () => {
  it.each([
    ['2', 'timeout'],
    ['3', 'notFound'],
    ['4', 'notFound'],
    ['5', 'tooSlow'],
    ['6', 'network'],
    ['8', 'noResume'],
    ['9', 'diskFull'],
    ['13', 'fileExists'],
    ['16', 'writeFailed'],
    ['17', 'writeFailed'],
    ['18', 'writeFailed'],
    ['19', 'dnsFailed'],
    ['22', 'badResponse'],
    ['23', 'tooManyRedirects'],
    ['24', 'authFailed'],
    ['29', 'serverBusy'],
  ])('maps aria2 exit code %s to %s', (code, key) => {
    expect(explainTaskErrorCode(code, '')).toBe(key)
  })

  it.each([['1'], ['7'], ['12'], ['26'], [''], ['999']])('falls back to unknown for %s', code => {
    expect(explainTaskErrorCode(code, 'whatever')).toBe('unknown')
  })

  it('recognises the Surge disk-space sentinel without a code', () => {
    expect(explainTaskErrorCode('1', 'write: insufficient disk space')).toBe('diskFull')
    expect(explainTaskErrorCode(undefined, null)).toBe('unknown')
  })

  it('builds the i18n key', () => {
    expect(explainTaskErrorKey('24', '')).toBe('taskDetail.errors.authFailed')
    expect(explainTaskErrorKey(null, null)).toBe('taskDetail.errors.unknown')
  })
})
