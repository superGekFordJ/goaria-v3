import { describe, expect, it } from 'vitest'
import { applyCapsuleEvent, INITIAL_CAPSULE_STATE } from './capsuleUiState'

const TOKEN_A = 'a'.repeat(64)
const TOKEN_B = 'b'.repeat(64)

describe('applyCapsuleEvent', () => {
  it('ignores detect when the caller marks the page ignored', () => {
    const next = applyCapsuleEvent(INITIAL_CAPSULE_STATE, {
      type: 'detect',
      generation: 1,
      pageToken: TOKEN_A,
      ignored: true,
    })
    expect(next).toEqual(INITIAL_CAPSULE_STATE)
  })

  it('paints idle on detect and keeps idle or ready when already visible', () => {
    const idle = applyCapsuleEvent(INITIAL_CAPSULE_STATE, {
      type: 'detect',
      generation: 1,
      pageToken: TOKEN_A,
    })
    expect(idle.ui).toBe('idle')
    expect(idle.pageToken).toBe(TOKEN_A)
    const again = applyCapsuleEvent(idle, { type: 'detect', generation: 2, pageToken: TOKEN_A })
    expect(again.ui).toBe('idle')
    expect(again.generation).toBe(2)
    const ready = applyCapsuleEvent(idle, {
      type: 'result',
      pageToken: TOKEN_A,
      ui: 'ready',
      count: 3,
    })
    const stillReady = applyCapsuleEvent(ready, {
      type: 'detect',
      generation: 3,
      pageToken: TOKEN_A,
    })
    expect(stillReady.ui).toBe('ready')
    expect(stillReady.count).toBe(3)
  })

  it('does not change state on a mismatched hide or result token', () => {
    const idle = applyCapsuleEvent(INITIAL_CAPSULE_STATE, {
      type: 'detect',
      generation: 1,
      pageToken: TOKEN_A,
    })
    expect(applyCapsuleEvent(idle, { type: 'hide', pageToken: TOKEN_B, reason: 'nav' })).toEqual(idle)
    expect(
      applyCapsuleEvent(idle, { type: 'result', pageToken: TOKEN_B, ui: 'success' }),
    ).toEqual(idle)
  })

  it('force-hides on generation hide without a token', () => {
    const idle = applyCapsuleEvent(INITIAL_CAPSULE_STATE, {
      type: 'detect',
      generation: 1,
      pageToken: TOKEN_A,
    })
    const hidden = applyCapsuleEvent(idle, { type: 'hide', reason: 'generation' })
    expect(hidden.ui).toBe('hidden')
    expect(hidden.pageToken).toBe('')
  })

  it('keeps ready on clickAccepted so extractor:click cannot start a multi-file batch', () => {
    const ready = applyCapsuleEvent(
      { ...INITIAL_CAPSULE_STATE, ui: 'ready', pageToken: TOKEN_A, count: 4 },
      { type: 'clickAccepted' },
    )
    expect(ready.ui).toBe('ready')
  })

  it('moves idle to resolving on clickAccepted', () => {
    const idle = applyCapsuleEvent(INITIAL_CAPSULE_STATE, {
      type: 'detect',
      generation: 1,
      pageToken: TOKEN_A,
    })
    const resolving = applyCapsuleEvent(idle, { type: 'clickAccepted' })
    expect(resolving.ui).toBe('resolving')
  })

  it('caps result filenames and strips control characters', () => {
    const idle = applyCapsuleEvent(INITIAL_CAPSULE_STATE, {
      type: 'detect',
      generation: 1,
      pageToken: TOKEN_A,
    })
    const next = applyCapsuleEvent(idle, {
      type: 'result',
      pageToken: TOKEN_A,
      ui: 'success',
      filename: 'a\r\n' + 'x'.repeat(250),
    })
    expect(next.filename.includes('\n')).toBe(false)
    expect(next.filename.length).toBe(200)
  })

  it('maps watchdog from resolving and committing to retryable timeout', () => {
    const resolving = applyCapsuleEvent(
      { ...INITIAL_CAPSULE_STATE, ui: 'resolving', pageToken: TOKEN_A },
      { type: 'watchdog' },
    )
    expect(resolving).toMatchObject({ ui: 'error', errorCode: 'timeout' })
    const committing = applyCapsuleEvent(
      { ...INITIAL_CAPSULE_STATE, ui: 'committing', pageToken: TOKEN_A },
      { type: 'watchdog' },
    )
    expect(committing).toMatchObject({ ui: 'error', errorCode: 'timeout' })
  })

  it('keeps success until hide or a new detect token', () => {
    const success = applyCapsuleEvent(
      { ...INITIAL_CAPSULE_STATE, ui: 'success', pageToken: TOKEN_A },
      { type: 'result', pageToken: TOKEN_A, ui: 'error', error_code: 'busy' },
    )
    expect(success.ui).toBe('success')
    const hidden = applyCapsuleEvent(success, { type: 'hide', pageToken: TOKEN_A })
    expect(hidden.ui).toBe('hidden')
    const again = applyCapsuleEvent(success, {
      type: 'detect',
      generation: 9,
      pageToken: TOKEN_B,
    })
    expect(again.ui).toBe('idle')
    expect(again.pageToken).toBe(TOKEN_B)
  })
})
