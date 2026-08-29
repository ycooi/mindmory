import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { apply, checkpointPayload } from '../../packaging/dsh/checkpoint-relay.mjs'

const session = { id: 'harness-session-1', meta: { cwd: '/project' } }

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
})

test('relays user then assistant in session event order without credentials', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'mindmory-dsh-relay-'))
  const command = join(dir, 'capture.mjs')
  const capture = join(dir, 'events.jsonl')
  await writeFile(command, `#!/usr/bin/env node
import { appendFileSync } from 'node:fs'
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => { input += chunk })
process.stdin.on('end', () => appendFileSync(process.env.MINDMORY_DSH_TEST_CAPTURE, JSON.stringify({ host: process.argv[2], payload: JSON.parse(input) }) + '\\n'))
`)
  await chmod(command, 0o755)
  const oldCapture = process.env.MINDMORY_DSH_TEST_CAPTURE
  process.env.MINDMORY_DSH_TEST_CAPTURE = capture
  const warnings = []
  let listener
  const ctx = {
    on(event, callback) { assert.equal(event, 'session/event'); listener = callback },
    logger: { warn(message) { warnings.push(message) } },
  }
  apply(ctx, { command, timeoutMs: 5000 })
  listener(session, {
    type: 'user/message', seq: 1, time: Date.parse('2026-08-29T02:00:00Z'),
    data: { id: 'u1', source: { kind: 'user' }, content: [{ type: 'text', text: 'question' }] },
  })
  listener(session, {
    type: 'assistant/message', seq: 2, time: Date.parse('2026-08-29T02:00:01Z'),
    data: { message: { id: 'a1', content: [{ type: 'text', text: 'answer' }] } },
  })
  let rows = []
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try { rows = (await readFile(capture, 'utf8')).trim().split('\n').map(JSON.parse) } catch {}
    if (rows.length === 2) break
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (oldCapture === undefined) delete process.env.MINDMORY_DSH_TEST_CAPTURE
  else process.env.MINDMORY_DSH_TEST_CAPTURE = oldCapture
  assert.equal(warnings.length, 0)
  assert.equal(rows.length, 2)
  assert.deepEqual(rows.map(row => row.host), ['deepseek-harness', 'deepseek-harness'])
  assert.deepEqual(rows.map(row => row.payload.hook_event_name), ['UserPromptSubmit', 'Stop'])
  assert.ok(rows.every(row => !JSON.stringify(row).toLowerCase().includes('token')))
})
