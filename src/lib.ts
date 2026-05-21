import * as exec from '@actions/exec'
import * as core from '@actions/core'
import * as fs from 'node:fs'
import * as os from 'node:os'
import * as path from 'node:path'

export interface ExecResult {
  stdout: string
  stderr: string
  exitCode: number
}

export async function run(command: string, args: string[] = [], opts: exec.ExecOptions = {}): Promise<ExecResult> {
  let stdout = ''
  let stderr = ''
  const exitCode = await exec.exec(command, args, {
    ...opts,
    ignoreReturnCode: true,
    silent: opts.silent ?? false,
    listeners: {
      stdout: (data) => { stdout += data.toString() },
      stderr: (data) => { stderr += data.toString() },
      ...(opts.listeners ?? {}),
    },
  })
  return { stdout, stderr, exitCode }
}

export async function which(tool: string): Promise<string | null> {
  try {
    const result = await run('bash', ['-c', `command -v ${tool}`], { silent: true })
    if (result.exitCode === 0) return result.stdout.trim()
  } catch {
    // fall through
  }
  return null
}

export function getYeetPackPath(override: string): string {
  if (override) return override
  const bundled = path.join(__dirname, 'yeet-pack-linux-amd64')
  if (fs.existsSync(bundled)) return bundled
  if (os.platform() !== 'linux' || os.arch() !== 'x64') {
    throw new Error(`bundled yeet-pack binary not found at ${bundled} and runner is not linux/amd64`)
  }
  throw new Error(`yeet-pack binary missing from action dist: ${bundled}`)
}

export async function exposeYeetPackOnPath(bundled: string): Promise<string> {
  const dest = '/usr/local/bin/yeet-pack'
  if (fs.existsSync(dest)) return dest
  try {
    fs.copyFileSync(bundled, dest)
    fs.chmodSync(dest, 0o755)
  } catch {
    await exec.exec('sudo', ['cp', bundled, dest])
    await exec.exec('sudo', ['chmod', '+x', dest])
  }
  return dest
}

export function ownerFromImage(image: string): string {
  const parts = image.split('/')
  if (parts.length < 2) throw new Error(`cannot derive owner from image: ${image}`)
  return parts[1]
}

export function defaultVerifyIdentity(): string {
  const repo = process.env.GITHUB_REPOSITORY
  if (!repo) throw new Error('GITHUB_REPOSITORY not set; pass verify-identity explicitly')
  return `^https://github.com/${repo}/.github/workflows/.+$`
}

export function nowMs(): number {
  return Number(process.hrtime.bigint() / 1_000_000n)
}

export function logTiming(label: string, startMs: number): void {
  core.info(`${label} took ${nowMs() - startMs}ms`)
}
