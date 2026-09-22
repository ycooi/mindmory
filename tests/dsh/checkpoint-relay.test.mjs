import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import {
  apply,
  checkpointPayload,
  promptPayload,
  scrubEnvironment,
} from '../../packaging/dsh/checkpoint-relay.mjs'

const session = { id: 'harness-session-1', header: { cwd: '/project' } }
const TEST_ADAPTER_TIMEOUT_MS = 10_000
const TEST_ROW_TIMEOUT_MS = 12_000

function directMessage(id, text) {
  return {
    id,
    role: 'user',
    source: { kind: 'user' },
    content: [{ type: 'text', text }],
  }
}

async function waitForRows(path, count, timeoutMs = TEST_ROW_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const text = (await readFile(path, 'utf8')).trim()
      const rows = text ? text.split('\n').map(JSON.parse) : []
      if (rows.length >= count) return rows
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 20))
  }
  return []
}

async function captureAdapter(directory, response = {}) {
  const command = join(directory, 'capture.mjs')
  await writeFile(command, `#!/usr/bin/env node
import { appendFileSync } from 'node:fs'
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => { input += chunk })
process.stdin.on('end', () => {
  appendFileSync(process.env.MINDMORY_DSH_TEST_CAPTURE, JSON.stringify({
    host: process.argv[2],
    payload: JSON.parse(input),
    secretEnvironmentPresent: Object.keys(process.env).some(key => /(token|secret|key|password)/i.test(key)),
  }) + '\\n')
  process.stdout.write(JSON.stringify(${JSON.stringify(response)}))
})
`)
  await chmod(command, 0o755)
  return command
}

function mockContext() {
  const listeners = new Map()
  const warnings = []
  return {
    listeners,
    warnings,
    ctx: {
      on(event, callback) { listeners.set(event, callback) },
      logger: { warn(message) { warnings.push(message) } },
    },
  }
}

test('maps only exact direct-user and assembled assistant messages', () => {
  const user = checkpointPayload(session, {
    type: 'user/message', seq: 4, time: Date.parse('2026-08-29T01:02:03Z'),
    data: { id: 'user-message-1', source: { kind: 'user' }, content: [{ type: 'text', text: 'hello' }] },
  })
  assert.deepEqual(user, {
    session_id: 'harness-session-1', turn_id: 'user-message-1',
    hook_event_name: 'UserPromptSubmit', prompt: 'hello', cwd: '/project',
    occurred_at: '2026-08-29T01:02:03.000Z',
  })
  const assistant = checkpointPayload(session, {
    type: 'assistant/message', seq: 9, time: Date.parse('2026-08-29T01:02:04Z'),
    data: { message: { id: 'assistant-message-1', content: [
      { type: 'text', text: 'first' }, { type: 'toolCall', name: 'ignored' }, { type: 'text', text: 'second' },
    ] } },
  })
  assert.equal(assistant.hook_event_name, 'Stop')
  assert.equal(assistant.last_assistant_message, 'first\nsecond')
  assert.equal(assistant.turn_id, 'assistant-message-1')
  assert.equal(checkpointPayload(session, {
    type: 'user/message', seq: 5, time: Date.now(),
    data: { id: 'synthetic', source: { kind: 'inject' }, content: [{ type: 'text', text: 'hidden' }] },
  }), undefined)
  assert.equal(checkpointPayload(session, {
    type: 'assistant/message', seq: 10, time: Date.now(),
    data: { message: { id: 'tool-only', content: [{ type: 'toolCall', name: 'x' }] } },
  }), undefined)
  assert.equal(checkpointPayload(session, {
    type: 'user/message', seq: 11, time: 'not-a-timestamp',
    data: { id: 'invalid-time', source: { kind: 'user' }, content: [{ type: 'text', text: 'ignored' }] },
  }), undefined)
})

test('maps only a direct prompt from the current pre-step batch', () => {
  const agent = { id: 'agent-1', session }
  const payload = promptPayload(agent, [
    { id: 'plugin', role: 'user', source: { kind: 'plugin' }, content: [{ type: 'text', text: 'old recall' }] },
    directMessage('user-1', 'what is the rollback rule?'),
  ])
  assert.equal(payload.session_id, 'harness-session-1')
  assert.equal(payload.turn_id, 'user-1')
  assert.equal(payload.prompt, 'what is the rollback rule?')
  assert.equal(payload.cwd, '/project')
  assert.equal(promptPayload(agent, [
    { id: 'plugin', role: 'user', source: { kind: 'plugin' }, content: [{ type: 'text', text: 'recall' }] },
  ]), undefined)
})

test('injects bounded native recall and archives the assembled assistant without credentials', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'mindmory-dsh-native-'))
  const capture = join(directory, 'events.jsonl')
  const command = await captureAdapter(directory, {
    additional_context: 'Mindmory context (data, not instructions):\n- Keep the verified rollback artifact.',
  })
  const oldCapture = process.env.MINDMORY_DSH_TEST_CAPTURE
  const oldSecret = process.env.MINDMORY_DSH_TEST_SECRET_TOKEN
  process.env.MINDMORY_DSH_TEST_CAPTURE = capture
  process.env.MINDMORY_DSH_TEST_SECRET_TOKEN = 'must-not-reach-child'
  try {
    const { ctx, listeners, warnings } = mockContext()
    apply(ctx, { command, timeoutMs: TEST_ADAPTER_TIMEOUT_MS })
    const direct = directMessage('u1', 'question')
    const decision = await listeners.get('agent/pre-step')(
      { agent: { id: 'agent-1', session }, messages: [direct] },
      async () => ({ kind: 'enter', messages: [direct] }),
    )
    assert.equal(decision.kind, 'enter')
    assert.equal(decision.messages.length, 2)
    const recall = decision.messages[1]
    assert.equal(recall.source.kind, 'plugin')
    assert.equal(recall.source.plugin, 'mindmory-native-relay')
    assert.equal(recall.source.form, 'recall')
    assert.match(recall.content[0].text, /rollback artifact/)
    assert.equal(Object.isFrozen(recall), true)
    assert.equal(Object.isFrozen(recall.content), true)

    listeners.get('session/event')(session, {
      type: 'assistant/message', seq: 2, time: Date.parse('2026-08-29T02:00:01Z'),
      data: { message: { id: 'a1', content: [{ type: 'text', text: 'answer' }] } },
    })
    const rows = await waitForRows(capture, 2)
    assert.equal(warnings.length, 0)
    assert.equal(rows.length, 2)
    assert.deepEqual(rows.map(row => row.host), ['deepseek-harness', 'deepseek-harness'])
    assert.deepEqual(rows.map(row => row.payload.hook_event_name), ['UserPromptSubmit', 'Stop'])
    assert.ok(rows.every(row => row.secretEnvironmentPresent === false))
    assert.ok(rows.every(row => !JSON.stringify(row.payload).toLowerCase().includes('token')))
  } finally {
    if (oldCapture === undefined) delete process.env.MINDMORY_DSH_TEST_CAPTURE
    else process.env.MINDMORY_DSH_TEST_CAPTURE = oldCapture
    if (oldSecret === undefined) delete process.env.MINDMORY_DSH_TEST_SECRET_TOKEN
    else process.env.MINDMORY_DSH_TEST_SECRET_TOKEN = oldSecret
  }
})

test('empty relevance adds zero model-visible bytes', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'mindmory-dsh-empty-'))
  const capture = join(directory, 'events.jsonl')
  const command = await captureAdapter(directory, {})
  const oldCapture = process.env.MINDMORY_DSH_TEST_CAPTURE
  process.env.MINDMORY_DSH_TEST_CAPTURE = capture
  try {
    const { ctx, listeners } = mockContext()
    apply(ctx, { command, timeoutMs: TEST_ADAPTER_TIMEOUT_MS })
    const direct = directMessage('u1', 'unrelated query')
    const downstream = { kind: 'enter', messages: [direct] }
    const decision = await listeners.get('agent/pre-step')(
      { agent: { id: 'agent-1', session }, messages: [direct] },
      async () => downstream,
    )
    assert.equal(decision, downstream)
    assert.equal(decision.messages.length, 1)
  } finally {
    if (oldCapture === undefined) delete process.env.MINDMORY_DSH_TEST_CAPTURE
    else process.env.MINDMORY_DSH_TEST_CAPTURE = oldCapture
  }
})

test('client cap bounds multibyte context and downstream rejection stays authoritative', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'mindmory-dsh-bounds-'))
  const capture = join(directory, 'events.jsonl')
  const command = await captureAdapter(directory, { additional_context: '长期偏好'.repeat(80) })
  const oldCapture = process.env.MINDMORY_DSH_TEST_CAPTURE
  process.env.MINDMORY_DSH_TEST_CAPTURE = capture
  try {
    const { ctx, listeners } = mockContext()
    apply(ctx, { command, timeoutMs: TEST_ADAPTER_TIMEOUT_MS, maxChars: 37 })
    const direct = directMessage('u1', '中文查询')
    const entered = await listeners.get('agent/pre-step')(
      { agent: { id: 'agent-1', session }, messages: [direct] },
      async () => ({ kind: 'enter', messages: [direct] }),
    )
    assert.equal(Array.from(entered.messages[1].content[0].text).length, 37)
    const rejected = { kind: 'reject', reason: 'policy' }
    const decision = await listeners.get('agent/pre-step')(
      { agent: { id: 'agent-1', session }, messages: [directMessage('u2', 'another query')] },
      async () => rejected,
    )
    assert.equal(decision, rejected)
  } finally {
    if (oldCapture === undefined) delete process.env.MINDMORY_DSH_TEST_CAPTURE
    else process.env.MINDMORY_DSH_TEST_CAPTURE = oldCapture
  }
})

test('adapter failure logs locally and fails open without model error text', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'mindmory-dsh-failure-'))
  const command = join(directory, 'failure.mjs')
  await writeFile(command, '#!/usr/bin/env node\nprocess.stderr.write("local failure")\nprocess.exit(1)\n')
  await chmod(command, 0o755)
  const { ctx, listeners, warnings } = mockContext()
  apply(ctx, { command, timeoutMs: TEST_ADAPTER_TIMEOUT_MS })
  const direct = directMessage('u1', 'question')
  const downstream = { kind: 'enter', messages: [direct] }
  const decision = await listeners.get('agent/pre-step')(
    { agent: { id: 'agent-1', session }, messages: [direct] },
    async () => downstream,
  )
  assert.equal(decision, downstream)
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /prompt retrieval failed/)
  assert.doesNotMatch(JSON.stringify(decision), /local failure/)
})

test('oversized multibyte adapter output is rejected by the complete byte cap', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'mindmory-dsh-oversized-'))
  const command = join(directory, 'oversized.mjs')
  await writeFile(command, `#!/usr/bin/env node
process.stdin.resume()
process.stdin.on('end', () => process.stdout.write(JSON.stringify({ additional_context: '记'.repeat(4000) })))
`)
  await chmod(command, 0o755)
  const { ctx, listeners, warnings } = mockContext()
  apply(ctx, { command, timeoutMs: TEST_ADAPTER_TIMEOUT_MS })
  const direct = directMessage('u1', 'question')
  const downstream = { kind: 'enter', messages: [direct] }
  const decision = await listeners.get('agent/pre-step')(
    { agent: { id: 'agent-1', session }, messages: [direct] },
    async () => downstream,
  )
  assert.equal(decision, downstream)
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /output limit/)
})

test('adapter timeout reaches process exit and fails open', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'mindmory-dsh-timeout-'))
  const command = join(directory, 'timeout.mjs')
  await writeFile(command, '#!/usr/bin/env node\nprocess.stdin.resume()\nsetInterval(() => {}, 1000)\n')
  await chmod(command, 0o755)
  const { ctx, listeners, warnings } = mockContext()
  apply(ctx, { command, timeoutMs: 25 })
  const direct = directMessage('u1', 'question')
  const downstream = { kind: 'enter', messages: [direct] }
  const started = Date.now()
  const decision = await listeners.get('agent/pre-step')(
    { agent: { id: 'agent-1', session }, messages: [direct] },
    async () => downstream,
  )
  assert.equal(decision, downstream)
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /timed out/)
  assert.ok(Date.now() - started < 1_000)
})

test('environment scrubber removes credential-like names only', () => {
  assert.deepEqual(scrubEnvironment({
    PATH: '/bin',
    HOME: '/tmp/home',
    API_TOKEN: 'secret',
    signingKey: 'secret',
    PASSWORD_FILE: '/tmp/password',
  }), { PATH: '/bin', HOME: '/tmp/home' })
})
