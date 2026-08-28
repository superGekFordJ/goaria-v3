let generation = 0

export function currentDirectConnectGeneration(): number {
  return generation
}

export function bumpDirectConnectGeneration(): number {
  generation += 1
  return generation
}

export function resetDirectConnectGenerationForTests(value = 0): void {
  generation = value
}
