export interface AiAdminToolFunction {
  name: string
  description: string
  parameters: Record<string, unknown>
}

export interface AiAdminOpenAITool {
  type: 'function'
  function: AiAdminToolFunction
}

export interface AiAdminCatalogEntry {
  name: string
  description: string
  mutating: boolean
  parameters: Record<string, unknown>
}

export interface AiAdminToolsData {
  tools: AiAdminOpenAITool[]
  catalog: AiAdminCatalogEntry[]
  system_prompt: string
}

export type ToolCallStatus = 'pending' | 'running' | 'success' | 'error'

export interface ToolCall {
  id: string
  name: string
  argsText: string
  status: ToolCallStatus
  mutating: boolean
  result?: unknown
  error?: string
  auditLogged?: boolean
}

export interface ChatMessage {
  id: string
  role: 'user' | 'assistant'
  content: string
  calls?: ToolCall[]
  error?: string
}

export interface ExecuteToolResponse {
  success: boolean
  message: string
  data?: {
    tool: string
    summary: string
    result: unknown
    audit_logged: boolean
  }
}

export interface ProposedToolCall {
  name: string
  arguments: Record<string, unknown>
}

export interface AiChatResult {
  content: string
  toolCalls: ProposedToolCall[]
}
