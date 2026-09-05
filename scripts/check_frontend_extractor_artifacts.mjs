#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..');
const distDir = path.join(repoRoot, 'frontend', 'dist');

const targetVariant = process.argv[2] || process.env.EXTRACTOR_VARIANT || 'generic-no-pack';

const isTagged = targetVariant === 'full-pack' || targetVariant === 'tagged';
const isGeneric = targetVariant === '' || targetVariant === 'generic-no-pack' || targetVariant === 'generic';

if (!isTagged && !isGeneric) {
  console.error(`Unknown variant specified for artifact check: "${targetVariant}". Expected "generic-no-pack" or "full-pack".`);
  process.exit(1);
}

const manifestPathVite = path.join(distDir, '.vite', 'manifest.json');
const manifestPathLegacy = path.join(distDir, 'manifest.json');

let manifestPath = null;
if (fs.existsSync(manifestPathVite)) {
  manifestPath = manifestPathVite;
} else if (fs.existsSync(manifestPathLegacy)) {
  manifestPath = manifestPathLegacy;
}

if (!manifestPath) {
  console.error(`Artifact check failed: manifest not found at "${manifestPathVite}" or "${manifestPathLegacy}".`);
  console.error('Make sure "build.manifest: true" is enabled in frontend/vite.config.js and frontend has been built.');
  process.exit(1);
}

let manifest;
try {
  const content = fs.readFileSync(manifestPath, 'utf8');
  manifest = JSON.parse(content);
} catch (err) {
  console.error(`Failed to read or parse manifest at "${manifestPath}":`, err.message);
  process.exit(1);
}

const matchingEntries = [];
for (const [key, entry] of Object.entries(manifest)) {
  const matches = (str) => typeof str === 'string' && (str.includes('features/extractor') || str.includes('ExtractorSection'));
  if (
    matches(key) ||
    matches(entry.file) ||
    matches(entry.src) ||
    matches(entry.name) ||
    (Array.isArray(entry.dynamicImports) && entry.dynamicImports.some(matches)) ||
    (Array.isArray(entry.imports) && entry.imports.some(matches))
  ) {
    matchingEntries.push({ key, file: entry.file, src: entry.src });
  }
}

if (isGeneric) {
  if (matchingEntries.length > 0) {
    console.error('Generic frontend artifact closure VIOLATED! Found extractor module(s) in manifest:');
    for (const m of matchingEntries) {
      console.error(`  - key: ${m.key}, file: ${m.file}, src: ${m.src}`);
    }
    process.exit(1);
  }
  console.log(`Generic frontend artifact closure verified: no extractor modules present in manifest (${manifestPath}).`);
  process.exit(0);
}

if (isTagged) {
  if (matchingEntries.length === 0) {
    console.error('Tagged frontend artifact closure VIOLATED! Expected extractor module in manifest, but none found.');
    process.exit(1);
  }
  console.log(`Tagged frontend artifact closure verified: found extractor module(s) in manifest:`);
  for (const m of matchingEntries) {
    console.log(`  - key: ${m.key}, file: ${m.file}, src: ${m.src}`);
  }
  process.exit(0);
}
