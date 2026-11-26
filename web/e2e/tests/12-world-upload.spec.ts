import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createInstance, dataDir, login } from './helpers'

const fixture = fileURLToPath(new URL('../../../testdata/world-upload/ChunkedUpload.zip', import.meta.url))
const instance = 'world-upload'

function payload(name: string) {
  return { name, mimeType: 'application/octet-stream', buffer: Buffer.from(name) }
}

async function upload(page: Page) {
  await page.getByRole('button', { name: 'Upload', exact: true }).click()
  const job = page.getByRole('dialog')
  await expect(job.getByText(/succeeded/i).first()).toBeVisible()
  await page.keyboard.press('Escape')
}

test.describe.serial('world upload formats', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('defaults to ZIP, explains a RAR rejection and imports a chunked save', async ({ page }) => {
    await createInstance(page, {
      id: instance, name: 'World upload test', serverName: 'World upload test',
      world: 'Original', port: 2466,
    })
    await page.goto('/instances/' + instance + '/worlds')
    await expect(page.getByRole('radio', { name: 'ZIP archive' })).toBeChecked()
    await expect(page.getByText('Choose a ZIP of the complete Valheim 1.0 world folder')).toBeVisible()

    const input = page.getByLabel('Choose a world ZIP archive')
    const submit = page.getByRole('button', { name: 'Upload', exact: true })
    await input.setInputFiles(payload('ChunkedUpload.rar'))
    await expect(page.getByText(/extract the RAR archive/i)).toBeVisible()
    await expect(submit).toBeDisabled()

    await input.setInputFiles(fixture)
    const saved = path.join(dataDir, 'instances', instance, 'save', 'worlds_local', 'ChunkedUpload')
    const preview = page.getByRole('button', { name: 'Preview', exact: true })
    await preview.click()
    await expect(page.getByText('Valheim 1.0 world folder · 5 files')).toBeVisible()
    expect(fs.existsSync(saved)).toBe(false)
    await expect(submit).toBeEnabled()
    await upload(page)
    expect(fs.readFileSync(path.join(saved, '00_00__0_1.chunk'), 'utf8')).toBe('test chunk')
    expect(fs.existsSync(path.join(saved, '_main.1.ok'))).toBeTruthy()
    await page.reload()
    await expect(page.getByRole('cell', { name: 'ChunkedUpload', exact: true })).toBeVisible()

    await input.setInputFiles(fixture)
    await preview.click()
    await expect(page.getByText('A world with this name already exists.')).toBeVisible()
    await expect(submit).toBeDisabled()
    await page.getByRole('checkbox', { name: /overwrite existing/i }).check()
    await expect(submit).toBeEnabled()
    await page.getByRole('button', { name: 'Clear', exact: true }).click()
    await expect(page.getByText('A world with this name already exists.')).toHaveCount(0)
  })

  test('legacy mode waits for a matching pair and accepts files picked separately', async ({ page }) => {
    await page.goto('/instances/' + instance + '/worlds')
    await page.getByText('Legacy files', { exact: true }).click()
    const input = page.getByLabel('Choose legacy world files')
    const submit = page.getByRole('button', { name: 'Upload', exact: true })
    await input.setInputFiles(payload('Legacy.db'))
    await expect(page.getByText('Select both files to upload this world.')).toBeVisible()
    await expect(submit).toBeDisabled()

    await input.setInputFiles(payload('Wrong.fwl'))
    await expect(page.getByText('The .db and .fwl files must have the same world name.')).toBeVisible()
    await expect(submit).toBeDisabled()

    await input.setInputFiles(payload('Legacy.fwl'))
    await expect(submit).toBeEnabled()
    await upload(page)
    const saved = path.join(dataDir, 'instances', instance, 'save', 'worlds_local')
    expect(fs.readFileSync(path.join(saved, 'Legacy.db'), 'utf8')).toBe('Legacy.db')
    expect(fs.readFileSync(path.join(saved, 'Legacy.fwl'), 'utf8')).toBe('Legacy.fwl')
  })
})
