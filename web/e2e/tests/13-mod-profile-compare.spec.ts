import { expect, test, type FileChooser, type Page } from '@playwright/test'
import { fileURLToPath } from 'node:url'
import { createInstance, login } from './helpers'

const profile = fileURLToPath(new URL('../../../testdata/mod-profile/Team.r2z', import.meta.url))

async function chooseProfile(page: Page, file: Parameters<FileChooser['setFiles']>[0]) {
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Client profile archive', exact: true }).click()
  await (await chooser).setFiles(file)
}

test('exports the instance profile and previews a profile file before any installation', async ({ page }) => {
  await login(page)
  const instance = 'mod-profile-compare'
  await createInstance(page, {
    id: instance, name: 'Mod profile comparison', serverName: 'Mod profile comparison',
    world: 'Original', port: 2470,
  })
  await page.goto('/instances/' + instance + '/mods')
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'Export profile (.r2z)', exact: true }).click(),
  ])
  expect(download.suggestedFilename()).toBe(instance + '-mods.r2z')

  await page.getByRole('button', { name: 'Import profile', exact: true }).click()
  const compare = page.getByRole('button', { name: 'Preview profile', exact: true })
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

test('finds installed mod artwork and queues only explicitly selected mods from a share code', async ({ page }) => {
  const instance = 'mod-profile-selection'
  const id = 'profile-install-job'
  const code = '12345678-1234-4234-9234-123456789abc'
  const icon = 'https://mod-assets.example.test/mod-icon.png'
  const installed = {
    id: 1, instance_id: instance, source: 'thunderstore', owner: 'DemoTeam', name: 'ServerMod',
    version: '1.2.3', enabled: true, icon_url: icon, installed_at: '2026-01-01T00:00:00Z',
  }
  const job = {
    id, instance_id: instance, type: 'mod_install', title: 'Install mods from profile',
    status: 'succeeded', created_at: '2026-01-01T00:00:00Z',
  }

  await login(page)
  await createInstance(page, {
    id: instance, name: 'Profile selection', serverName: 'Profile selection',
    world: 'Original', port: 2474,
  })
  await page.route(icon, (route) => route.fulfill({
    contentType: 'image/png',
    body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l0sAAAAASUVORK5CYII=', 'base64'),
  }))
  await page.route('**/api/v1/instances/' + instance + '/mods', (route) => route.fulfill({ json: {
    bepinex: { installed: true, enabled: true, version: '5.4.2360' },
    mods: [installed],
  } }))
  await page.route('**/api/v1/instances/' + instance + '/mods/compare', (route) => {
    expect(route.request().postData()).toContain(code)
    return route.fulfill({ json: {
      profile_name: 'Shared Team', matched: 0, skipped_server_mods: [], skipped_profile_mods: [],
      differences: [
        { name: 'DemoTeam-NewMod', profile: { version: '2.0.1', enabled: true }, reasons: ['missing_on_server'] },
        { name: 'DemoTeam-AnotherMod', profile: { version: '1.0.0', enabled: true }, reasons: ['missing_on_server'] },
        { name: 'DemoTeam-DisabledMod', profile: { version: '1.5.0', enabled: false }, reasons: ['missing_on_server'] },
        {
          name: 'DemoTeam-ServerMod', reasons: ['version'],
          profile: { version: '1.0.0', enabled: true }, server: { version: '1.2.3', enabled: true },
        },
      ],
    } })
  })
  await page.route('**/api/v1/instances/' + instance + '/mods/profile-install', (route) => {
    expect(route.request().postDataJSON()).toEqual({
      packages: [{ owner: 'DemoTeam', name: 'NewMod', version: '2.0.1' }],
    })
    return route.fulfill({ status: 202, json: { job } })
  })
  await page.route('**/api/v1/jobs/' + id, (route) => route.fulfill({ json: { job } }))
  await page.route('**/api/v1/jobs/' + id + '/log', (route) => route.fulfill({ json: { lines: [] } }))

  await page.goto('/instances/' + instance + '/mods')
  const image = page.locator('img[src="' + icon + '"]')
  await image.scrollIntoViewIfNeeded()
  await expect(image).toHaveJSProperty('naturalWidth', 1)
  const search = page.getByRole('textbox', { name: 'Search installed mods' })
  await search.fill('does not match')
  await expect(page.getByText('No installed mods match the filters.')).toBeVisible()
  await search.fill('demo')
  await expect(page.getByText('ServerMod', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Import profile', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Import mod profile' })
  await dialog.getByText('Thunderstore code', { exact: true }).click()
  await dialog.getByRole('textbox', { name: 'Thunderstore profile code' }).fill(code)
  await dialog.getByRole('button', { name: 'Preview profile' }).click()
  await expect(dialog.getByRole('status')).toContainText('Shared Team')
  await expect(dialog.getByRole('button', { name: 'Install selected (0)' })).toBeDisabled()
  await expect(dialog.getByRole('checkbox', { name: 'Install DemoTeam-DisabledMod', exact: true })).toHaveCount(0)
  await expect(dialog.getByRole('checkbox', { name: 'Install DemoTeam-ServerMod', exact: true })).toHaveCount(0)
  await dialog.getByRole('checkbox', { name: 'Install DemoTeam-NewMod', exact: true }).check()
  await dialog.getByRole('button', { name: 'Install selected (1)' }).click()
  await expect(page.getByRole('dialog', { name: 'Job details' }).getByText('Install mods from profile')).toBeVisible()
})
