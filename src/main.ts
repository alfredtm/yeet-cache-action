import * as core from '@actions/core'
import * as crypto from 'node:crypto'
import {
  defaultVerifyIdentity,
  exposeYeetPackOnPath,
  getYeetPackPath,
  logTiming,
  nowMs,
  ownerFromImage,
  run,
} from './lib.js'

async function computeHashViaApi(paths: string[], extra: string, token: string): Promise<string> {
  const repo = process.env.GITHUB_REPOSITORY
  const sha = process.env.GITHUB_SHA
  if (!repo || !sha) throw new Error('GITHUB_REPOSITORY / GITHUB_SHA not set')

  const env = { ...process.env, GH_TOKEN: token }
  const commitRes = await run('gh', ['api', `repos/${repo}/git/commits/${sha}`, '-q', '.tree.sha'], { env })
  if (commitRes.exitCode !== 0) throw new Error(`gh api git/commits failed: ${commitRes.stderr}`)
  const treeSha = commitRes.stdout.trim()

  const treeRes = await run('gh', ['api', `repos/${repo}/git/trees/${treeSha}`], { env })
  if (treeRes.exitCode !== 0) throw new Error(`gh api git/trees failed: ${treeRes.stderr}`)
  const tree = JSON.parse(treeRes.stdout) as { tree: Array<{ path: string; sha: string }> }

  const shas: string[] = []
  for (const p of paths) {
    const entry = tree.tree.find((e) => e.path === p)
    if (!entry) throw new Error(`path not found in tree: ${p}`)
    shas.push(entry.sha)
  }

  const input = shas.map((s) => s + '\n').join('') + `extra:${extra}\n`
  return crypto.createHash('sha256').update(input).digest('hex').slice(0, 12)
}

async function main(): Promise<void> {
  const paths = core.getInput('paths')
  const hashInput = core.getInput('hash')
  const image = core.getInput('image', { required: true })
  const extra = core.getInput('extra')
  const registry = core.getInput('registry') || 'ghcr.io'
  const registryUsername = core.getInput('registry-username') || process.env.GITHUB_ACTOR || ''
  const registryPassword = core.getInput('registry-password', { required: true })
  const tags = core.getInput('tags')
  const sign = core.getInput('sign') === 'true'
  const verifyOnHit = (core.getInput('verify-on-hit') || 'true') === 'true'
  const verifyIdentity = core.getInput('verify-identity') || defaultVerifyIdentity()
  const viaApi = core.getInput('via-api') === 'true'
  const yeetPackOverride = core.getInput('yeet-pack-binary-path')

  if (!hashInput && !paths) {
    throw new Error("either 'paths' or 'hash' input must be provided")
  }

  const bundledYeetPack = getYeetPackPath(yeetPackOverride)
  const yeetPack = await exposeYeetPackOnPath(bundledYeetPack)

  let hash: string
  if (hashInput) {
    hash = hashInput
  } else {
    const pathsList = paths.trim().split(/\s+/).filter(Boolean)
    if (viaApi) {
      hash = await computeHashViaApi(pathsList, extra, registryPassword)
    } else {
      const result = await run(yeetPack, ['hash', '--paths', pathsList.join(','), '--extra', extra])
      if (result.exitCode !== 0) {
        throw new Error(`yeet-pack hash failed: ${result.stderr || result.stdout}`)
      }
      hash = result.stdout.trim()
    }
  }

  if (!/^[0-9a-f]{12}$/.test(hash)) {
    throw new Error(`invalid hash (expected 12 hex chars): ${hash}`)
  }

  const srcTag = `${image}:src-${hash}`
  core.setOutput('src-hash', hash)
  core.setOutput('src-tag', srcTag)
  core.notice(`source hash = ${hash}`)

  const loginStart = nowMs()
  const loginRes = await run(yeetPack, [
    'login',
    '--registry', registry,
    '--username', registryUsername,
    '--password-stdin',
  ], { input: Buffer.from(registryPassword), silent: true })
  if (loginRes.exitCode !== 0) {
    throw new Error(`yeet-pack login failed: ${loginRes.stderr || loginRes.stdout}`)
  }
  logTiming('registry login', loginStart)

  const checkStart = nowMs()
  const check = await run(yeetPack, ['check', '--image', srcTag])
  logTiming('cache check', checkStart)

  if (check.exitCode === 0) {
    core.notice(`CACHE HIT — ${srcTag}`)

    if (sign && verifyOnHit) {
      const verifyStart = nowMs()
      const owner = ownerFromImage(image)
      const verify = await run('gh', [
        'attestation', 'verify', `oci://${srcTag}`,
        '--owner', owner,
        '--predicate-type', 'https://slsa.dev/provenance/v1',
        '--cert-identity-regex', verifyIdentity,
      ], { env: { ...process.env, GH_TOKEN: registryPassword } })
      if (verify.exitCode !== 0) {
        core.error(`cache image ${srcTag} failed GitHub attestation verification.`)
        core.error('Cache may have been tampered with, built before signing was enabled,')
        core.error('or built by an unexpected workflow identity. Refusing to retag.')
        core.error(verify.stderr || verify.stdout)
        throw new Error('attestation verification failed')
      }
      logTiming('attestation verify', verifyStart)
    } else if (sign) {
      core.info('verify-on-hit disabled — skipping attestation verification (trust-on-first-use)')
    }

    if (tags) {
      const tagStart = nowMs()
      const tagList = tags.split(',').map((t) => t.trim()).filter(Boolean)
      const results = await Promise.all(
        tagList.map((t) => run(yeetPack, ['tag', '--src', srcTag, '--tags', t]))
      )
      for (const r of results) {
        if (r.exitCode !== 0) {
          throw new Error(`yeet-pack tag failed: ${r.stderr || r.stdout}`)
        }
      }
      logTiming('tag promotion', tagStart)
    }

    core.setOutput('hit', 'true')
    core.setOutput('cached-tag', srcTag)
    core.saveState('hit', 'true')
  } else {
    core.notice(`cache miss — ${srcTag} not in registry`)
    core.setOutput('hit', 'false')
    core.saveState('hit', 'false')
    core.saveState('src-tag', srcTag)
    core.saveState('sign', sign ? 'true' : 'false')
    core.saveState('registry-password', registryPassword)
  }
}

main().catch((err) => {
  core.setFailed(err instanceof Error ? err.message : String(err))
})
