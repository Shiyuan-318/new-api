import { describe, expect, test } from 'vitest'

import { renderAuditContent } from '@/features/usage-logs/lib/format'

import { TOOL_DESC_KEYS, TOOL_LABEL_KEYS } from '../constants'
import { parseFallbackToolCalls } from '../lib/parse-tool-calls'

const translate = (key: string, opts?: Record<string, unknown>): string => {
  let result = key
  for (const [name, value] of Object.entries(opts ?? {})) {
    result = result.replaceAll(`{{${name}}}`, String(value))
  }
  return result
}

const TOOL_NAMES = [
  'get_model_price',
  'update_model_price',
  'create_channel',
  'adjust_user_quota',
  'get_homepage_html',
  'update_homepage_html',
]

describe('parseFallbackToolCalls', () => {
  test('reads a fenced JSON block', () => {
    const text = [
      'Sure, here is the proposed change.',
      '```json',
      '{"tool_calls":[{"name":"update_model_price","arguments":{"model":"gpt-4o","ratio":2.5}}]}',
      '```',
    ].join('\n')
    expect(parseFallbackToolCalls(text)).toEqual([
      { name: 'update_model_price', arguments: { model: 'gpt-4o', ratio: 2.5 } },
    ])
  })

  test('reads a bare JSON object and fills missing arguments', () => {
    expect(
      parseFallbackToolCalls('{"tool_calls":[{"name":"get_homepage_html"}]}')
    ).toEqual([{ name: 'get_homepage_html', arguments: {} }])
  })

  test('ignores ordinary prose and malformed JSON', () => {
    expect(parseFallbackToolCalls('I cannot do that.')).toEqual([])
    expect(parseFallbackToolCalls('```json\n{broken```')).toEqual([])
    expect(parseFallbackToolCalls('')).toEqual([])
  })

  test('supports multiple calls in one block', () => {
    expect(
      parseFallbackToolCalls(
        '{"tool_calls":[{"name":"a","arguments":{}},{"name":"b","arguments":{"x":1}}]}'
      )
    ).toEqual([
      { name: 'a', arguments: {} },
      { name: 'b', arguments: { x: 1 } },
    ])
  })
})

describe('AI admin audit rendering', () => {
  test('renders the tool name and change summary in the audit log', () => {
    const other = {
      op: {
        action: 'ai_admin.execute',
        params: {
          tool: 'update_model_price',
          summary: 'updated price of model "gpt-4o": ratio 2 -> 3',
          success: true,
        },
      },
    }
    const rendered = renderAuditContent(
      other as Parameters<typeof renderAuditContent>[0],
      translate
    )
    expect(rendered).not.toBeNull()
    expect(rendered).toContain('update_model_price')
    expect(rendered).toContain('ratio 2 -> 3')
  })

  test('unknown actions still fall back to null', () => {
    expect(
      renderAuditContent(
        { op: { action: 'not.registered', params: {} } },
        translate
      )
    ).toBeNull()
  })
})

describe('tool catalogue labels', () => {
  test('every tool has a label and description key', () => {
    for (const name of TOOL_NAMES) {
      expect(TOOL_LABEL_KEYS[name], `missing label for ${name}`).toBeTruthy()
      expect(TOOL_DESC_KEYS[name], `missing description for ${name}`).toBeTruthy()
    }
  })
})
