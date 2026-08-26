import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

const globalStyles = readFileSync('src/style.css', 'utf8')
const workspaceStyles = readFileSync('src/app-layout.css', 'utf8')
const chatStyles = readFileSync('src/chat.css', 'utf8')

describe('iOS minimal visual contract', () => {
  it('defines the approved semantic palette and system font stack', () => {
    expect(globalStyles.toLowerCase()).toContain('--bg: #fffafa')
    expect(globalStyles.toLowerCase()).toContain('--text: #1c1c1e')
    expect(globalStyles.toLowerCase()).toContain('--primary: #2e8b57')
    expect(globalStyles.toLowerCase()).toContain('--link: #027afe')
    expect(globalStyles).toContain('-apple-system')
    expect(globalStyles).not.toMatch(/fonts\.googleapis|@import\s+url/i)
  })

  it('keeps the viewport bounded with dynamic-height and safe-area rules', () => {
    expect(globalStyles).toContain('overflow-x: clip')
    expect(workspaceStyles).toContain('height: 100vh; height: 100dvh')
    expect(workspaceStyles).toContain('env(safe-area-inset-bottom)')
    expect(workspaceStyles).toContain('@media (max-width: 760px)')
    expect(globalStyles).toContain('@media (max-width: 879px)')
  })

  it('isolates scroll containers and prevents textarea drag resizing', () => {
    expect(workspaceStyles).toContain('overscroll-behavior: contain')
    expect(chatStyles.match(/overscroll-behavior: contain/g)?.length).toBeGreaterThanOrEqual(3)
    expect(workspaceStyles.match(/resize: none/g)?.length).toBeGreaterThanOrEqual(2)
    expect(chatStyles).toContain('resize: none')
    expect(chatStyles).toContain('max-height: 140px')
  })

  it('uses green for chat actions and blue for retrieval references', () => {
    expect(chatStyles).toContain('.chat-composer button')
    expect(chatStyles).toContain('background: var(--primary)')
    expect(chatStyles).toContain('.chat-workspace .source small { color: var(--link); }')
  })

  it('compacts chat onboarding for 568px-tall mobile viewports', () => {
    expect(workspaceStyles).toContain('@media (max-width: 760px) and (max-height: 650px)')
    expect(workspaceStyles).toContain('.chat-page-heading .eyebrow { display: none; }')
    expect(chatStyles).toContain('@media (max-height: 650px)')
    expect(chatStyles).toContain('@container (max-width: 639px)')
    expect(chatStyles).toContain('.conversation { grid-template-rows: 48px minmax(0, 1fr) auto auto; }')
    expect(chatStyles).toContain('.chat-onboarding > span { width: 36px; height: 36px;')
  })

  it('uses a 640px boundary for a persistent session rail and mobile drawer', () => {
    expect(chatStyles).toContain('grid-template-columns: 200px minmax(0, 1fr)')
    expect(workspaceStyles).toContain('container-type: inline-size')
    expect(chatStyles).toContain('@container (max-width: 639px)')
    expect(chatStyles).toContain('.session-drawer-trigger { display: none;')
    expect(chatStyles).toMatch(/@container \(max-width: 639px\)[\s\S]*\.session-sidebar \{ display: none;/)
    expect(chatStyles).toMatch(/@container \(max-width: 639px\)[\s\S]*\.session-drawer-trigger \{ display: grid;/)
    expect(chatStyles).not.toMatch(/\.session-list\s*\{[^}]*display:\s*flex/)
  })

  it('keeps messages flexible while evidence and overlays own their scrolling', () => {
    expect(chatStyles).toContain('grid-template-rows: 68px minmax(0, 1fr) auto auto')
    expect(chatStyles).toContain('.evidence-summary')
    expect(chatStyles).toContain('.evidence-status')
    expect(chatStyles).toMatch(/\.evidence-detail\s*\{[^}]*overflow:\s*hidden/)
    expect(chatStyles).toMatch(/\.evidence-detail-content\s*\{[^}]*overflow-y:\s*auto/)
    expect(chatStyles).toMatch(/\.session-drawer\s*\{[^}]*min-height:\s*0/)
    expect(chatStyles).toMatch(/\.message-list\s*\{[^}]*min-width:\s*0/)
    expect(chatStyles).toMatch(/@container \(max-width: 639px\)[\s\S]*\.evidence-detail-layer \{[^}]*align-items:\s*end/)
    expect(chatStyles).toContain('height: min(72dvh, 560px, 100%)')
  })

  it('uses document-like assistant answers while preserving user bubbles and safe wrapping', () => {
    expect(chatStyles).toContain('.message-row.assistant .message-body { width: 100%; max-width: 760px; }')
    expect(chatStyles).toContain('.message-row.assistant .message-content { padding: 3px 2px; border: 0; background: transparent; font-size: 15px; line-height: 1.72; }')
    expect(chatStyles).toMatch(/\.message-row\.user \.message-content\s*\{[^}]*border:\s*1px solid var\(--primary\)[^}]*background:\s*var\(--primary\)/)
    expect(chatStyles).toMatch(/\.message-content\s*\{[^}]*overflow-wrap:\s*anywhere[^}]*white-space:\s*pre-wrap/)
    expect(chatStyles).toContain('.message-row.user .message-body { max-width: min(82%, 720px);')
    expect(chatStyles).toMatch(/@media \(max-width: 760px\)[\s\S]*\.message-row\.user \.message-body \{ max-width: 88%; \}/)
  })
})
