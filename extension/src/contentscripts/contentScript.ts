import { sendMessage } from 'webext-bridge/content-script'

// Skeleton: future plans implement pairing pair.js (reads data-secret),
// Shadow DOM popup, and download link capture.
async function pingBackground() {
  try {
    await sendMessage('ping', { type: 'ping' }, 'background')
  } catch {
    // Background may not be ready yet during scaffold testing.
  }
}

void pingBackground()
