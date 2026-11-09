import { useState } from 'react'

import { useTranslation } from 'react-i18next'
import {
  CheckCircle2,
  ClipboardList,
  Loader2,
  Play,
  ShieldAlert,
  XCircle,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

import { TOOL_LABEL_KEYS } from '../constants'
import type { ToolCall } from '../types'

interface ToolCallCardProps {
  call: ToolCall
  description?: string
  onArgsChange: (value: string) => void
  onRun: () => void
}

export function ToolCallCard({
  call,
  description,
  onArgsChange,
  onRun,
}: ToolCallCardProps) {
  const { t } = useTranslation()
  const [jsonError, setJsonError] = useState('')
  const editable = call.status === 'pending' || call.status === 'error'
  const label = t(TOOL_LABEL_KEYS[call.name] ?? call.name)

  const validate = (value: string): boolean => {
    try {
      const parsed = JSON.parse(value)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setJsonError(t('Arguments must be a JSON object'))
        return false
      }
      setJsonError('')
      return true
    } catch {
      setJsonError(t('Invalid JSON in arguments'))
      return false
    }
  }

  const statusBadge = () => {
    switch (call.status) {
      case 'running':
        return (
          <Badge variant='secondary'>
            <Loader2 className='mr-1 size-3 animate-spin' />
            {t('Running')}
          </Badge>
        )
      case 'success':
        return (
          <Badge variant='secondary' className='text-emerald-600'>
            <CheckCircle2 className='mr-1 size-3' />
            {t('Executed')}
          </Badge>
        )
      case 'error':
        return (
          <Badge variant='destructive'>
            <XCircle className='mr-1 size-3' />
            {t('Failed')}
          </Badge>
        )
      default:
        return <Badge variant='outline'>{t('Awaiting confirmation')}</Badge>
    }
  }

  return (
    <div className='space-y-2 rounded-lg border bg-card p-3 shadow-sm'>
      <div className='flex flex-wrap items-center gap-2'>
        <span className='text-sm font-medium'>{label}</span>
        <code className='rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground'>
          {call.name}
        </code>
        {call.mutating && (
          <Badge variant='outline' className='border-amber-500/40 text-amber-600'>
            <ShieldAlert className='mr-1 size-3' />
            {t('Changes data')}
          </Badge>
        )}
        <span className='ml-auto'>{statusBadge()}</span>
      </div>

      {description && (
        <p className='text-xs text-muted-foreground'>{description}</p>
      )}

      <div className='space-y-1'>
        <div className='text-xs font-medium text-muted-foreground'>
          {t('Arguments (editable before running)')}
        </div>
        <Textarea
          value={call.argsText}
          onChange={(event) => onArgsChange(event.target.value)}
          disabled={!editable}
          spellCheck={false}
          className='min-h-20 resize-y font-mono text-xs leading-relaxed'
        />
        {jsonError && (
          <p className='text-xs text-destructive'>{jsonError}</p>
        )}
      </div>

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <span className='text-xs text-muted-foreground'>
          {t('Nothing is applied until you click Run.')}
        </span>
        <Button
          size='sm'
          disabled={!editable || call.status === 'running'}
          onClick={() => {
            if (validate(call.argsText)) onRun()
          }}
        >
          {call.status === 'running' ? (
            <Loader2 className='mr-1.5 size-4 animate-spin' />
          ) : (
            <Play className='mr-1.5 size-4' />
          )}
          {t('Run')}
        </Button>
      </div>

      {call.status === 'success' && (
        <div className='space-y-1 rounded-md bg-muted/60 p-2'>
          <div className='text-xs font-medium'>{t('Result')}</div>
          <pre className='max-h-60 overflow-auto whitespace-pre-wrap break-all font-mono text-xs'>
            {JSON.stringify(call.result, null, 2)}
          </pre>
          {call.auditLogged && (
            <div className='flex items-center gap-1 text-xs text-muted-foreground'>
              <ClipboardList className='size-3' />
              {t('Change recorded in audit logs')}
            </div>
          )}
        </div>
      )}
      {call.status === 'error' && call.error && (
        <div className='rounded-md bg-destructive/10 p-2 text-xs text-destructive'>
          {call.error}
        </div>
      )}
    </div>
  )
}
