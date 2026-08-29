let bootReady = false

export function isBootReady(): boolean {
  return bootReady
}

export function setBootReady(ready: boolean): void {
  bootReady = ready
}

export function resetBootReadyForTests(): void {
  bootReady = false
}
