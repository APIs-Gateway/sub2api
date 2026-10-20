import { readdir, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(fileURLToPath(new URL('../..', import.meta.url)))
const srcDir = path.join(root, 'src')
const baselinePath = path.join(root, 'src', 'i18n', 'hardcoded-cjk-baseline.json')
const updateBaseline = process.argv.includes('--update-baseline')
const cjkPattern = /[\u3400-\u9fff]/
const sourceExtensions = new Set(['.ts', '.tsx', '.vue', '.js'])

function toPosix(relativePath) {
  return relativePath.split(path.sep).join('/')
}

function shouldSkip(relativePath) {
  const normalized = toPosix(relativePath)
  return (
    normalized.startsWith('i18n/locales/') ||
    normalized.includes('/__tests__/') ||
    normalized.endsWith('.spec.ts') ||
    normalized.endsWith('.test.ts')
  )
}

async function listSourceFiles(dir = srcDir) {
  const entries = await readdir(dir, { withFileTypes: true })
  const files = []

  for (const entry of entries) {
    const absolute = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      files.push(...await listSourceFiles(absolute))
      continue
    }

    if (!sourceExtensions.has(path.extname(entry.name))) {
      continue
    }

    const relative = path.relative(srcDir, absolute)
    if (!shouldSkip(relative)) {
      files.push(absolute)
    }
  }

  return files
}

function normalizeLine(line) {
  return line.trim().replace(/\s+/g, ' ')
}

async function collectCjkLines() {
  const files = await listSourceFiles()
  const result = {}

  for (const file of files) {
    const content = await readFile(file, 'utf8')
    const matches = content
      .split(/\r?\n/)
      .map(normalizeLine)
      .filter((line) => cjkPattern.test(line))

    if (matches.length > 0) {
      result[toPosix(path.relative(srcDir, file))] = [...new Set(matches)].sort()
    }
  }

  return Object.fromEntries(Object.entries(result).sort(([a], [b]) => a.localeCompare(b)))
}

function findAdded(current, baseline) {
  const added = []
  for (const [file, lines] of Object.entries(current)) {
    const known = new Set(baseline[file] ?? [])
    for (const line of lines) {
      if (!known.has(line)) {
        added.push({ file, line })
      }
    }
  }
  return added
}

const current = await collectCjkLines()

if (updateBaseline) {
  await writeFile(baselinePath, `${JSON.stringify(current, null, 2)}\n`)
  console.log(`Updated ${path.relative(root, baselinePath)}`)
  process.exit(0)
}

let baseline
try {
  baseline = JSON.parse(await readFile(baselinePath, 'utf8'))
} catch {
  console.error('Missing hardcoded CJK baseline. Run: node src/i18n/check-hardcoded-cjk.mjs --update-baseline')
  process.exit(1)
}

const added = findAdded(current, baseline)
if (added.length > 0) {
  console.error('New hardcoded CJK text found outside locale files:')
  for (const item of added.slice(0, 50)) {
    console.error(`- ${item.file}: ${item.line}`)
  }
  if (added.length > 50) {
    console.error(`...and ${added.length - 50} more`)
  }
  process.exit(1)
}

console.log('No new hardcoded CJK text found.')
