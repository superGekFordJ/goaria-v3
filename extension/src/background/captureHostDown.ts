let resumeHook: () => Promise<void> = async () => {}

export function setCaptureHostDownHook(fn: () => Promise<void>): void {
  resumeHook = fn
}

export async function notifyCaptureHostDown(): Promise<void> {
  await resumeHook()
}

export async function onCaptureUnpair(): Promise<void> {
  await notifyCaptureHostDown()
}

export function dropCaptureOnReconnect(): void {
  void notifyCaptureHostDown()
}
