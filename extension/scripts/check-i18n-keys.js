import { readdirSync, readFileSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve, join, dirname } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const localesDir = resolve(__dirname, '..', 'public', '_locales')

const errors = []
const locales = []

for (const entry of readdirSync(localesDir)) {
  const full = join(localesDir, entry)
  if (!statSync(full).isDirectory()) continue
  const msgFile = join(full, 'messages.json')
  let keys
  try {
    keys = Object.keys(JSON.parse(readFileSync(msgFile, 'utf8')))
  } catch {
    errors.push(`${entry}: error: cannot parse messages.json`)
    continue
  }
  locales.push({ locale: entry, keys: new Set(keys) })
}

// Use the first locale (sorted) as the reference key set.
locales.sort((a, b) => a.locale.localeCompare(b.locale))
const reference = locales[0]

for (const loc of locales) {
  if (loc === reference) continue
  const missing = [...reference.keys].filter((k) => !loc.keys.has(k))
  const extra = [...loc.keys].filter((k) => !reference.keys.has(k))
  for (const k of missing) {
    errors.push(`${loc.locale}: error: missing key "${k}" (present in ${reference.locale})`)
  }
  for (const k of extra) {
    errors.push(`${loc.locale}: error: extra key "${k}" (not in ${reference.locale})`)
  }
}

for (const e of errors) console.log(e)

console.log(`\n${errors.length} key consistency error(s) across ${locales.length} locales`)
if (errors.length > 0) process.exit(1)
