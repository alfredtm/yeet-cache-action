import { execFileSync } from 'node:child_process'
import { cpSync, rmSync, mkdirSync, existsSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const root = new URL('.', import.meta.url).pathname
const ncc = join(root, 'node_modules/.bin/ncc')

const tmpMain = join(root, '.ncc-main')
const tmpPost = join(root, '.ncc-post')
const dist = join(root, 'dist')

for (const dir of [tmpMain, tmpPost]) {
  if (existsSync(dir)) rmSync(dir, { recursive: true, force: true })
}
if (!existsSync(dist)) mkdirSync(dist, { recursive: true })

const run = (args) => execFileSync(ncc, args, { stdio: 'inherit', cwd: root })

run(['build', 'src/main.ts', '-o', tmpMain, '--license', 'licenses.txt'])
run(['build', 'src/post.ts', '-o', tmpPost])

for (const f of readdirSync(dist)) {
  if (f === 'yeet-pack-linux-amd64') continue
  rmSync(join(dist, f), { recursive: true, force: true })
}

cpSync(join(tmpMain, 'index.js'), join(dist, 'main.js'))
cpSync(join(tmpPost, 'index.js'), join(dist, 'post.js'))
const licenseSrc = join(tmpMain, 'licenses.txt')
if (existsSync(licenseSrc)) cpSync(licenseSrc, join(dist, 'licenses.txt'))

for (const file of readdirSync(tmpPost)) {
  if (file === 'index.js' || file === 'licenses.txt') continue
  cpSync(join(tmpPost, file), join(dist, file))
}
for (const file of readdirSync(tmpMain)) {
  if (file === 'index.js' || file === 'licenses.txt') continue
  cpSync(join(tmpMain, file), join(dist, file))
}

rmSync(tmpMain, { recursive: true, force: true })
rmSync(tmpPost, { recursive: true, force: true })
