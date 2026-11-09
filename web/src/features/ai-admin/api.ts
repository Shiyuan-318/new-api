import { api } from '@/lib/api'

import {
  AI_ADMIN_CHAT_ENDPOINT,
  AI_ADMIN_EXECUTE_ENDPOINT,
  AI_ADMIN_TOOLS_ENDPOINT,
} from './constants'
import { parseFallbackToolCalls } from './lib/parse-tool-calls'
import type {
  AiAdminOpenAITool,
  AiAdminToolsData,
  AiChatResult,
  ExecuteToolResponse,
  ProposedToolCall,
} from './types'

export { parseFallbackToolCalls }

interface ServerEnvelope<T> {
  success: boolean
  message?: string
  data?: T
}

export async function getAiAdminTools(): Promise<AiAdminToolsData> {
  const res = await api.get<ServerEnvelope<AiAdminToolsData>>(
    AI_ADMIN_TOOLS_ENDPOINT
  )
  if (!res.data?.success || !res.data.data) {
    throw new Error(res.data?.message || 'Failed to load AI admin tools')
  }
  return res.data.data
}

export async function getAvailableModels(group: string): Promise<string[]> {
  const res = await api.get<ServerEnvelope<string[]>>('/api/user/models', {
    params: { group },
  })
  if (!res.data?.success || !Array.isArray(res.data.data)) return []
  return res.data.data
}

export async function getAvailableGroups(): Promise<string[]> {
  const res = await api.get<
    ServerEnvelope<Record<string, { desc: string; ratio: number | string }>>
  >('/api/user/self/groups')
  if (!res.data?.success || !res.data.data) return []
  return Object.keys(res.data.data)
}

export async function executeAiAdminTool(
  name: string,
  args: Record<string, unknown>
): Promise<ExecuteToolResponse> {
  const res = await api.post<ExecuteToolResponse>(
    AI_ADMIN_EXECUTE_ENDPOINT,
    { name, arguments: args },
    { skipErrorHandler: true }
  )
  return res.data
}

interface RawToolCall {
  function?: {
    name?: string
    arguments?: string | Record<string, unknown>
  }
}

function normalizeArguments(raw: unknown): Record<string, unknown> {
  if (raw && typeof raw === 'object') return raw as Record<string, unknown>
  if (typeof raw === 'string') {
    try {
      const parsed = JSON.parse(raw)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>
      }
    } catch {
      // Fall through: invalid arguments are surfaced in the editable card.
    }
  }
  return {}
}

interface ChatCompletionBody {
  error?: { message?: string; code?: string }
  choices?: Array<{
    message?: {
      content?: string | null
      tool_calls?: RawToolCall[]
    }
  }>
}

export async function sendAiAdminChat(payload: {
  model: string
  group: string
  tools: AiAdminOpenAITool[]
  systemPrompt: string
  useFunctionCalling: boolean
  messages: Array<{ role: string; content: string }>
}): Promise<AiChatResult> {
  const requestBody: Record<string, unknown> = {
    model: payload.model,
    group: payload.group,
    stream: false,
    temperature: 0.2,
    messages: [
      { role: 'system', content: payload.systemPrompt },
      ...payload.messages,
    ],
  }
  // Some channels/models do not support native function calling. In prompt
  // mode the system prompt's tool catalogue plus the JSON fallback is used.
  if (payload.useFunctionCalling) {
    requestBody.tools = payload.tools
    requestBody.tool_choice = 'auto'
  }
  const res = await api.post<ChatCompletionBody>(
    AI_ADMIN_CHAT_ENDPOINT,
    requestBody,
    { skipErrorHandler: true }
  )
  const body = res.data
  if (body?.error) {
    const message =
      body.error.message || body.error.code || 'Upstream model returned an error'
    throw new Error(String(message))
  }
  const message = body?.choices?.[0]?.message
  if (!message) {
    throw new Error('Unexpected response from the model')
  }
  const content = typeof message.content === 'string' ? message.content : ''
  let toolCalls: ProposedToolCall[] = []
  if (Array.isArray(message.tool_calls) && message.tool_calls.length > 0) {
    toolCalls = (message.tool_calls as RawToolCall[])
      .map((call) => ({
        name: call?.function?.name ?? '',
        arguments: normalizeArguments(call?.function?.arguments),
      }))
      .filter((call) => Boolean(call.name))
  }
  if (toolCalls.length === 0) {
    toolCalls = parseFallbackToolCalls(content)
  }
  return { content, toolCalls }
}
