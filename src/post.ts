import * as core from '@actions/core'
import { logTiming, nowMs, run } from './lib.js'

async function post(): Promise<void> {
  const hit = core.getState('hit')
  const sign = core.getState('sign')
  if (hit !== 'false' || sign !== 'true') return

  const srcTag = core.getState('src-tag')
  if (!srcTag) {
    core.warning('post: no src-tag in state; skipping attestation')
    return
  }

  const token = core.getState('registry-password') || process.env.GITHUB_TOKEN
  if (!token) {
    core.warning('post: no token available for attestation API; skipping')
    return
  }

  const start = nowMs()
  const digestResult = await run('crane', ['digest', srcTag])
  if (digestResult.exitCode !== 0) {
    core.warning(`post: crane digest failed for ${srcTag}; image may not have been pushed. Skipping attestation. ${digestResult.stderr || digestResult.stdout}`)
    return
  }
  const digest = digestResult.stdout.trim()
  const sha = digest.startsWith('sha256:') ? digest.slice('sha256:'.length) : digest
  if (!/^[0-9a-f]{64}$/.test(sha)) {
    core.warning(`post: invalid digest from crane: ${digest}; skipping attestation`)
    return
  }

  try {
    const { attestProvenance } = await import('@actions/attest')
    const attestation = await attestProvenance({
      subjects: [{ name: srcTag, digest: { sha256: sha } }],
      token,
    })
    core.info(`attestation id: ${attestation.attestationID ?? 'unknown'}`)
    logTiming('attestation', start)
  } catch (err) {
    core.setFailed(`attestProvenance failed: ${err instanceof Error ? err.message : String(err)}`)
  }
}

post().catch((err) => {
  core.setFailed(err instanceof Error ? err.message : String(err))
})
