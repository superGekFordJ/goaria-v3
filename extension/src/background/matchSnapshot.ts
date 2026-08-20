import type { MatchDigestSnapshot } from './digestMatch'
import { isValidMatchSnapshot } from './digestMatch'

let snapshot: MatchDigestSnapshot | undefined
let generation = 0

function cloneSnapshot(snap: MatchDigestSnapshot): MatchDigestSnapshot {
  return {
    digest_version: snap.digest_version,
    salt: snap.salt,
    exact_digests: [...snap.exact_digests],
    subdomain_digests: [...snap.subdomain_digests],
  }
}

export function applyMatchSnapshot(snap: MatchDigestSnapshot): number {
  snapshot = cloneSnapshot(snap)
  generation += 1
  return generation
}

export function clearMatchSnapshot(): number {
  snapshot = undefined
  generation += 1
  return generation
}

export function getMatchSnapshot(): MatchDigestSnapshot | undefined {
  if (!snapshot) return undefined
  return cloneSnapshot(snapshot)
}

export function getMatchGeneration(): number {
  return generation
}

export function isMatchGenerationCurrent(captured: number): boolean {
  return generation === captured
}

export function applyParsedMatch(snap: MatchDigestSnapshot | undefined): number {
  if (!isValidMatchSnapshot(snap)) {
    return clearMatchSnapshot()
  }
  return applyMatchSnapshot(snap)
}
