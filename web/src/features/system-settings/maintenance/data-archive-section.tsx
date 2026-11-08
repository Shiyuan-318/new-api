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
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

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
    db_type: z.enum(['mysql', 'postgres', 'sqlite']),
    dsn: z.string(),
    table_name: z.string(),
    sync_hour: z.coerce.number().int().min(0).max(23),
    max_body_bytes: z.coerce.number().int().min(1024).max(524288),
    max_local_mb: z.coerce.number().int().min(1),
  }),
})

type DataArchiveFormInput = z.input<typeof dataArchiveSchema>
type DataArchiveFormValues = z.output<typeof dataArchiveSchema>

type FlatDataArchiveDefaults = {
  'data_archive_setting.enabled': boolean
  'data_archive_setting.db_type': 'mysql' | 'postgres' | 'sqlite'
  'data_archive_setting.dsn': string
  'data_archive_setting.table_name': string
  'data_archive_setting.sync_hour': number
  'data_archive_setting.max_body_bytes': number
  'data_archive_setting.max_local_mb': number
}

type DataArchiveSettingsSectionProps = {
  defaultValues: FlatDataArchiveDefaults
}

const buildFormDefaults = (
  defaults: DataArchiveSettingsSectionProps['defaultValues']
): DataArchiveFormInput => ({
  data_archive_setting: {
    enabled: defaults['data_archive_setting.enabled'],
    db_type: defaults['data_archive_setting.db_type'],
    dsn: defaults['data_archive_setting.dsn'],
    table_name: defaults['data_archive_setting.table_name'],
    sync_hour: defaults['data_archive_setting.sync_hour'],
    max_body_bytes: defaults['data_archive_setting.max_body_bytes'],
    max_local_mb: defaults['data_archive_setting.max_local_mb'],
  },
})

const normalizeDefaults = (
  defaults: DataArchiveSettingsSectionProps['defaultValues']
): FlatDataArchiveDefaults => ({
  'data_archive_setting.enabled': defaults['data_archive_setting.enabled'],
  'data_archive_setting.db_type': defaults['data_archive_setting.db_type'],
  'data_archive_setting.dsn': defaults['data_archive_setting.dsn'],
  'data_archive_setting.table_name':
    defaults['data_archive_setting.table_name'],
  'data_archive_setting.sync_hour': defaults['data_archive_setting.sync_hour'],
  'data_archive_setting.max_body_bytes':
    defaults['data_archive_setting.max_body_bytes'],
  'data_archive_setting.max_local_mb':
    defaults['data_archive_setting.max_local_mb'],
})

const normalizeFormValues = (
  values: DataArchiveFormValues
): FlatDataArchiveDefaults => ({
  'data_archive_setting.enabled': values.data_archive_setting.enabled,
  'data_archive_setting.db_type': values.data_archive_setting.db_type,
  'data_archive_setting.dsn': values.data_archive_setting.dsn,
  'data_archive_setting.table_name': values.data_archive_setting.table_name,
  'data_archive_setting.sync_hour': values.data_archive_setting.sync_hour,
  'data_archive_setting.max_body_bytes':
    values.data_archive_setting.max_body_bytes,
  'data_archive_setting.max_local_mb':
    values.data_archive_setting.max_local_mb,
})

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
              'Archive every request\'s input and output: data is staged locally first, then uploaded to the archive database at the configured off-peak hour every night.'
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
                    <Input type='text' {...field} disabled={!archiveEnabled} />
                  </FormControl>
                  <FormDescription>
                    {t('The table is created automatically if it does not exist.')}
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
                      'When the local staging directory exceeds this size, new data is discarded until the next upload.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </SettingsFormGrid>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
