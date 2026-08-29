let downHook: () => Promise<void> = async () => {}
let reconnectHook: () => Promise<void> = async () => {}

export function setCaptureHostDownHook(fn: () => Promise<void>): void {
  downHook = fn
}

export function setCaptureReconnectHook(fn: () => Promise<void>): void {
  reconnectHook = fn
}

export async function notifyCaptureHostDown(): Promise<void> {
  await downHook()
}

export async function onCaptureUnpair(): Promise<void> {
  await notifyCaptureHostDown()
}

export function dropCaptureOnReconnect(): void {
  void reconnectHook()
}
