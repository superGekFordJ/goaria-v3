import { mintRequestId } from './mintRequestId'
import type { ExtractorSessionRecord } from './extractorSessionStore'

export function canReuseBatch(
  session: ExtractorSessionRecord | null,
  mintId: () => string = mintRequestId,
): {
  sessionId: string
  itemIds: string[]
  requestId: string
  markRetry: boolean
} | null {
  if (!session?.sessionId || !session.itemIds || session.itemIds.length < 1) return null
  const err = session.errorCode
  if (err === 'auth_expired' || err === 'session_expired' || err === 'pack_error') return null
  if (err === 'idempotency_conflict') {
    return {
      sessionId: session.sessionId,
      itemIds: session.itemIds,
      requestId: mintId(),
      markRetry: false,
    }
  }
  if (session.batchRequestId && (session.state === 'committing' || err === 'timeout' || err === 'busy')) {
    return {
      sessionId: session.sessionId,
      itemIds: session.itemIds,
      requestId: session.batchRequestId,
      markRetry: false,
    }
  }
  if (session.batchRequestId && (err === 'unavailable' || err === 'invalid_request') && !session.batchRetryUsed) {
    return {
      sessionId: session.sessionId,
      itemIds: session.itemIds,
      requestId: session.batchRequestId,
      markRetry: true,
    }
  }
  if (!session.batchRequestId && session.state === 'error' && session.itemIds.length >= 1) {
    return {
      sessionId: session.sessionId,
      itemIds: session.itemIds,
      requestId: mintId(),
      markRetry: false,
    }
  }
  return null
}
