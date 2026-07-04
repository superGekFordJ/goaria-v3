import { sendMessage } from 'webext-bridge/content-script'

// Skeleton: future plans implement pairing pair.js (reads data-secret),
// Shadow DOM popup, and download link capture.
// pingBackground is a channel-verification helper kept for future use;
// not auto-fired on load to avoid injecting pings into every tab.
export async function pingBackground() {
  try {
    await sendMessage('ping', { type: 'ping' }, 'background')
  } catch {
    // Background may not be ready yet during scaffold testing.
  }
}
