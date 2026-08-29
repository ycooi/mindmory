/**
 * Mindmory conversation relay for DeepSeek Harness.
 *
 * This host-plane Cordis plugin observes Harness's canonical session event
 * stream. It forwards exact direct-user messages and assembled assistant
 * messages to the release's credential-hiding checkpoint adapter. Synthetic
 * user injections and empty/tool-call-only assistant messages are ignored.
 */
import { spawn } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const name = 'mindmory-checkpoint-relay'
export const inject = ['sessions']

const defaultCommand = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../integrations/checkpoint-hook.sh',
)

function textContent(message) {
  if (!message || !Array.isArray(message.content)) return ''
  return message.content
    .filter(block => block && block.type === 'text' && typeof block.text === 'string')
    .map(block => block.text)
    .join('\n')
}

/** Convert one relevant Harness session event into checkpoint-hook input. */
export function checkpointPayload(session, event) {
  const sessionId = String(session?.id ?? '').trim()
  if (!sessionId || !event || typeof event !== 'object') return undefined

  if (event.type === 'user/message') {
    if (event.data?.source?.kind !== 'user') return undefined
    const content = textContent(event.data)
    if (!content.trim()) return undefined
    return {
      session_id: sessionId,
      turn_id: String(event.data?.id ?? event.seq ?? ''),
      hook_event_name: 'UserPromptSubmit',
      prompt: content,
      cwd: typeof session?.meta?.cwd === 'string' ? session.meta.cwd : '',
      occurred_at: new Date(event.time).toISOString(),
    }
  }

  if (event.type === 'assistant/message') {
    const message = event.data?.message
    const content = textContent(message)
    if (!content.trim()) return undefined
    return {
      session_id: sessionId,
      turn_id: String(message?.id ?? event.seq ?? ''),
      hook_event_name: 'Stop',
      last_assistant_message: content,
      cwd: typeof session?.meta?.cwd === 'string' ? session.meta.cwd : '',
      occurred_at: new Date(event.time).toISOString(),
    }
  }

  return undefined
}

function runCheckpoint(command, payload, timeoutMs) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, ['deepseek-harness'], {
      stdio: ['pipe', 'ignore', 'pipe'],
    })
    let stderr = ''
    const timer = setTimeout(() => {
      child.kill('SIGTERM')
      reject(new Error(`checkpoint adapter timed out after ${timeoutMs}ms`))
    }, timeoutMs)
    child.stderr.setEncoding('utf8')
    child.stderr.on('data', chunk => {
      if (stderr.length < 4096) stderr += chunk
    })
    child.once('error', error => {
      clearTimeout(timer)
      reject(error)
    })
    child.once('close', code => {
      clearTimeout(timer)
      if (code === 0) resolvePromise()
      else reject(new Error(stderr.trim() || `checkpoint adapter exited ${code}`))
    })
    child.stdin.end(JSON.stringify(payload))
  })
}

/** Register the ordered, non-blocking relay for the lifetime of the plugin. */
export function apply(ctx, config = {}) {
  const command = typeof config.command === 'string' && config.command.trim()
    ? config.command
    : defaultCommand
  const timeoutMs = Number.isFinite(config.timeoutMs) && config.timeoutMs > 0
    ? config.timeoutMs
    : 10000
  const queues = new Map()

  ctx.on('session/event', (session, event) => {
    const payload = checkpointPayload(session, event)
    if (!payload) return
    const key = payload.session_id
    const previous = queues.get(key) ?? Promise.resolve()
    const current = previous
      .catch(() => undefined)
      .then(() => runCheckpoint(command, payload, timeoutMs))
      .catch(error => {
        ctx.logger.warn(
          `mindmory-checkpoint-relay: ${event.type} failed for ${key} seq=${event.seq}: ${String(error)}`,
        )
      })
      .finally(() => {
        if (queues.get(key) === current) queues.delete(key)
      })
    queues.set(key, current)
  })
}
