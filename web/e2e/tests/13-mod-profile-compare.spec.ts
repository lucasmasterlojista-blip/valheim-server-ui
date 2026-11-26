import { expect, test, type FileChooser, type Page } from '@playwright/test'
import { fileURLToPath } from 'node:url'
import { createInstance, login } from './helpers'

const profile = fileURLToPath(new URL('../../../testdata/mod-profile/Team.r2z', import.meta.url))

async function chooseProfile(page: Page, file: Parameters<FileChooser['setFiles']>[0]) {
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Client profile archive', exact: true }).click()
  await (await chooser).setFiles(file)
}

test('compares a profile export in the Mods tab without installing its mods', async ({ page }) => {
  await login(page)
  const instance = 'mod-profile-compare'
  await createInstance(page, {
    id: instance, name: 'Mod profile comparison', serverName: 'Mod profile comparison',
    world: 'Original', port: 2470,
  })
  await page.goto('/instances/' + instance + '/mods')
  const compare = page.getByRole('button', { name: 'Compare profile', exact: true })
  await expect(compare).toBeDisabled()

  await chooseProfile(page, { name: 'invalid.r2z', mimeType: 'application/zip', buffer: Buffer.from('not a profile') })
  await compare.click()
  await expect(page.getByText('file is not a valid mod profile ZIP')).toBeVisible()

  await chooseProfile(page, profile)
  await compare.click()
  await expect(page.getByRole('status').filter({ hasText: 'Synthetic Team' })).toContainText('0 matching · 1 with differences')
  const differences = page.getByRole('table', { name: 'Client profile differences' })
  await expect(differences.getByRole('cell', { name: 'DemoTeam-ExampleMod', exact: true })).toBeVisible()
  await expect(differences.getByText('1.2.3 · Enabled')).toBeVisible()
  await differences.getByText('Missing on server').scrollIntoViewIfNeeded()
  await expect(differences.getByText('Missing on server')).toBeInViewport()
  const result = await page.request.get('/api/v1/instances/' + instance + '/mods')
  expect((await result.json()).mods).toEqual([])

  await page.getByRole('button', { name: 'Clear client profile archive' }).click()
  await expect(differences).toHaveCount(0)
  await expect(compare).toBeDisabled()
})
