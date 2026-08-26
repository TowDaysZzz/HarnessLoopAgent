import { StrictMode } from 'react'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'

describe('Mori workspace behavior', () => {
  beforeEach(() => {
    window.location.hash = ''
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('not authenticated')))
    vi.useFakeTimers({ shouldAdvanceTime: true })
    vi.setSystemTime(new Date('2026-08-26T09:30:00+08:00'))
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  async function enterDemo() {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)
    await user.click(screen.getByRole('button', { name: '体验交互 Demo' }))
    return user
  }

  it('initializes the knowledge base from chat and synchronizes the whole workspace', async () => {
    window.location.hash = '#chat'
    let resolveInitialization!: (value: unknown) => void
    const initialization = new Promise(resolve => { resolveInitialization = resolve })
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: false }) })
      if (url === '/v1/knowledge-base' && init?.method === 'POST') return initialization
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)

    const initialize = await screen.findByRole('button', { name: '开启知识花园' })
    await user.click(initialize)
    await user.click(initialize)
    expect(fetchMock.mock.calls.filter(([url, init]) => url === '/v1/knowledge-base' && (init as RequestInit)?.method === 'POST')).toHaveLength(1)
    expect(await screen.findByRole('status')).toHaveTextContent('正在开启知识花园')

    await act(async () => resolveInitialization({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '我的笔记', status: 'ready' } }) }))
    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await waitFor(() => expect(composer).toHaveFocus())
    expect(screen.getByRole('button', { name: '新建对话' })).toBeEnabled()
    expect(fetchMock.mock.calls.filter(([url, init]) => url === '/v1/sessions' && (init as RequestInit)?.method === 'POST')).toHaveLength(0)
    expect(screen.getByText('知识库已连接')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '我的笔记' }))
    expect(screen.getByRole('textbox', { name: '笔记标题' })).toBeEnabled()
    expect(screen.getByRole('textbox', { name: '笔记内容' })).toBeEnabled()
  })

  it('does not offer initialization before the existing knowledge-base state resolves', async () => {
    window.location.hash = '#chat'
    let resolveKnowledgeBase!: (value: unknown) => void
    const knowledgeBaseLoad = new Promise(resolve => { resolveKnowledgeBase = resolve })
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return knowledgeBaseLoad
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)

    expect(await screen.findByRole('status')).toHaveTextContent('正在确认知识花园状态')
    expect(screen.queryByRole('button', { name: '开启知识花园' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url, init]) => url === '/v1/knowledge-base' && (init as RequestInit)?.method === 'POST')).toHaveLength(0)

    await act(async () => resolveKnowledgeBase({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '已有知识库', status: 'ready' } }) }))
    expect(await screen.findByRole('textbox', { name: '对话内容' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: '开启知识花园' })).not.toBeInTheDocument()
  })

  it('offers state reloading instead of initialization when knowledge-base lookup fails', async () => {
    window.location.hash = '#chat'
    let loadAttempts = 0
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') {
        loadAttempts++
        if (loadAttempts === 1) return Promise.reject(new Error('知识库状态读取失败'))
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '已有知识库', status: 'ready' } }) })
      }
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)

    expect(await screen.findByRole('alert')).toHaveTextContent('知识库状态读取失败')
    expect(screen.queryByRole('button', { name: '开启知识花园' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重新确认知识花园状态' }))

    expect(await screen.findByRole('textbox', { name: '对话内容' })).toBeEnabled()
    expect(fetchMock.mock.calls.filter(([url, init]) => url === '/v1/knowledge-base' && (init as RequestInit)?.method === 'POST')).toHaveLength(0)
    expect(loadAttempts).toBe(2)
  })

  it('keeps asynchronous state updates enabled after StrictMode replays effects', async () => {
    window.location.hash = '#chat'
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: false }) })
      if (url === '/v1/knowledge-base' && init?.method === 'POST') return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '我的笔记', status: 'ready' } }) })
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<StrictMode><App /></StrictMode>)

    const initialize = await screen.findByRole('button', { name: '开启知识花园' })
    initialize.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('textbox', { name: '对话内容' })).toBeEnabled()
  })

  it('announces initialization failure and allows a successful retry', async () => {
    window.location.hash = '#chat'
    let initializationAttempts = 0
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: false }) })
      if (url === '/v1/knowledge-base' && init?.method === 'POST') {
        initializationAttempts++
        if (initializationAttempts === 1) return Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({ error: { message: '初始化暂时失败' } }) })
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '我的笔记', status: 'ready' } }) })
      }
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)

    await user.click(await screen.findByRole('button', { name: '开启知识花园' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('初始化暂时失败')
    const retry = screen.getByRole('button', { name: '开启知识花园' })
    expect(retry).toBeEnabled()
    retry.focus()
    await user.keyboard('{Enter}')

    expect(await screen.findByRole('textbox', { name: '对话内容' })).toHaveFocus()
    expect(initializationAttempts).toBe(2)
  })

  it('reuses the same recoverable initialization action from the notes page', async () => {
    let attempts = 0
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: false }) })
      if (url === '/v1/knowledge-base' && init?.method === 'POST') {
        attempts++
        if (attempts === 1) return Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({ error: { message: '初始化暂时失败' } }) })
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '我的笔记', status: 'ready' } }) })
      }
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)

    await user.click(await screen.findByRole('button', { name: '立即开启' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('初始化暂时失败')
    await user.click(screen.getByRole('button', { name: '立即开启' }))

    expect(await screen.findByText('知识库已连接')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: '笔记标题' })).toBeEnabled()
    expect(attempts).toBe(2)
  })

  it.each(['resolve', 'reject'] as const)('ignores a late knowledge-base load that would %s after unmount', async outcome => {
    window.location.hash = '#chat'
    let settleLoad!: (value: unknown) => void
    const oldLoad = new Promise((resolve, reject) => {
      settleLoad = outcome === 'resolve' ? resolve : reject
    })
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return oldLoad
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const view = render(<App />)

    expect(await screen.findByRole('status')).toHaveTextContent('正在确认知识花园状态')
    view.unmount()
    await act(async () => settleLoad(outcome === 'resolve'
      ? { ok: true, json: () => Promise.resolve({ configured: false }) }
      : new Error('迟到的知识库加载错误')))

    expect(consoleError).not.toHaveBeenCalled()
    consoleError.mockRestore()
  })

  it('does not steal focus when initialization finishes after leaving chat', async () => {
    window.location.hash = '#chat'
    let resolveInitialization!: (value: unknown) => void
    const initialization = new Promise(resolve => { resolveInitialization = resolve })
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/v1/auth/me') return Promise.resolve({ ok: true, json: () => Promise.resolve({ user_id: 1 }) })
      if (url === '/v1/notes') return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      if (url === '/v1/knowledge-base' && init?.method !== 'POST') return Promise.resolve({ ok: true, json: () => Promise.resolve({ configured: false }) })
      if (url === '/v1/knowledge-base' && init?.method === 'POST') return initialization
      if (url.includes('/v1/sessions?')) return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [] }) })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)

    await user.click(await screen.findByRole('button', { name: '开启知识花园' }))
    await user.click(screen.getByRole('button', { name: '我的笔记' }))
    const search = screen.getByRole('textbox', { name: '搜索笔记' })
    search.focus()
    await act(async () => resolveInitialization({ ok: true, json: () => Promise.resolve({ configured: true, knowledge_base: { kb_id: 7, name: '我的笔记', status: 'ready' } }) }))

    expect(search).toHaveFocus()
    expect(screen.getByText('知识库已连接')).toBeInTheDocument()
  })

  it('keeps the login form in a logical keyboard order', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)
    await user.tab()
    expect(screen.getByRole('textbox', { name: '邮箱' })).toHaveFocus()
    await user.tab()
    expect(screen.getByLabelText('密码')).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: '登录' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: '体验交互 Demo' })).toHaveFocus()
  })

  it('announces and dismisses login errors', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<App />)
    await user.type(screen.getByRole('textbox', { name: '邮箱' }), 'demo@example.com')
    await user.type(screen.getByLabelText('密码'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('not authenticated')
    await user.click(screen.getByRole('button', { name: '关闭错误' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows the current local date instead of a fixed demo date', async () => {
    await enterDemo()
    expect(screen.getByText('星期三 · 8月26日')).toBeInTheDocument()
  })

  it('focuses search when Meta+K is pressed', async () => {
    const user = await enterDemo()
    const search = screen.getByRole('textbox', { name: '搜索笔记' })
    await user.keyboard('{Meta>}k{/Meta}')
    expect(search).toHaveFocus()
  })

  it('exposes current navigation semantics and hides unfinished controls', async () => {
    await enterDemo()
    expect(screen.getByRole('button', { name: /^我的笔记/ })).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('button', { name: /^收集箱/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '置顶' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '添加标签' })).not.toBeInTheDocument()
  })

  it('makes every demo conversation button switch real content', async () => {
    const user = await enterDemo()
    await user.click(screen.getByRole('button', { name: '与知识对话' }))
    await user.click(screen.getByRole('button', { name: '本周项目回顾' }))
    expect(screen.getByText('本周你完成了搜索体验的第一轮验证。下一步重点是优化新用户空状态，并减少首次使用时不必要的设置。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '本周项目回顾' })).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('button', { name: '让知识真正流动起来' })).not.toBeInTheDocument()
  })

  it('supports keyboard activation for navigation, note creation, conversation switching, and toast dismissal', async () => {
    const user = await enterDemo()
    const chatNav = screen.getByRole('button', { name: '与知识对话' })
    chatNav.focus()
    await user.keyboard('{Enter}')
    expect(chatNav).toHaveAttribute('aria-current', 'page')

    const thread = screen.getByRole('button', { name: '本周项目回顾' })
    thread.focus()
    await user.keyboard('{Enter}')
    expect(thread).toHaveAttribute('aria-current', 'page')

    const notesNav = screen.getByRole('button', { name: '我的笔记' })
    notesNav.focus()
    await user.keyboard('{Enter}')
    await user.type(screen.getByRole('textbox', { name: '笔记标题' }), '键盘笔记')
    await user.type(screen.getByRole('textbox', { name: '笔记内容' }), '全程使用键盘。')
    const save = screen.getByRole('button', { name: '保存笔记' })
    save.focus()
    await user.keyboard('{Enter}')
    await act(async () => { await vi.advanceTimersByTimeAsync(400) })
    expect(screen.getByRole('status')).toHaveTextContent('笔记已保存到演示空间。')
    const close = screen.getByRole('button', { name: '关闭提示' })
    close.focus()
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()

    const deleteButton = screen.getAllByRole('button', { name: '删除笔记' })[0]
    deleteButton.focus()
    await user.keyboard('{Enter}')
    const confirmDelete = screen.getByRole('button', { name: '确认删除' })
    confirmDelete.focus()
    await user.keyboard('{Enter}')
    expect(screen.getByRole('status')).toHaveTextContent('笔记已删除。')
    await user.click(screen.getByRole('button', { name: '关闭提示' }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
