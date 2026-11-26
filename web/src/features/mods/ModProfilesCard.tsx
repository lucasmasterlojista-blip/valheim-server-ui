import { useState } from 'react'
import { Alert, Badge, Button, Checkbox, FileInput, Group, Modal, SegmentedControl, Stack, Text, TextInput } from '@mantine/core'
import { useDisclosure } from '@mantine/hooks'
import { IconDownload, IconFileZip, IconUpload } from '@tabler/icons-react'
import { ApiError } from '../../api/client'
import type { Mod, Schemas } from '../../api/types'
import { Dash, DataTable, type DataTableColumn, SectionCard } from '../../ui'
import { useJobDrawer } from '../jobs'
import { useCompareModProfile, useExportModProfile, useInstallProfileSelection } from './useMods'

type Difference = Schemas['ModProfileDifference']
type ModEntry = Schemas['ModProfileEntry']
type ProfilePackage = Schemas['ModProfileInstallRequest']['packages'][number]
type ProfileSource = 'file' | 'code'
const MAX_BATCH = 128

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

const comparisonColumns: DataTableColumn<Difference>[] = [
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

export function ModProfilesCard({
  id,
  canImport,
  bepinexInstalled,
  installedMods,
}: {
  id: string
  canImport: boolean
  bepinexInstalled: boolean
  installedMods: Mod[]
}) {
  const exportProfile = useExportModProfile(id)
  const [opened, { open, close }] = useDisclosure(false)

  return (
    <>
      <SectionCard title="Mod profiles">
        <Stack gap="sm">
          <Text size="sm" c="dimmed">
            {canImport
              ? 'Preview a Thunderstore profile before installing selected mods, or export the server profile for players.'
              : 'Download the server profile to import it in your mod manager.'}
          </Text>
          <Group gap="xs">
            {canImport && <Button size="xs" leftSection={<IconUpload size={14} />} onClick={open}>Import profile</Button>}
            <Button
              size="xs"
              variant="default"
              leftSection={<IconDownload size={14} />}
              loading={exportProfile.isPending}
              onClick={() => exportProfile.mutate()}
            >
              Export profile (.r2z)
            </Button>
          </Group>
        </Stack>
      </SectionCard>

      {opened && canImport && (
        <ImportModProfile
          id={id}
          onClose={close}
          bepinexInstalled={bepinexInstalled}
          installedMods={installedMods}
        />
      )}
    </>
  )
}

function ImportModProfile({
  id,
  onClose,
  bepinexInstalled,
  installedMods,
}: {
  id: string
  onClose: () => void
  bepinexInstalled: boolean
  installedMods: Mod[]
}) {
  const [source, setSource] = useState<ProfileSource>('file')
  const [file, setFile] = useState<File | null>(null)
  const [code, setCode] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const comparison = useCompareModProfile(id)
  const install = useInstallProfileSelection(id)
  const { openJob } = useJobDrawer()
  const { data, error } = comparison
  const pending = comparison.isPending || install.isPending
  const fieldErrors = error instanceof ApiError ? error.fieldErrors() : {}
  const installedNames = new Set(installedMods.map((mod) => (mod.owner + '-' + mod.name).toLowerCase()))
  const installable = new Map<string, ProfilePackage>()
  if (bepinexInstalled && data) {
    for (const diff of data.differences) {
      if (!diff.profile?.enabled || !diff.reasons.includes('missing_on_server') || installedNames.has(diff.name.toLowerCase())) {
        continue
      }
      const separator = diff.name.indexOf('-')
      if (separator > 0) {
        installable.set(diff.name, {
          owner: diff.name.slice(0, separator),
          name: diff.name.slice(separator + 1),
          version: diff.profile.version,
        })
      }
    }
  }
  const selectedPackages = [...installable].filter(([name]) => selected.includes(name)).map(([, pkg]) => pkg)
  const maxSelected = Math.min(installable.size, MAX_BATCH)
  const input = source === 'file' ? file : code.trim()

  function resetPreview() {
    setSelected([])
    comparison.reset()
  }

  function toggleMod(name: string) {
    setSelected((current) => current.includes(name) ? current.filter((item) => item !== name) : [...current, name])
  }

  function installSelected() {
    if (selectedPackages.length === 0) return
    install.mutate(
      { packages: selectedPackages },
      { onSuccess: ({ job }) => {
        onClose()
        openJob(job.id)
      } },
    )
  }

  const columns: DataTableColumn<Difference>[] = installable.size === 0 ? comparisonColumns : [
    ...comparisonColumns,
    {
      key: 'install',
      header: 'Install',
      render: ({ name }) => installable.has(name) ? (
        <Checkbox
          aria-label={`Install ${name}`}
          checked={selected.includes(name)}
          disabled={pending || (!selected.includes(name) && selectedPackages.length >= MAX_BATCH)}
          onChange={() => toggleMod(name)}
        />
      ) : <Dash />,
    },
  ]

  return (
    <Modal opened onClose={onClose} title="Import mod profile" size="xl" closeOnEscape={!install.isPending} closeOnClickOutside={!install.isPending}>
      <Stack gap="sm">
        <SegmentedControl<ProfileSource>
          aria-label="Profile source"
          value={source}
          onChange={(next) => {
            setSource(next)
            resetPreview()
          }}
          disabled={pending}
          data={[
            { value: 'file', label: 'Profile file' },
            { value: 'code', label: 'Thunderstore code' },
          ]}
        />
        <form onSubmit={(event) => {
          event.preventDefault()
          if (!input || pending) return
          setSelected([])
          comparison.mutate(input)
        }}>
          <Group align="end">
            {source === 'file' ? (
              <FileInput
                label="Client profile archive"
                placeholder="Choose an .r2z or .zip file"
                accept=".r2z,.zip"
                leftSection={<IconFileZip size={16} />}
                clearable
                clearButtonProps={{ 'aria-label': 'Clear client profile archive', disabled: pending }}
                error={fieldErrors.file}
                value={file}
                onChange={(next) => {
                  setFile(next)
                  resetPreview()
                }}
                disabled={pending}
                flex="1 1 230px"
              />
            ) : (
              <TextInput
                label="Thunderstore profile code"
                placeholder="Paste the code from Share"
                error={fieldErrors.code}
                value={code}
                onChange={(event) => {
                  setCode(event.currentTarget.value)
                  resetPreview()
                }}
                disabled={pending}
                maxLength={80}
                flex="1 1 230px"
              />
            )}
            <Button type="submit" disabled={!input || install.isPending} loading={comparison.isPending}>Preview profile</Button>
          </Group>
        </form>

        {data && (
          <>
            <Text size="sm" role="status">
              Profile <strong>{data.profile_name}</strong> · {data.matched} matching · {data.differences.length} with differences
            </Text>
            {installable.size > 0 && (
              <>
                <Text size="sm" c="dimmed">
                  Select mods that can run on a dedicated server. The install may add dependencies.
                </Text>
                <Checkbox
                  label={installable.size > MAX_BATCH ? `Select the first ${MAX_BATCH} installable mods` : 'Select all installable mods'}
                  checked={selectedPackages.length === maxSelected}
                  indeterminate={selectedPackages.length > 0 && selectedPackages.length < maxSelected}
                  disabled={pending}
                  onChange={(event) => setSelected(event.currentTarget.checked ? [...installable.keys()].slice(0, MAX_BATCH) : [])}
                />
              </>
            )}
            {data.differences.length > 0 ? (
              <DataTable
                aria-label="Client profile differences"
                columns={columns}
                rows={data.differences}
                rowKey={(diff) => diff.name}
                minWidth={installable.size ? 640 : 560}
              />
            ) : (
              <Text size="sm" c="dimmed">No differences among compared Thunderstore mods.</Text>
            )}
            {!bepinexInstalled && (
              <Alert color="frost" title="BepInEx required">Install BepInEx to add mods from the profile.</Alert>
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
            {installable.size > 0 && (
              <Group justify="flex-end">
                <Button onClick={installSelected} disabled={selectedPackages.length === 0 || comparison.isPending} loading={install.isPending}>
                  Install selected ({selectedPackages.length})
                </Button>
              </Group>
            )}
          </>
        )}
      </Stack>
    </Modal>
  )
}
