/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'
import { Download, RefreshCw, Trash2 } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'

const dataArchiveSchema = z.object({
  data_archive_setting: z.object({
    enabled: z.boolean(),
    storage_mode: z.enum(['local', 'database']),
    db_type: z.enum(['mysql', 'postgres', 'sqlite']),
    dsn: z.string(),
    table_name: z.string(),
    sync_hour: z.coerce.number().int().min(0).max(23),
    max_body_bytes: z.coerce.number().int().min(1024).max(524288),
    max_local_mb: z.coerce.number().int().min(1),
    max_file_mb: z.coerce.number().int().min(1).max(1024),
  }),
})

type DataArchiveFormInput = z.input<typeof dataArchiveSchema>
type DataArchiveFormValues = z.output<typeof dataArchiveSchema>

type FlatDataArchiveDefaults = {
  'data_archive_setting.enabled': boolean
  'data_archive_setting.storage_mode': 'local' | 'database'
  'data_archive_setting.db_type': 'mysql' | 'postgres' | 'sqlite'
  'data_archive_setting.dsn': string
  'data_archive_setting.table_name': string
  'data_archive_setting.sync_hour': number
  'data_archive_setting.max_body_bytes': number
  'data_archive_setting.max_local_mb': number
  'data_archive_setting.max_file_mb': number
}

type DataArchiveSettingsSectionProps = {
  defaultValues: FlatDataArchiveDefaults
}

type ArchiveFileEntry = {
  name: string
  size_bytes: number
  entries: number
  mod_time: number
}

type ArchiveStatusData = {
  enabled: boolean
  storage_mode: string
  max_file_mb: number
  files: ArchiveFileEntry[]
  total_bytes: number
  database?: { table: string; entries: number }
}

const buildFormDefaults = (
  defaults: DataArchiveSettingsSectionProps['defaultValues']
): DataArchiveFormInput => ({
  data_archive_setting: {
    enabled: defaults['data_archive_setting.enabled'],
    storage_mode: defaults['data_archive_setting.storage_mode'],
    db_type: defaults['data_archive_setting.db_type'],
    dsn: defaults['data_archive_setting.dsn'],
    table_name: defaults['data_archive_setting.table_name'],
    sync_hour: defaults['data_archive_setting.sync_hour'],
    max_body_bytes: defaults['data_archive_setting.max_body_bytes'],
    max_local_mb: defaults['data_archive_setting.max_local_mb'],
    max_file_mb: defaults['data_archive_setting.max_file_mb'],
  },
})

const normalizeDefaults = (
  defaults: DataArchiveSettingsSectionProps['defaultValues']
): FlatDataArchiveDefaults => ({
  'data_archive_setting.enabled': defaults['data_archive_setting.enabled'],
  'data_archive_setting.storage_mode':
    defaults['data_archive_setting.storage_mode'],
  'data_archive_setting.db_type': defaults['data_archive_setting.db_type'],
  'data_archive_setting.dsn': defaults['data_archive_setting.dsn'],
  'data_archive_setting.table_name':
    defaults['data_archive_setting.table_name'],
  'data_archive_setting.sync_hour': defaults['data_archive_setting.sync_hour'],
  'data_archive_setting.max_body_bytes':
    defaults['data_archive_setting.max_body_bytes'],
  'data_archive_setting.max_local_mb':
    defaults['data_archive_setting.max_local_mb'],
  'data_archive_setting.max_file_mb':
    defaults['data_archive_setting.max_file_mb'],
})

const normalizeFormValues = (
  values: DataArchiveFormValues
): FlatDataArchiveDefaults => ({
  'data_archive_setting.enabled': values.data_archive_setting.enabled,
  'data_archive_setting.storage_mode':
    values.data_archive_setting.storage_mode,
  'data_archive_setting.db_type': values.data_archive_setting.db_type,
  'data_archive_setting.dsn': values.data_archive_setting.dsn,
  'data_archive_setting.table_name': values.data_archive_setting.table_name,
  'data_archive_setting.sync_hour': values.data_archive_setting.sync_hour,
  'data_archive_setting.max_body_bytes':
    values.data_archive_setting.max_body_bytes,
  'data_archive_setting.max_local_mb':
    values.data_archive_setting.max_local_mb,
  'data_archive_setting.max_file_mb':
    values.data_archive_setting.max_file_mb,
})

function formatBytes(bytes: number, decimals = 2): string {
  if (!bytes || Number.isNaN(bytes)) return '0 Bytes'
  if (bytes === 0) return '0 Bytes'
  if (bytes < 0) return `-${formatBytes(-bytes, decimals)}`
  const k = 1024
  const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(Math.abs(bytes)) / Math.log(k))
  if (i < 0 || i >= sizes.length) return `${bytes} Bytes`
  return `${Number.parseFloat((bytes / Math.pow(k, i)).toFixed(decimals))} ${
    sizes[i]
  }`
}

export function DataArchiveSettingsSection({
  defaultValues,
}: DataArchiveSettingsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const baselineRef = useRef<FlatDataArchiveDefaults>(
    normalizeDefaults(defaultValues)
  )
  const baselineSerializedRef = useRef<string>(
    JSON.stringify(normalizeDefaults(defaultValues))
  )

  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )

  const form = useForm<DataArchiveFormInput, unknown, DataArchiveFormValues>({
    resolver: zodResolver(dataArchiveSchema),
    defaultValues: formDefaults,
  })

  useResetForm(form, formDefaults)

  useEffect(() => {
    const normalized = normalizeDefaults(defaultValues)
    const serialized = JSON.stringify(normalized)
    if (serialized === baselineSerializedRef.current) return
    baselineRef.current = normalized
    baselineSerializedRef.current = serialized
  }, [defaultValues])

  const archiveEnabled = form.watch('data_archive_setting.enabled')
  const storageMode = form.watch('data_archive_setting.storage_mode')
  const isDatabaseMode = storageMode === 'database'

  const onSubmit = async (values: DataArchiveFormValues) => {
    const normalized = normalizeFormValues(values)
    const updates = (
      Object.keys(normalized) as Array<keyof FlatDataArchiveDefaults>
    ).filter((key) => normalized[key] !== baselineRef.current[key])

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const key of updates) {
      await updateOption.mutateAsync({
        key,
        value: normalized[key],
      })
    }

    baselineRef.current = normalized
    baselineSerializedRef.current = JSON.stringify(normalized)
    toast.success(t('Data archive settings saved'))
  }

  // ---------------------------------------------------------------------------
  // Archive data management (list / download / delete)
  // ---------------------------------------------------------------------------
  const [archiveStatus, setArchiveStatus] = useState<ArchiveStatusData | null>(
    null
  )
  const [statusLoading, setStatusLoading] = useState(false)
  const [rowBusy, setRowBusy] = useState<string | null>(null)

  const fetchArchiveStatus = useCallback(async () => {
    setStatusLoading(true)
    try {
      const res = await api.get('/api/data_archive/status')
      setArchiveStatus(requireServerSuccess(res.data).data)
    } catch (error) {
      handleServerError(error)
    } finally {
      setStatusLoading(false)
    }
  }, [])

  useEffect(() => {
    if (archiveEnabled) {
      fetchArchiveStatus()
    }
  }, [archiveEnabled, fetchArchiveStatus])

  const downloadArchiveFile = async (fileName?: string) => {
    try {
      const params = new URLSearchParams()
      if (fileName) params.set('file', fileName)
      const res = await api.get(
        `/api/data_archive/download?${params.toString()}`,
        { responseType: 'blob' }
      )
      const url = URL.createObjectURL(res.data as Blob)
      const link = document.createElement('a')
      link.href = url
      link.download =
        fileName && fileName.endsWith('.jsonl')
          ? `${fileName.slice(0, -'.jsonl'.length)}.json`
          : (fileName ?? 'request-archive-export.json')
      document.body.appendChild(link)
      link.click()
      link.remove()
      URL.revokeObjectURL(url)
    } catch (error) {
      handleServerError(error)
    }
  }

  const deleteArchiveFile = async (fileName: string) => {
    setRowBusy(fileName)
    try {
      const res = await api.delete(
        `/api/data_archive/file?file=${encodeURIComponent(fileName)}`
      )
      requireServerSuccess(res.data)
      toast.success(t('Archive entry deleted'))
      fetchArchiveStatus()
    } catch (error) {
      handleServerError(error)
    } finally {
      setRowBusy(null)
    }
  }

  const clearArchiveAll = async () => {
    setRowBusy('all')
    try {
      const res = await api.delete('/api/data_archive/all')
      const data = requireServerSuccess(res.data).data as
        | { deleted?: number }
        | null
      toast.success(
        data?.deleted != null
          ? t('Deleted {{count}} archived entries', { count: data.deleted })
          : t('All archived entries deleted')
      )
      fetchArchiveStatus()
    } catch (error) {
      handleServerError(error)
    } finally {
      setRowBusy(null)
    }
  }

  return (
    <SettingsSection title={t('Request Data Archive')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              "Archive every request's input and output. Local mode keeps JSON volume files on this server; database mode uploads them to the archive database at the configured off-peak hour every night."
            )}
          </p>

          <FormField
            control={form.control}
            name='data_archive_setting.enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable request data archive')}</FormLabel>
                  <FormDescription>
                    {t(
                      'When enabled, relay request bodies and response bodies are retained for auditing.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <SettingsFormGrid>
            <FormField
              control={form.control}
              name='data_archive_setting.storage_mode'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Storage Mode')}</FormLabel>
                  <Select
                    items={[
                      { value: 'local', label: t('Local JSON files') },
                      { value: 'database', label: t('External database') },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                    disabled={!archiveEnabled}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent alignItemWithTrigger={false}>
                      <SelectGroup>
                        <SelectItem value='local'>
                          {t('Local JSON files')}
                        </SelectItem>
                        <SelectItem value='database'>
                          {t('External database')}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    {t(
                      'Local mode is the default: entries stay on this server as JSON files and never leave the machine.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='data_archive_setting.max_file_mb'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('JSON volume size (MB)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      max={1024}
                      step={1}
                      {...safeNumberFieldProps(field)}
                      disabled={!archiveEnabled || isDatabaseMode}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Each local JSON file rolls over after reaching this size (default 50 MB).'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='data_archive_setting.max_body_bytes'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Max stored body size (bytes)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1024}
                      max={524288}
                      step={1024}
                      {...safeNumberFieldProps(field)}
                      disabled={!archiveEnabled}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Request and response bodies are truncated to this size per request (max 512 KB).'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='data_archive_setting.max_local_mb'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Local staging limit (MB)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      {...safeNumberFieldProps(field)}
                      disabled={!archiveEnabled}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'When the local staging directory exceeds this size, new data is discarded until entries are deleted or uploaded.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </SettingsFormGrid>

          {isDatabaseMode && (
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='data_archive_setting.db_type'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Archive database type')}</FormLabel>
                    <Select
                      items={[
                        { value: 'mysql', label: 'MySQL' },
                        { value: 'postgres', label: 'PostgreSQL' },
                        { value: 'sqlite', label: 'SQLite' },
                      ]}
                      value={field.value}
                      onValueChange={field.onChange}
                      disabled={!archiveEnabled}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='mysql'>MySQL</SelectItem>
                          <SelectItem value='postgres'>PostgreSQL</SelectItem>
                          <SelectItem value='sqlite'>SQLite</SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='data_archive_setting.dsn'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Archive database DSN')}</FormLabel>
                    <FormControl>
                      <Input
                        type='text'
                        placeholder='user:password@tcp(host:3306)/archive_db'
                        {...field}
                        disabled={!archiveEnabled}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'For SQLite, enter the database file path, e.g. data/archive.db.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='data_archive_setting.table_name'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Archive table name')}</FormLabel>
                    <FormControl>
                      <Input
                        type='text'
                        {...field}
                        disabled={!archiveEnabled}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'The table is created automatically if it does not exist.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='data_archive_setting.sync_hour'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Upload hour')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={23}
                        step={1}
                        {...safeNumberFieldProps(field)}
                        disabled={!archiveEnabled}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Local hour (0-23) the daily upload runs at. Use a low-traffic hour such as 3 AM.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          )}
        </SettingsForm>
      </Form>

      {archiveEnabled && (
        <div
          role='group'
          aria-label={t('Archive data management')}
          className='flex min-w-0 flex-col gap-4'
        >
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <h4 className='font-medium'>{t('Archive data management')}</h4>
            <div className='flex items-center gap-2'>
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={fetchArchiveStatus}
                disabled={statusLoading}
                className='gap-1.5'
              >
                <RefreshCw className='size-3.5' />
                {t('Refresh')}
              </Button>
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() => downloadArchiveFile()}
                disabled={statusLoading}
                className='gap-1.5'
              >
                <Download className='size-3.5' />
                {isDatabaseMode
                  ? t('Export entries (JSON)')
                  : t('Download latest volume')}
              </Button>
              <AlertDialog>
                <AlertDialogTrigger
                  render={
                    <Button
                      type='button'
                      variant='destructive'
                      size='sm'
                      disabled={statusLoading || rowBusy !== null}
                      className='gap-1.5'
                    />
                  }
                >
                  <Trash2 className='size-3.5' />
                  {t('Delete all entries')}
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>
                      {t('Delete all archived entries?')}
                    </AlertDialogTitle>
                    <AlertDialogDescription>
                      {isDatabaseMode
                        ? t(
                            'Every row in the archive database table will be permanently deleted.'
                          )
                        : t(
                            'Every local JSON volume will be permanently deleted.'
                          )}
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>{t('Cancel')}</AlertDialogCancel>
                    <AlertDialogAction
                      variant='destructive'
                      onClick={clearArchiveAll}
                    >
                      {t('Confirm')}
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            </div>
          </div>

          {!isDatabaseMode ? (
            <div className='rounded-lg border p-4'>
              {statusLoading && !archiveStatus ? (
                <p className='text-muted-foreground text-sm'>
                  {t('Loading...')}
                </p>
              ) : archiveStatus && archiveStatus.files.length > 0 ? (
                <div className='flex flex-col gap-3'>
                  <div className='text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-sm'>
                    <span>
                      {t('Total')}: {formatBytes(archiveStatus.total_bytes)}
                    </span>
                    <span>
                      {t('Files')}: {archiveStatus.files.length}
                    </span>
                    <span>
                      {t('Entries')}:{' '}
                      {archiveStatus.files.reduce(
                        (sum, file) => sum + file.entries,
                        0
                      )}
                    </span>
                  </div>
                  <div className='flex flex-col divide-y'>
                    {archiveStatus.files
                      .slice()
                      .reverse()
                      .map((file) => (
                        <div
                          key={file.name}
                          className='flex flex-wrap items-center justify-between gap-2 py-2'
                        >
                          <div className='min-w-0'>
                            <p className='truncate font-mono text-xs'>
                              {file.name}
                            </p>
                            <p className='text-muted-foreground text-xs'>
                              {formatBytes(file.size_bytes)} · {file.entries}{' '}
                              {t('entries')} ·{' '}
                              {new Date(file.mod_time * 1000).toLocaleString()}
                            </p>
                          </div>
                          <div className='flex shrink-0 items-center gap-1.5'>
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              className='gap-1.5'
                              onClick={() => downloadArchiveFile(file.name)}
                            >
                              <Download className='size-3.5' />
                              {t('Download')}
                            </Button>
                            <AlertDialog>
                              <AlertDialogTrigger
                                render={
                                  <Button
                                    type='button'
                                    variant='outline'
                                    size='sm'
                                    className='gap-1.5 text-destructive'
                                    disabled={rowBusy !== null}
                                  />
                                }
                              >
                                <Trash2 className='size-3.5' />
                                {t('Delete')}
                              </AlertDialogTrigger>
                              <AlertDialogContent>
                                <AlertDialogHeader>
                                  <AlertDialogTitle>
                                    {t('Delete archive file {{name}}?', {
                                      name: file.name,
                                    })}
                                  </AlertDialogTitle>
                                  <AlertDialogDescription>
                                    {t(
                                      'This JSON volume ({{count}} entries, {{size}}) will be permanently deleted.',
                                      {
                                        count: file.entries,
                                        size: formatBytes(file.size_bytes),
                                      }
                                    )}
                                  </AlertDialogDescription>
                                </AlertDialogHeader>
                                <AlertDialogFooter>
                                  <AlertDialogCancel>
                                    {t('Cancel')}
                                  </AlertDialogCancel>
                                  <AlertDialogAction
                                    variant='destructive'
                                    onClick={() => deleteArchiveFile(file.name)}
                                  >
                                    {t('Confirm')}
                                  </AlertDialogAction>
                                </AlertDialogFooter>
                              </AlertDialogContent>
                            </AlertDialog>
                          </div>
                        </div>
                      ))}
                  </div>
                </div>
              ) : (
                <Alert>
                  <AlertDescription>
                    {t(
                      'No archived entries yet. New requests will be stored here as JSON volumes.'
                    )}
                  </AlertDescription>
                </Alert>
              )}
            </div>
          ) : (
            <div className='rounded-lg border p-4'>
              {archiveStatus?.database ? (
                <div className='text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-sm'>
                  <span>
                    {t('Archive table')}:{' '}
                    <span className='font-mono text-xs'>
                      {archiveStatus.database.table}
                    </span>
                  </span>
                  <span>
                    {t('Entries')}: {archiveStatus.database.entries}
                  </span>
                </div>
              ) : (
                <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
              )}
            </div>
          )}
        </div>
      )}
    </SettingsSection>
  )
}
