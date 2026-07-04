import { onMessage } from 'webext-bridge/background'

// Skeleton: future plans implement interception (Firefox webRequestBlocking /
// Chrome downloads API path B), WebSocket connection, cookie capture, pairing.
onMessage('ping', () => ({ pong: true }))
