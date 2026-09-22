/**
 * Zero-schema native Mindmory integration for DeepSeek Harness.
 *
 * The agent pre-step path archives each direct prompt and retrieves strict,
 * bounded relevance through the distribution's credential-hiding adapter.
 * The returned plain text is appended as a source-attributed recall message,
 * so Harness records exactly what the model sees. Completed assembled replies
 * are archived from the canonical session event stream. No Mindmory MCP tools
 * or credentials enter the Harness profile or model context.
 */
import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const name = 'mindmory-native-relay'
export const inject = ['agents', 'sessions']

const DEFAULT_TIMEOUT_MS = 10_000
const DEFAULT_MAX_CHARS = 320
const OUTPUT_LIMIT_BYTES = 8 * 1024
const SECRET_ENV_NAME = /(token|secret|key|password)/i

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

function sessionCwd(session) {
  if (typeof session?.header?.cwd === 'string') return session.header.cwd
  if (typeof session?.meta?.cwd === 'string') return session.meta.cwd
  return ''
}

function eventTimestamp(value) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString()
}

/** Convert one relevant Harness session event into native-hook input. */
export function checkpointPayload(session, event) {
  const sessionId = String(session?.id ?? '').trim()
  if (!sessionId || !event || typeof event !== 'object') return undefined

  const occurredAt = eventTimestamp(event.time)
  if (!occurredAt) return undefined

  if (event.type === 'user/message') {
    if (event.data?.source?.kind !== 'user') return undefined
    const content = textContent(event.data)
    if (!content.trim()) return undefined
    return {
      session_id: sessionId,
      turn_id: String(event.data?.id ?? event.seq ?? ''),
      hook_event_name: 'UserPromptSubmit',
      prompt: content,
      cwd: sessionCwd(session),
      occurred_at: occurredAt,
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
      cwd: sessionCwd(session),
      occurred_at: occurredAt,
    }
  }

  return undefined
}

/** Build native-hook input from the latest direct prompt in a pre-step batch. */
export function promptPayload(agent, messages) {
  const session = agent?.session
  const sessionId = String(session?.id ?? '').trim()
  if (!sessionId || !Array.isArray(messages)) return undefined
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index]
    if (message?.role !== 'user' || message?.source?.kind !== 'user') continue
    const content = textContent(message)
    if (!content.trim()) continue
    return {
      session_id: sessionId,
      turn_id: String(message.id ?? ''),
      hook_event_name: 'UserPromptSubmit',
      prompt: content,
      cwd: sessionCwd(session),
      occurred_at: new Date().toISOString(),
    }
  }
  return undefined
}

/** Return a child environment without ambient credentials. */
export function scrubEnvironment(environment = process.env) {
  return Object.fromEntries(
    Object.entries(environment).filter(([key, value]) => value !== undefined && !SECRET_ENV_NAME.test(key)),
  )
}

function runAdapter(command, payload, timeoutMs) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, ['deepseek-harness'], {
      stdio: ['pipe', 'pipe', 'pipe'],
      env: scrubEnvironment(),
    })
    let stdout = ''
    let stderr = ''
    let stdoutBytes = 0
    let stderrBytes = 0
    let timedOut = false
    let oversized = false
    let forceKill
    const timer = setTimeout(() => {
      timedOut = true
      child.kill('SIGTERM')
      forceKill = setTimeout(() => child.kill('SIGKILL'), 1_000)
      forceKill.unref?.()
    }, timeoutMs)
    child.stdout.setEncoding('utf8')
    child.stderr.setEncoding('utf8')
    child.stdout.on('data', chunk => {
      const bytes = Buffer.byteLength(chunk)
      if (stdoutBytes + bytes <= OUTPUT_LIMIT_BYTES) {
        stdout += chunk
        stdoutBytes += bytes
      } else {
        oversized = true
        child.kill('SIGTERM')
      }
    })
    child.stderr.on('data', chunk => {
      const bytes = Buffer.byteLength(chunk)
      if (stderrBytes + bytes <= OUTPUT_LIMIT_BYTES) {
        stderr += chunk
        stderrBytes += bytes
      }
    })
    child.once('error', error => {
      clearTimeout(timer)
      if (forceKill) clearTimeout(forceKill)
      reject(error)
    })
    child.once('close', code => {
      clearTimeout(timer)
      if (forceKill) clearTimeout(forceKill)
      if (timedOut) {
        reject(new Error(`native adapter timed out after ${timeoutMs}ms`))
        return
      }
      if (oversized) {
        reject(new Error('native adapter exceeded its output limit'))
        return
      }
      if (code !== 0) {
        reject(new Error(stderr.trim() || `native adapter exited ${code}`))
        return
      }
      try {
        const output = JSON.parse(stdout || '{}')
        resolvePromise(typeof output.additional_context === 'string' ? output.additional_context : '')
      } catch {
        reject(new Error('native adapter returned invalid JSON'))
      }
    })
    child.stdin.end(JSON.stringify(payload))
  })
}

function deepFreeze(value) {
  if (!value || typeof value !== 'object' || Object.isFrozen(value)) return value
  for (const child of Object.values(value)) deepFreeze(child)
  return Object.freeze(value)
}

function recallMessage(text) {
  return deepFreeze({
    id: randomUUID(),
    role: 'user',
    content: [{ type: 'text', text }],
    source: { kind: 'plugin', plugin: name, form: 'recall' },
  })
}

/** Register native prompt retrieval and ordered assistant checkpointing. */
export function apply(ctx, config = {}) {
  const command = typeof config.command === 'string' && config.command.trim()
    ? config.command
    : defaultCommand
  const timeoutMs = Number.isFinite(config.timeoutMs) && config.timeoutMs > 0
    ? config.timeoutMs
    : DEFAULT_TIMEOUT_MS
  const maxChars = Number.isInteger(config.maxChars) && config.maxChars > 0
    ? Math.min(config.maxChars, DEFAULT_MAX_CHARS)
    : DEFAULT_MAX_CHARS
  const queues = new Map()

  ctx.on('agent/pre-step', async ({ agent, messages }, next) => {
    const payload = promptPayload(agent, messages)
    if (!payload) return next()
    let additionalContext = ''
    try {
      additionalContext = await runAdapter(command, payload, timeoutMs)
    } catch (error) {
      ctx.logger.warn(
        `mindmory-native-relay: prompt retrieval failed for ${payload.session_id}: ${String(error)}`,
      )
    }
    const downstream = await next()
    if (downstream.kind !== 'enter' || additionalContext.trim() === '') return downstream
    const bounded = Array.from(additionalContext).slice(0, maxChars).join('')
    return {
      kind: 'enter',
      messages: [...downstream.messages, recallMessage(bounded)],
    }
  })

  ctx.on('session/event', (session, event) => {
    if (event?.type !== 'assistant/message') return
    const payload = checkpointPayload(session, event)
    if (!payload) return
    const key = payload.session_id
    const previous = queues.get(key) ?? Promise.resolve()
    const current = previous
      .catch(() => undefined)
      .then(() => runAdapter(command, payload, timeoutMs))
      .catch(error => {
        ctx.logger.warn(
          `mindmory-native-relay: assistant checkpoint failed for ${key} seq=${event.seq}: ${String(error)}`,
        )
      })
      .finally(() => {
        if (queues.get(key) === current) queues.delete(key)
      })
    queues.set(key, current)
  })
}
