import type { ProposedToolCall } from '../types'

/** Parse the JSON fallback used by models without native function calling.
 * Accepts a fenced ```json block or a bare JSON object/array and returns the
 * proposed calls. Invalid JSON is ignored so ordinary assistant prose is
 * never mistaken for a tool call. */
export function parseFallbackToolCalls(text: string): ProposedToolCall[] {
  if (!text) return []
  const candidates: string[] = []
  const fence = /```(?:json)?\s*([\s\S]*?)```/g
  let match: RegExpExecArray | null
  while ((match = fence.exec(text)) !== null) {
    candidates.push(match[1])
  }
  candidates.push(text)
  for (const candidate of candidates) {
    try {
      const parsed = JSON.parse(candidate.trim()) as {
        tool_calls?: Array<{ name: string; arguments?: Record<string, unknown> }>
      }
      const calls = Array.isArray(parsed) ? parsed : parsed?.tool_calls
      if (!Array.isArray(calls)) continue
      const valid = calls
        .filter(
          (call): call is { name: string; arguments: Record<string, unknown> } =>
            Boolean(call && typeof call.name === 'string' && call.name)
        )
        .map((call) => ({
          name: call.name,
          arguments: call.arguments ?? {},
        }))
      if (valid.length) return valid
    } catch {
      // Try the next candidate.
    }
  }
  return []
}
