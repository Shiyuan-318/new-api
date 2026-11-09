import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { useTranslation } from 'react-i18next'
import {
  Bot,
  Loader2,
  RefreshCw,
  Send,
  ShieldCheck,
  Sparkles,
  Trash2,
  Wrench,
} from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  executeAiAdminTool,
  getAiAdminTools,
  getAvailableGroups,
  getAvailableModels,
  sendAiAdminChat,
} from './api'
import { ToolCallCard } from './components/tool-call-card'
import {
  DEFAULT_GROUP,
  EXAMPLE_PROMPTS,
  TOOL_DESC_KEYS,
  TOOL_LABEL_KEYS,
} from './constants'
import type {
  AiAdminCatalogEntry,
  AiAdminOpenAITool,
  ChatMessage,
  ToolCall,
} from './types'

let idCounter = 0
function nextId(prefix: string): string {
  idCounter += 1
  return `${prefix}-${Date.now()}-${idCounter}`
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/** Convert the UI transcript into plain chat messages. Tool proposals and
 * their executed results are folded into text so any model can follow along
 * without requiring the OpenAI tool-result message format. */
function buildHistory(messages: ChatMessage[]): Array<{ role: string; content: string }> {
  const history: Array<{ role: string; content: string }> = []
  for (const message of messages) {
    if (message.error) continue
    if (message.role === 'user') {
      history.push({ role: 'user', content: message.content })
      continue
    }
    let content = message.content?.trim() ?? ''
    const calls = message.calls ?? []
    const pending = calls.filter((call) => call.status !== 'success')
    const executed = calls.filter((call) => call.status === 'success')
    if (pending.length) {
      content += `\n\n[Proposed tools awaiting confirmation]\n${pending
        .map((call) => `${call.name}: ${call.argsText}`)
        .join('\n')}`
    }
    if (executed.length) {
      content += `\n\n[Executed tool results]\n${executed
        .map((call) => `${call.name}: ${JSON.stringify(call.result)}`)
        .join('\n')}`
    }
    if (content.trim()) {
      history.push({ role: 'assistant', content })
    }
  }
  return history
}

export function AiAdmin() {
  const { t } = useTranslation()
  const [tools, setTools] = useState<AiAdminOpenAITool[]>([])
  const [catalog, setCatalog] = useState<AiAdminCatalogEntry[]>([])
  const [systemPrompt, setSystemPrompt] = useState('')
  const [models, setModels] = useState<string[]>([])
  const [groups, setGroups] = useState<string[]>([])
  const [model, setModel] = useState('')
  const [group, setGroup] = useState(DEFAULT_GROUP)
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [useFunctionCalling, setUseFunctionCalling] = useState(true)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  const catalogByName = useMemo(() => {
    const map: Record<string, AiAdminCatalogEntry> = {}
    for (const entry of catalog) map[entry.name] = entry
    return map
  }, [catalog])

  const loadTools = useCallback(async () => {
    setLoading(true)
    setLoadError('')
    try {
      const [toolsData, groupList] = await Promise.all([
        getAiAdminTools(),
        getAvailableGroups(),
      ])
      setTools(toolsData.tools ?? [])
      setCatalog(toolsData.catalog ?? [])
      setSystemPrompt(toolsData.system_prompt ?? '')
      const availableGroups = groupList.length ? groupList : [DEFAULT_GROUP]
      setGroups(availableGroups)
      setGroup((current) =>
        availableGroups.includes(current) ? current : availableGroups[0]
      )
    } catch (error) {
      setLoadError(errorMessage(error))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadTools()
  }, [loadTools])

  useEffect(() => {
    let active = true
    getAvailableModels(group)
      .then((list) => {
        if (!active) return
        setModels(list)
        setModel((current) =>
          list.includes(current) ? current : (list[0] ?? '')
        )
      })
      .catch(() => {
        if (active) setModels([])
      })
    return () => {
      active = false
    }
  }, [group])

  useEffect(() => {
    const node = scrollRef.current
    if (node) node.scrollTop = node.scrollHeight
  }, [messages])

  const updateCall = useCallback(
    (messageId: string, callId: string, patch: Partial<ToolCall>) => {
      setMessages((previous) =>
        previous.map((message) =>
          message.id !== messageId
            ? message
            : {
                ...message,
                calls: (message.calls ?? []).map((call) =>
                  call.id === callId ? { ...call, ...patch } : call
                ),
              }
        )
      )
    },
    []
  )

  const handleSend = useCallback(async () => {
    const text = input.trim()
    if (!text || sending) return
    if (!model) {
      toast.error(t('Please select a model first'))
      return
    }
    const userMessage: ChatMessage = {
      id: nextId('user'),
      role: 'user',
      content: text,
    }
    const placeholderId = nextId('assistant')
    const history = [...messages, userMessage]
    setMessages([
      ...history,
      { id: placeholderId, role: 'assistant', content: '', calls: [] },
    ])
    setInput('')
    setSending(true)
    try {
      const result = await sendAiAdminChat({
        model,
        group,
        tools,
        systemPrompt,
        useFunctionCalling,
        messages: buildHistory(history),
      })
      const calls: ToolCall[] = result.toolCalls.map((call) => ({
        id: nextId('call'),
        name: call.name,
        argsText: JSON.stringify(call.arguments ?? {}, null, 2),
        status: 'pending',
        mutating: catalogByName[call.name]?.mutating ?? true,
      }))
      setMessages((previous) =>
        previous.map((message) =>
          message.id === placeholderId
            ? { ...message, content: result.content, calls }
            : message
        )
      )
    } catch (error) {
      setMessages((previous) =>
        previous.map((message) =>
          message.id === placeholderId
            ? { ...message, error: errorMessage(error) }
            : message
        )
      )
    } finally {
      setSending(false)
    }
  }, [catalogByName, group, input, messages, model, sending, systemPrompt, tools, useFunctionCalling, t])

  const handleRun = useCallback(
    async (messageId: string, callId: string) => {
      const call = messages
        .find((message) => message.id === messageId)
        ?.calls?.find((item) => item.id === callId)
      if (!call) return
      let args: Record<string, unknown>
      try {
        args = JSON.parse(call.argsText) as Record<string, unknown>
      } catch {
        toast.error(t('Invalid JSON in arguments'))
        return
      }
      updateCall(messageId, callId, { status: 'running', error: undefined })
      try {
        const response = await executeAiAdminTool(call.name, args)
        if (!response.success) {
          throw new Error(response.message || 'Execution failed')
        }
        updateCall(messageId, callId, {
          status: 'success',
          result: response.data?.result,
          auditLogged: response.data?.audit_logged,
        })
        toast.success(t('Tool executed and audit logged'))
      } catch (error) {
        updateCall(messageId, callId, {
          status: 'error',
          error: errorMessage(error),
        })
      }
    },
    [messages, t, updateCall]
  )

  const clearChat = useCallback(() => {
    setMessages([])
    setInput('')
  }, [])

  return (
    <div className='flex size-full min-h-0 flex-col overflow-hidden'>
      <header className='flex flex-wrap items-center gap-2 border-b px-4 py-3'>
        <div className='flex items-center gap-2'>
          <Bot className='size-5 text-primary' />
          <h1 className='text-base font-semibold'>{t('AI Admin')}</h1>
          <Badge variant='outline' className='hidden sm:inline-flex'>
            <ShieldCheck className='mr-1 size-3' />
            {t('Super Admin only')}
          </Badge>
        </div>
        <div className='ml-auto flex flex-wrap items-center gap-2'>
          <Select value={group} onValueChange={setGroup}>
            <SelectTrigger className='h-8 w-32'>
              <SelectValue placeholder={t('Group')} />
            </SelectTrigger>
            <SelectContent>
              {groups.map((item) => (
                <SelectItem key={item} value={item}>
                  {item}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={model || undefined} onValueChange={setModel}>
            <SelectTrigger className='h-8 w-48'>
              <SelectValue placeholder={t('Select model')} />
            </SelectTrigger>
            <SelectContent>
              {models.map((item) => (
                <SelectItem key={item} value={item}>
                  {item}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <label className='flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground'>
            <Switch
              checked={useFunctionCalling}
              onCheckedChange={setUseFunctionCalling}
              className='scale-90'
            />
            {t('Native function calling')}
          </label>
          <Button
            variant='outline'
            size='sm'
            onClick={clearChat}
            disabled={messages.length === 0}
          >
            <Trash2 className='mr-1.5 size-4' />
            {t('Clear chat')}
          </Button>
        </div>
      </header>

      <div ref={scrollRef} className='min-h-0 flex-1 overflow-y-auto px-4 py-5'>
        <div className='mx-auto w-full max-w-4xl space-y-4'>
          {loading && (
            <div className='flex items-center justify-center gap-2 py-16 text-sm text-muted-foreground'>
              <Loader2 className='size-4 animate-spin' />
              {t('Loading tools...')}
            </div>
          )}
          {!loading && loadError && (
            <div className='mx-auto max-w-md space-y-3 rounded-lg border border-destructive/40 p-5 text-center'>
              <p className='text-sm text-destructive'>{loadError}</p>
              <Button variant='outline' size='sm' onClick={() => void loadTools()}>
                <RefreshCw className='mr-1.5 size-4' />
                {t('Retry')}
              </Button>
            </div>
          )}
          {!loading && !loadError && messages.length === 0 && (
            <div className='space-y-6 py-8 text-center'>
              <Sparkles className='mx-auto size-9 text-primary' />
              <div className='space-y-2'>
                <h2 className='text-lg font-semibold'>
                  {t('Manage your gateway with AI')}
                </h2>
                <p className='mx-auto max-w-xl text-sm text-muted-foreground'>
                  {t('Ask the assistant to change model prices, create channels, adjust user balances or edit the homepage. Every proposed action must be reviewed and confirmed by clicking Run before it is applied.')}
                </p>
              </div>
              <div className='flex flex-wrap justify-center gap-2'>
                {catalog.map((entry) => (
                  <Badge key={entry.name} variant='secondary'>
                    <Wrench className='mr-1 size-3' />
                    {t(TOOL_LABEL_KEYS[entry.name] ?? entry.name)}
                  </Badge>
                ))}
              </div>
              <div className='grid gap-2 text-left sm:grid-cols-2'>
                {EXAMPLE_PROMPTS.map((prompt) => (
                  <button
                    key={prompt}
                    type='button'
                    onClick={() => setInput(prompt)}
                    className='rounded-lg border p-3 text-sm transition-colors hover:bg-muted'
                  >
                    {t(prompt)}
                  </button>
                ))}
              </div>
            </div>
          )}

          {messages.map((message) => (
            <div key={message.id} className='space-y-2'>
              {message.role === 'user' ? (
                <div className='flex justify-end'>
                  <div className='max-w-[85%] whitespace-pre-wrap rounded-2xl rounded-br-sm bg-primary px-3.5 py-2.5 text-sm text-primary-foreground'>
                    {message.content}
                  </div>
                </div>
              ) : (
                <div className='space-y-3'>
                  {message.content && (
                    <div className='whitespace-pre-wrap rounded-2xl rounded-bl-sm border bg-card px-3.5 py-2.5 text-sm'>
                      {message.content}
                    </div>
                  )}
                  {message.error && (
                    <div className='rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive'>
                      {message.error}
                    </div>
                  )}
                  {(message.calls ?? []).map((call) => (
                    <ToolCallCard
                      key={call.id}
                      call={call}
                      description={
                        TOOL_DESC_KEYS[call.name]
                          ? t(TOOL_DESC_KEYS[call.name])
                          : catalogByName[call.name]?.description
                      }
                      onArgsChange={(value) =>
                        updateCall(message.id, call.id, { argsText: value })
                      }
                      onRun={() => void handleRun(message.id, call.id)}
                    />
                  ))}
                  {sending && message.calls?.length === 0 && !message.content && !message.error && (
                    <div className='flex items-center gap-2 text-sm text-muted-foreground'>
                      <Loader2 className='size-4 animate-spin' />
                      {t('Thinking...')}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>

      <footer className='border-t px-4 py-3'>
        <div className='mx-auto max-w-4xl space-y-2'>
          <div className='flex items-end gap-2'>
            <Textarea
              value={input}
              onChange={(event) => setInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
                  event.preventDefault()
                  void handleSend()
                }
              }}
              placeholder={t('Describe the management task you want the AI to propose...')}
              disabled={sending || loading}
              className='min-h-16 resize-y'
            />
            <Button
              onClick={() => void handleSend()}
              disabled={sending || loading || !input.trim()}
              className='h-10'
            >
              {sending ? (
                <Loader2 className='size-4 animate-spin' />
              ) : (
                <Send className='size-4' />
              )}
              <span className='ml-2 hidden sm:inline'>{t('Send')}</span>
            </Button>
          </div>
          <p className='text-xs text-muted-foreground'>
            {t('AI requests use the models available to your account. Tool calls are only suggestions until you click Run; every execution is written to /usage-logs/audit.')}
          </p>
        </div>
      </footer>
    </div>
  )
}
