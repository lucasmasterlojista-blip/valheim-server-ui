import { useState } from 'react'
import { Alert, Button, Checkbox, Group, List, SegmentedControl, Stack, Text } from '@mantine/core'
import { Dropzone } from '@mantine/dropzone'
import { IconUpload, IconX } from '@tabler/icons-react'
import { ApiError } from '../../../api/client'
import { SectionCard } from '../../../ui'
import { notifyWarning } from '../../../lib/notify'
import { usePreviewWorldImport, useUploadWorlds } from './useWorlds'

type UploadFormat = 'zip' | 'legacy'

const LEGACY_SUFFIX = /\.(db|fwl)$/i

function extension(file: File) {
  return file.name.slice(file.name.lastIndexOf('.')).toLowerCase()
}

export function WorldUploadCard({ id }: { id: string }) {
  const [format, setFormat] = useState<UploadFormat>('zip')
  const [files, setFiles] = useState<File[]>([])
  const [overwrite, setOverwrite] = useState(false)
  const upload = useUploadWorlds(id)
  const preview = usePreviewWorldImport(id)
  const pending = upload.isPending || preview.isPending
  const details = preview.data
  const previewError = preview.error instanceof ApiError
    ? preview.error.fields[0]?.message ?? preview.error.message
    : preview.error?.message
  const zip = format === 'zip'
  const matchingPair = files.length === 2 &&
    files[0].name.replace(LEGACY_SUFFIX, '') === files[1].name.replace(LEGACY_SUFFIX, '')
  const hasFiles = zip ? files.length === 1 : matchingPair
  const canUpload = hasFiles && !pending &&
    !details?.requires_stop && (!details?.exists || overwrite)

  function clearFiles() {
    setFiles([])
    setOverwrite(false)
    preview.reset()
  }

  function changeFormat(next: UploadFormat) {
    setFormat(next)
    clearFiles()
  }

  function warn(message: string) {
    notifyWarning(message, 'Invalid world selection')
  }

  function handleDrop(dropped: File[]) {
    if (dropped.length === 0) return
    if (dropped.some((file) => extension(file) === '.rar')) {
      warn('Extract the RAR archive and upload a ZIP of the complete world folder.')
      return
    }
    if (zip) {
      if (dropped.length !== 1 || extension(dropped[0]) !== '.zip') {
        warn('Choose one ZIP archive containing the complete world folder.')
        return
      }
      preview.reset()
      setOverwrite(false)
      setFiles(dropped)
      return
    }

    if (dropped.some((file) => !LEGACY_SUFFIX.test(file.name)) ||
        new Set(dropped.map(extension)).size !== dropped.length) {
      warn('Choose one .db file and one .fwl file for a legacy world.')
      return
    }
    preview.reset()
    setOverwrite(false)
    setFiles((previous) => [
      ...previous.filter((file) => !dropped.some((item) => extension(item) === extension(file))),
      ...dropped,
    ])
  }

  function submit() {
    if (!canUpload) return
    upload.mutate(
      { files, overwrite },
      {
        onSuccess: clearFiles,
      },
    )
  }

  return (
    <SectionCard title="Upload a world">
      <Stack gap="sm">
        <SegmentedControl<UploadFormat>
          aria-label="World upload format"
          value={format}
          onChange={changeFormat}
          disabled={pending}
          data={[
            { label: 'ZIP archive', value: 'zip' },
            { label: 'Legacy files', value: 'legacy' },
          ]}
        />
        <Dropzone
          onDrop={handleDrop}
          onReject={zip ? () => warn('Choose one ZIP archive at a time.') : undefined}
          multiple={!zip}
          disabled={pending}
          inputProps={{ 'aria-label': zip ? 'Choose a world ZIP archive' : 'Choose legacy world files' }}
        >
          <Group justify="center" gap="md" mih={100} style={{ pointerEvents: 'none' }}>
            <Dropzone.Accept>
              <IconUpload size={32} />
            </Dropzone.Accept>
            <Dropzone.Reject>
              <IconX size={32} />
            </Dropzone.Reject>
            <Dropzone.Idle>
              <IconUpload size={32} />
            </Dropzone.Idle>
            <div>
              {zip ? (
                <>
                  <Text size="sm">Choose a ZIP of the complete Valheim 1.0 world folder</Text>
                  <Text size="xs" c="dimmed" mt={4}>
                    Keep the <code>_main</code> files and all <code>.chunk</code> files together.
                  </Text>
                </>
              ) : (
                <Text size="sm">
                  Choose the matching <code>.db</code> and <code>.fwl</code> files from a pre-1.0 save
                </Text>
              )}
            </div>
          </Group>
        </Dropzone>

        {files.length > 0 && (
          <List size="sm" spacing={2}>
            {files.map((file) => <List.Item key={file.name}>{file.name}</List.Item>)}
          </List>
        )}
        {!zip && files.length > 0 && !matchingPair && (
          <Text size="sm" c="dimmed" role="status">
            {files.length === 1
              ? 'Select both files to upload this world.'
              : 'The .db and .fwl files must have the same world name.'}
          </Text>
        )}

        {previewError && <Alert color="red" title="Could not preview world" role="alert">{previewError}</Alert>}
        {details && (
          <Alert color={details.exists || details.requires_stop ? 'yellow' : 'blue'} title={details.name} role="status">
            <Text size="sm">{details.layout === 'directory' ? 'Valheim 1.0 world folder' : 'Legacy save'} · {details.file_count} files</Text>
            {details.exists && <Text size="sm">A world with this name already exists.</Text>}
            {details.requires_stop && <Text size="sm">Stop the instance before importing this world, then preview again.</Text>}
          </Alert>
        )}

        <Checkbox
          label="Overwrite existing world files with the same name"
          checked={overwrite}
          onChange={(e) => setOverwrite(e.currentTarget.checked)}
          disabled={pending}
        />

        <Group justify="flex-end">
          {files.length > 0 && (
            <Button variant="default" onClick={clearFiles} disabled={pending}>
              Clear
            </Button>
          )}
          <Button variant="light" onClick={() => preview.mutate(files)} disabled={!hasFiles || pending} loading={preview.isPending}>
            Preview
          </Button>
          <Button onClick={submit} disabled={!canUpload} loading={upload.isPending}>
            Upload
          </Button>
        </Group>
      </Stack>
    </SectionCard>
  )
}
