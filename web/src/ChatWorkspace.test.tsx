import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ChatWorkspace from './ChatWorkspace'

type JsonValue = Record<string, unknown>

const response = (body: JsonValue) => Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
const errorResponse = (status: number, code: string, message: string) => Promise.resolve({
  ok: false,
  status,
  json: () => Promise.resolve({ error: { code, message } }),
})

const chatProps = (overrides: Record<string, unknown> = {}) => ({
  knowledgeBaseState: 'ready',
  onInitializeKnowledgeBase: vi.fn(),
  onRetryKnowledgeBase: vi.fn(),
  onKnowledgeBaseRequired: vi.fn(),
  onError: vi.fn(),
  ...overrides,
}) as any

class FakeEventSource {
  static instances: FakeEventSource[] = []
  readonly close = vi.fn()
  onerror: ((event: Event) => void) | null = null
  private readonly listeners = new Map<string, Array<(event: Event) => void>>()

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }

  addEventListener(name: string, listener: EventListenerOrEventListenerObject) {
    const callback = typeof listener === 'function' ? listener : listener.handleEvent.bind(listener)
    this.listeners.set(name, [...(this.listeners.get(name) || []), callback])
  }

  emit(name: string, data = '{}') {
    const event = new MessageEvent(name, { data })
    for (const listener of this.listeners.get(name) || []) listener(event)
  }
}

describe('ChatWorkspace recovery', () => {
  const onError = vi.fn()

  beforeEach(() => {
    window.localStorage.clear()
    FakeEventSource.instances = []
    onError.mockReset()
  })

  afterEach(() => vi.unstubAllGlobals())

  it('keeps sessions in a sidebar and exposes the compact-layout drawer entry', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [
        { id: 'a', title: '第一会话', status: 'ready', created_at: '', updated_at: '' },
      ] })
      if (url.includes('/v1/sessions/a/messages')) return response({ items: [] })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const sidebar = await screen.findByRole('navigation', { name: '对话历史' })
    expect(sidebar).toHaveClass('session-sidebar')
    expect(within(sidebar).getByRole('button', { name: /第一会话/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '打开会话列表' })).toHaveAttribute('aria-expanded', 'false')
    expect(document.querySelector('.session-carousel')).not.toBeInTheDocument()
  })

  it('opens the session drawer and closes it after selecting a session or the backdrop', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [
        { id: 'a', title: '第一会话', status: 'ready', created_at: '', updated_at: '' },
        { id: 'b', title: '第二会话', status: 'ready', created_at: '', updated_at: '' },
      ] })
      if (url.includes('/v1/sessions/a/messages') || url.includes('/v1/sessions/b/messages')) return response({ items: [] })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const trigger = await screen.findByRole('button', { name: '打开会话列表' })
    await userEvent.click(trigger)
    let drawer = screen.getByRole('dialog', { name: '对话历史' })
    await userEvent.click(within(drawer).getByRole('button', { name: /第二会话/ }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '对话历史' })).not.toBeInTheDocument())
    expect(await screen.findByRole('heading', { name: '第二会话' })).toBeInTheDocument()
    expect(trigger).toHaveFocus()

    await userEvent.click(trigger)
    drawer = screen.getByRole('dialog', { name: '对话历史' })
    expect(drawer).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '关闭会话列表' }))
    expect(screen.queryByRole('dialog', { name: '对话历史' })).not.toBeInTheDocument()
  })

  it('manages session drawer semantics, background interaction, Escape, and focus restoration', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [] })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const trigger = await screen.findByRole('button', { name: '打开会话列表' })
    await userEvent.click(trigger)
    const drawer = screen.getByRole('dialog', { name: '对话历史' })
    expect(drawer).toHaveAttribute('aria-modal', 'true')
    expect(drawer).toHaveFocus()
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(document.querySelector('.conversation')).toHaveAttribute('inert')

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: '对话历史' })).not.toBeInTheDocument()
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveFocus()
    expect(document.querySelector('.conversation')).not.toHaveAttribute('inert')
  })

  it.each([
    [{ usable: false, reason: 'rag_refused', item_count: 0, items: [] }, '门禁未通过 · rag_refused'],
    [{ usable: false, reason: 'database connection string leaked', item_count: 0, items: [] }, '门禁未通过 · retrieval_unavailable'],
    [{ usable: true, item_count: 0, items: [] }, '未找到可用的检索依据'],
  ])('renders refused and empty retrieval results as a compact status', async (retrieval, expectedStatus) => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [{ id: 's1', title: '会话', status: 'ready', created_at: '', updated_at: '' }] })
      if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '检索测试')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    FakeEventSource.instances[0].emit('tool.completed', JSON.stringify({ tool: 'semantic_search_notes', summary: JSON.stringify(retrieval) }))

    expect(await screen.findByRole('status', { name: '检索状态' })).toHaveTextContent(expectedStatus)
    expect(document.querySelector('.evidence-detail')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /查看.*检索依据/ })).not.toBeInTheDocument()
  })

  it('keeps available evidence collapsed and restores focus after closing its detail', async () => {
    const retrieval = { usable: true, item_count: 1, items: [{ content: '完整依据内容', score: 0.912, citation: { kb_id: 1, document_id: 2, chunk_id: 'chunk-1', file_name: '来源.md', chunk_index: 3 } }] }
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [{ id: 's1', title: '会话', status: 'ready', created_at: '', updated_at: '' }] })
      if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '检索测试')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    FakeEventSource.instances[0].emit('tool.completed', JSON.stringify({ tool: 'semantic_search_notes', summary: JSON.stringify(retrieval) }))

    const trigger = await screen.findByRole('button', { name: '查看 1 条检索依据' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('完整依据内容')).not.toBeInTheDocument()
    await userEvent.click(trigger)
    const detail = screen.getByRole('dialog', { name: '检索依据' })
    expect(detail).toBeInTheDocument()
    expect(within(detail).getByText('来源.md')).toBeInTheDocument()
    expect(within(detail).getByText('相关度 0.912')).toBeInTheDocument()
    expect(within(detail).getByText('完整依据内容')).toBeInTheDocument()
    expect(within(detail).queryByText(/KB 1/)).not.toBeInTheDocument()
    expect(within(detail).queryByText(/文档 2/)).not.toBeInTheDocument()
    expect(within(detail).queryByText(/Chunk chunk-1/)).not.toBeInTheDocument()
    expect(within(detail).queryByText(/#3/)).not.toBeInTheDocument()
    expect(trigger).toHaveAttribute('aria-expanded', 'true')

    await userEvent.click(screen.getByRole('button', { name: '关闭检索依据' }))
    expect(screen.queryByRole('dialog', { name: '检索依据' })).not.toBeInTheDocument()
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(trigger).toHaveFocus()
  })

  it('closes evidence detail with Escape and when switching sessions', async () => {
    const retrieval = { usable: true, item_count: 1, items: [{ content: '会话一依据', score: 0.8, citation: { kb_id: 1, document_id: 2, chunk_id: 'chunk-1', file_name: '来源.md', chunk_index: 0 } }] }
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [
        { id: 's1', title: '会话一', status: 'ready', created_at: '', updated_at: '' },
        { id: 's2', title: '会话二', status: 'ready', created_at: '', updated_at: '' },
      ] })
      if (url.includes('/v1/sessions/s1/messages') || url.includes('/v1/sessions/s2/messages')) return response({ items: [] })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '检索测试')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    FakeEventSource.instances[0].emit('tool.completed', JSON.stringify({ tool: 'semantic_search_notes', summary: JSON.stringify(retrieval) }))
    FakeEventSource.instances[0].emit('run.completed')
    const trigger = await screen.findByRole('button', { name: '查看 1 条检索依据' })
    await waitFor(() => expect(within(screen.getByRole('navigation', { name: '对话历史' })).getByRole('button', { name: /会话二/ })).toBeEnabled())

    await userEvent.click(trigger)
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: '检索依据' })).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()

    await userEvent.click(trigger)
    const sidebar = screen.getByRole('navigation', { name: '对话历史' })
    await userEvent.click(within(sidebar).getByRole('button', { name: /会话二/ }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '检索依据' })).not.toBeInTheDocument())
    expect(await screen.findByRole('heading', { name: '会话二' })).toBeInTheDocument()
  })

  it('explains the knowledge-base prerequisite instead of rendering disabled chat controls', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      if (String(input).includes('/v1/sessions?')) return response({ items: [] })
      throw new Error(`unexpected fetch ${input}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ knowledgeBaseState: 'unconfigured', onError })} />)

    expect(await screen.findByRole('button', { name: '开启知识花园' })).toBeInTheDocument()
    expect(screen.getByText(/完成初始化后即可使用笔记问答和普通对话/)).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: '对话内容' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '新建对话' })).not.toBeInTheDocument()
    expect(screen.queryByText('可以询问过去记录的内容，也可以进行普通对话。')).not.toBeInTheDocument()
  })

  it('exposes one accessible initialization action while the knowledge base is initializing', async () => {
    const initialize = vi.fn()
    const fetchMock = vi.fn((input: string | URL) => {
      if (String(input).includes('/v1/sessions?')) return response({ items: [] })
      throw new Error(`unexpected fetch ${input}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ knowledgeBaseState: 'initializing', onInitializeKnowledgeBase: initialize, onError })} />)

    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent('正在开启知识花园')
    expect(screen.getByRole('button', { name: '正在开启知识花园' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: '新建对话' })).not.toBeInTheDocument()
    expect(initialize).not.toHaveBeenCalled()
  })

  it('downgrades to onboarding when the server reports a stale knowledge-base binding', async () => {
    const onKnowledgeBaseRequired = vi.fn()
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [] })
      if (url === '/v1/sessions' && init?.method === 'POST') return response({ id: 's1', title: '问题', status: 'ready', created_at: '', updated_at: '' })
      if (url.includes('/v1/sessions/s1/runs')) return errorResponse(409, 'knowledge_base_required', '请先创建并绑定个人知识库')
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ChatWorkspace {...chatProps({ onKnowledgeBaseRequired, onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '测试陈旧状态')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(onKnowledgeBaseRequired).toHaveBeenCalledTimes(1))
    view.rerender(<ChatWorkspace {...chatProps({ knowledgeBaseState: 'unconfigured', onKnowledgeBaseRequired, onError })} />)

    expect(await screen.findByRole('button', { name: '开启知识花园' })).toBeInTheDocument()
    expect(document.querySelectorAll('.message-row')).toHaveLength(0)
    expect(screen.queryByText('正在思考')).not.toBeInTheDocument()
  })

  it('clears the old conversation while a newly selected session fails to load', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [
        { id: 'a', title: '旧会话', status: 'ready', created_at: '', updated_at: '' },
        { id: 'b', title: '新会话', status: 'ready', created_at: '', updated_at: '' },
      ] })
      if (url.includes('/v1/sessions/a/messages')) return response({ items: [
        { id: 'm1', session_id: 'a', sequence: 1, role: 'assistant', content: '旧消息内容', created_at: '' },
      ] })
      if (url.includes('/v1/sessions/b/messages')) return Promise.reject(new Error('加载失败'))
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    expect(await screen.findByText('旧消息内容')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /新会话/ }))
    await waitFor(() => expect(onError).toHaveBeenCalledWith('加载失败'))
    expect(screen.queryByText('旧消息内容')).not.toBeInTheDocument()
  })

  it('closes the active EventSource when the workspace unmounts', async () => {
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [] })
      if (url === '/v1/sessions' && init?.method === 'POST') return response({ id: 's1', title: '问题', status: 'ready', created_at: '', updated_at: '' })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    const view = render(<ChatWorkspace {...chatProps({ onError })} />)

    await waitFor(() => expect(screen.getByRole('textbox', { name: '对话内容' })).toBeEnabled())
    await userEvent.type(screen.getByRole('textbox', { name: '对话内容' }), '测试问题')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    view.unmount()
    expect(FakeEventSource.instances[0].close).toHaveBeenCalledTimes(1)
  })

  it('removes optimistic messages when run creation fails', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [
        { id: 's1', title: '已有会话', status: 'ready', created_at: '', updated_at: '' },
      ] })
      if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
      if (url.includes('/v1/sessions/s1/runs')) return Promise.reject(new Error('创建 Run 失败'))
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '不会成功的问题')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(onError).toHaveBeenCalledWith('创建 Run 失败'))
    expect(document.querySelectorAll('.message-row')).toHaveLength(0)
    expect(composer).toBeEnabled()
  })

  it('keeps streamed assistant text in one semantically labelled response container', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [{ id: 's1', title: '已有会话', status: 'ready', created_at: '', updated_at: '' }] })
      if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '流式回答测试')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))

    const responseContainer = document.querySelector('.message-row.assistant .message-content')
    expect(responseContainer).toHaveAccessibleName('Note Agent 的回答')
    FakeEventSource.instances[0].emit('text.delta', JSON.stringify({ content: '第一段\n\n第二段' }))

    await waitFor(() => expect(responseContainer).toHaveTextContent('第一段 第二段'))
    expect(document.querySelector('.message-row.assistant .message-content')).toBe(responseContainer)
    expect(document.querySelectorAll('.message-row.assistant')).toHaveLength(1)
  })

  it.each(['run.completed', 'run.failed', 'run.timed_out', 'run.cancelled', 'run.interrupted'])(
    'handles the %s terminal event only once',
    async terminalEvent => {
      const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
        const url = String(input)
        if (url.includes('/v1/sessions?')) return response({ items: [] })
        if (url === '/v1/sessions' && init?.method === 'POST') return response({ id: 's1', title: '问题', status: 'ready', created_at: '', updated_at: '' })
        if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
        if (url === '/v1/runs/r1') return response({ status: terminalEvent === 'run.completed' ? 'completed' : terminalEvent.slice(4), error_message: '运行终止' })
        if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
        throw new Error(`unexpected fetch ${url}`)
      })
      vi.stubGlobal('fetch', fetchMock)
      vi.stubGlobal('EventSource', FakeEventSource)
      render(<ChatWorkspace {...chatProps({ onError })} />)

      const composer = await screen.findByRole('textbox', { name: '对话内容' })
      await userEvent.type(composer, '测试终止事件')
      await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
      await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
      FakeEventSource.instances[0].emit(terminalEvent)
      FakeEventSource.instances[0].emit(terminalEvent)

      await waitFor(() => expect(composer).toBeEnabled())
      expect(FakeEventSource.instances[0].close).toHaveBeenCalledTimes(1)
      expect(document.querySelectorAll('.message-row.assistant')).toHaveLength(0)
    },
  )

  it('keeps the existing candidate-note confirmation UI wired to SSE events', async () => {
    const fetchMock = vi.fn((input: string | URL) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [{ id: 's1', title: '会话', status: 'ready', created_at: '', updated_at: '' }] })
      if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '整理成笔记')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    FakeEventSource.instances[0].emit('note.draft.candidate', JSON.stringify({ id: 'c1', title: '候选标题', content: '候选内容', content_hash: 'hash', expires_at: '' }))

    expect(await screen.findByText('候选标题')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '确认保存' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '取消' })).toBeInTheDocument()
  })

  it('ignores a late failed status probe after the run already completed', async () => {
    let rejectProbe!: (reason: Error) => void
    const probe = new Promise((_, reject) => { rejectProbe = reject })
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [] })
      if (url === '/v1/sessions' && init?.method === 'POST') return response({ id: 's1', title: '问题', status: 'ready', created_at: '', updated_at: '' })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      if (url === '/v1/runs/r1') return probe
      if (url.includes('/v1/sessions/s1/messages')) return response({ items: [] })
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '竞态测试')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    FakeEventSource.instances[0].onerror?.(new Event('error'))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/v1/runs/r1', expect.anything()))
    FakeEventSource.instances[0].emit('run.completed')
    rejectProbe(new Error('迟到的探测错误'))

    await waitFor(() => expect(composer).toBeEnabled())
    expect(onError).not.toHaveBeenCalledWith(expect.stringContaining('迟到的探测错误'))
    expect(FakeEventSource.instances[0].close).toHaveBeenCalledTimes(1)
  })

  it('rolls back the empty assistant message when a live status probe fails', async () => {
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [] })
      if (url === '/v1/sessions' && init?.method === 'POST') return response({ id: 's1', title: '问题', status: 'ready', created_at: '', updated_at: '' })
      if (url.includes('/v1/sessions/s1/runs')) return response({ run: { id: 'r1' } })
      if (url === '/v1/runs/r1') return Promise.reject(new Error('状态探测失败'))
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('EventSource', FakeEventSource)
    render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '探测失败测试')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    FakeEventSource.instances[0].onerror?.(new Event('error'))

    await waitFor(() => expect(onError).toHaveBeenCalledWith('状态探测失败（Run ID: r1）'))
    expect(document.querySelectorAll('.message-row.assistant')).toHaveLength(0)
    expect(composer).toBeEnabled()
    expect(FakeEventSource.instances[0].close).toHaveBeenCalledTimes(1)
  })

  it('does not report a late run error after the workspace unmounts', async () => {
    let rejectRun!: (reason: Error) => void
    const runRequest = new Promise((_, reject) => { rejectRun = reject })
    const fetchMock = vi.fn((input: string | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/v1/sessions?')) return response({ items: [] })
      if (url === '/v1/sessions' && init?.method === 'POST') return response({ id: 's1', title: '问题', status: 'ready', created_at: '', updated_at: '' })
      if (url.includes('/v1/sessions/s1/runs')) return runRequest
      throw new Error(`unexpected fetch ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ChatWorkspace {...chatProps({ onError })} />)

    const composer = await screen.findByRole('textbox', { name: '对话内容' })
    await userEvent.type(composer, '离开页面')
    await userEvent.click(screen.getByRole('button', { name: '发送消息' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/runs'), expect.anything()))
    view.unmount()
    rejectRun(new Error('迟到的错误'))

    await new Promise(resolve => window.setTimeout(resolve, 10))
    expect(onError).not.toHaveBeenCalledWith('迟到的错误')
  })
})
