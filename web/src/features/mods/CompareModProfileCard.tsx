import { useState } from 'react'
import { Alert, Badge, Button, FileInput, Group, Stack, Text } from '@mantine/core'
import { IconFileZip } from '@tabler/icons-react'
import { ApiError } from '../../api/client'
import type { Schemas } from '../../api/types'
import { Dash, DataTable, type DataTableColumn, SectionCard } from '../../ui'
import { useCompareModProfile } from './useMods'

type Difference = Schemas['ModProfileDifference']
type ModEntry = Schemas['ModProfileEntry']

const reasonLabels: Record<Difference['reasons'][number], string> = {
  missing_on_server: 'Missing on server',
  only_on_server: 'Only on server',
  version: 'Different version',
  enabled: 'Different enabled state',
}

function VersionAndState({ entry }: { entry?: ModEntry }) {
  if (!entry) return <Dash />
  return <Text size="sm">{entry.version} · {entry.enabled ? 'Enabled' : 'Disabled'}</Text>
}

const columns: DataTableColumn<Difference>[] = [
  { key: 'name', header: 'Mod', render: ({ name }) => name },
  { key: 'profile', header: 'Client profile', render: ({ profile }) => <VersionAndState entry={profile} /> },
  { key: 'server', header: 'Server', render: ({ server }) => <VersionAndState entry={server} /> },
  {
    key: 'reasons',
    header: 'Differences',
    render: ({ reasons }) => (
      <Group gap={4}>
        {reasons.map((reason) => <Badge key={reason} variant="light" color="straw">{reasonLabels[reason]}</Badge>)}
      </Group>
    ),
  },
]

export function CompareModProfileCard({ id }: { id: string }) {
  const [file, setFile] = useState<File | null>(null)
  const comparison = useCompareModProfile(id)
  const { data, error, isPending } = comparison

  function chooseFile(next: File | null) {
    setFile(next)
    comparison.reset()
  }

  return (
    <SectionCard
      title="Compare client profile"
      description="Compare the Thunderstore mod versions and enabled states in an r2modman or Gale file export."
    >
      <Stack gap="sm">
        <Group align="end">
          <FileInput
            label="Client profile archive"
            placeholder="Choose an .r2z or .zip file"
            accept=".r2z,.zip"
            leftSection={<IconFileZip size={16} />}
            clearable
            clearButtonProps={{ 'aria-label': 'Clear client profile archive', disabled: isPending }}
            error={error instanceof ApiError ? error.fieldErrors().file : undefined}
            value={file}
            onChange={chooseFile}
            disabled={isPending}
            flex="1 1 250px"
          />
          <Button
            disabled={!file}
            loading={isPending}
            onClick={() => file && comparison.mutate(file)}
          >
            Compare profile
          </Button>
        </Group>
        {data && (
          <>
            <Text size="sm" role="status">
              Profile <strong>{data.profile_name}</strong> · {data.matched} matching · {data.differences.length} with differences
            </Text>
            {data.differences.length > 0 ? (
              <DataTable
                aria-label="Client profile differences"
                columns={columns}
                rows={data.differences}
                rowKey={(diff) => diff.name}
                minWidth={560}
              />
            ) : (
              <Text size="sm" c="dimmed">No differences among compared Thunderstore mods.</Text>
            )}
            {(data.skipped_profile_mods.length > 0 || data.skipped_server_mods.length > 0) && (
              <Alert color="straw" title="Mods excluded from comparison">
                <Stack gap={4}>
                  {data.skipped_profile_mods.length > 0 && (
                    <Text size="sm">Skipped in profile · {data.skipped_profile_mods.join(', ')}</Text>
                  )}
                  {data.skipped_server_mods.length > 0 && (
                    <Text size="sm">Skipped on server · {data.skipped_server_mods.join(', ')}</Text>
                  )}
                </Stack>
              </Alert>
            )}
          </>
        )}
      </Stack>
    </SectionCard>
  )
}
