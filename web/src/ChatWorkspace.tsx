import { FormEvent, useEffect, useRef, useState } from 'react'
import { Bot, Check, ChevronRight, Clock3, Layers3, Menu, MessageSquare, Plus, Send, User, X } from 'lucide-react'
import './chat.css'

type ChatSession = { id: string; title: string; status: string; created_at: string; updated_at: string }
type ChatMessage = { id: string; session_id: string; sequence: number; role: 'user' | 'assistant'; content: string; created_at: string }
type RetrievalItem = {
  content: string
  score: number
  citation: { kb_id: number; document_id: number; chunk_id: string; file_name: string; chunk_index: number }
}
type RetrievalResult = { usable: boolean; reason?: string; item_count: number; items: RetrievalItem[] }
type NoteCandidate = { id: string; title: string; content: string; content_hash: string; expires_at: string }

type Props = {
  knowledgeBaseState: 'resolving' | 'resolution_failed' | 'unconfigured' | 'initializing' | 'ready'
  onInitializeKnowledgeBase: () => void | Promise<void>
  onRetryKnowledgeBase: () => void | Promise<void>
  onKnowledgeBaseRequired: () => void | Promise<void>
  onError: (message: string) => void
  onNoteChanged?: () => void | Promise<void>
}

class APIRequestError extends Error {
  constructor(message: string, readonly code: string, readonly status: number) {
    super(message)
  }
}

const api = async (path: string, init?: RequestInit) => fetch(path, { credentials: 'include', ...init }).then(async response => {
  const body = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIRequestError(body?.error?.message || body?.message || '请求失败', body?.error?.code || '', response.status)
  return body
})

const formatTime = (value: string) => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  if (date.toDateString() === new Date().toDateString()) return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

const makeTitle = (message: string) => {
  const compact = message.replace(/\s+/g, ' ').trim()
  return compact.length > 28 ? `${compact.slice(0, 28)}...` : compact || '新会话'
}

const retrievalReasonCodes = new Set([
  'missing_response',
  'missing_request_id',
  'rag_refused',
  'evidence_gate_not_passed',
  'citation_check_not_supported',
  'citation_check_failed',
  'insufficient_results',
  'top_score_below_threshold',
])

const safeRetrievalReason = (reason?: string) => reason && retrievalReasonCodes.has(reason) ? reason : 'retrieval_unavailable'

export default function ChatWorkspace({ knowledgeBaseState, onInitializeKnowledgeBase, onRetryKnowledgeBase, onKnowledgeBaseRequired, onError, onNoteChanged }: Props) {
  const [sessions, setSessions] = useState<ChatSession[]>([])
  const [sessionID, setSessionID] = useState('')
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [query, setQuery] = useState('')
  const [chatting, setChatting] = useState(false)
  const [loading, setLoading] = useState(true)
  const [retrieval, setRetrieval] = useState<RetrievalResult | null>(null)
  const [candidate, setCandidate] = useState<NoteCandidate | null>(null)
  const [activeRunID, setActiveRunID] = useState('')
  const [creatingSession, setCreatingSession] = useState(false)
  const [sessionDrawerOpen, setSessionDrawerOpen] = useState(false)
  const [evidenceDetailOpen, setEvidenceDetailOpen] = useState(false)
  const requestSequence = useRef(0)
  const messageEnd = useRef<HTMLDivElement>(null)
  const messageList = useRef<HTMLDivElement>(null)
  const composer = useRef<HTMLTextAreaElement>(null)
  const followOutput = useRef(true)
  const activeStream = useRef<{ runID: string; source: EventSource } | null>(null)
  const mounted = useRef(true)
  const initializationRequested = useRef(false)
  const previousKnowledgeBaseState = useRef(knowledgeBaseState)
  const conversation = useRef<HTMLElement>(null)
  const sessionDrawer = useRef<HTMLElement>(null)
  const sessionDrawerTrigger = useRef<HTMLButtonElement>(null)
  const evidenceDetail = useRef<HTMLElement>(null)
  const evidenceDetailTrigger = useRef<HTMLButtonElement>(null)
  const canChat = knowledgeBaseState === 'ready'

  function closeActiveStream(runID?: string) {
    const current = activeStream.current
    if (!current || (runID && current.runID !== runID)) return
    current.source.close()
    activeStream.current = null
  }

  async function loadMessages(id: string, clearRunContext = true) {
    const request = ++requestSequence.current
    setLoading(true)
    if (clearRunContext) setRetrieval(null)
    try {
      const result = await api(`/v1/sessions/${id}/messages?limit=200`)
      if (request === requestSequence.current) setMessages(result.items || [])
    } catch (reason) {
      if (request === requestSequence.current) onError((reason as Error).message)
    } finally {
      if (request === requestSequence.current) setLoading(false)
    }
  }

  async function refreshSessions() {
    const result = await api('/v1/sessions?limit=50')
    const items = (result.items || []) as ChatSession[]
    if (mounted.current) setSessions(items)
    return items
  }

  async function loadInitial() {
    try {
      const items = await refreshSessions()
      if (!mounted.current) return
      const storedID = window.localStorage.getItem('note-agent-session') || ''
      const selectedID = items.some(item => item.id === storedID) ? storedID : items[0]?.id || ''
      if (selectedID) {
        setSessionID(selectedID)
        window.localStorage.setItem('note-agent-session', selectedID)
        await loadMessages(selectedID)
      } else {
        setSessionID('')
        setMessages([])
        setLoading(false)
      }
    } catch (reason) {
      if (!mounted.current) return
      setLoading(false)
      onError((reason as Error).message)
    }
  }

  useEffect(() => {
    mounted.current = true
    void loadInitial()
    return () => {
      mounted.current = false
      requestSequence.current++
      closeActiveStream()
    }
  }, [])
  useEffect(() => {
    if (knowledgeBaseState === 'ready' && previousKnowledgeBaseState.current !== 'ready' && initializationRequested.current) {
      initializationRequested.current = false
      window.setTimeout(() => composer.current?.focus(), 0)
    }
    previousKnowledgeBaseState.current = knowledgeBaseState
  }, [knowledgeBaseState])
  useEffect(() => {
    if (followOutput.current) messageEnd.current?.scrollIntoView({ behavior: chatting ? 'smooth' : 'auto', block: 'end' })
  }, [messages, chatting])
  useEffect(() => {
    const background = conversation.current
    if (!sessionDrawerOpen) {
      background?.removeAttribute('inert')
      return
    }
    background?.setAttribute('inert', '')
    sessionDrawer.current?.focus()
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        closeSessionDrawer()
        return
      }
      if (event.key !== 'Tab' || !sessionDrawer.current) return
      const focusable = Array.from(sessionDrawer.current.querySelectorAll<HTMLElement>('button:not(:disabled), [href], input:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'))
      if (!focusable.length) {
        event.preventDefault()
        sessionDrawer.current.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && (document.activeElement === first || document.activeElement === sessionDrawer.current)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      background?.removeAttribute('inert')
    }
  }, [sessionDrawerOpen])
  useEffect(() => {
    const background = conversation.current
    if (!evidenceDetailOpen) {
      background?.removeAttribute('inert')
      return
    }
    background?.setAttribute('inert', '')
    evidenceDetail.current?.focus()
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        closeEvidenceDetail()
        return
      }
      if (event.key !== 'Tab' || !evidenceDetail.current) return
      const focusable = Array.from(evidenceDetail.current.querySelectorAll<HTMLElement>('button:not(:disabled), [href], input:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'))
      if (!focusable.length) {
        event.preventDefault()
        evidenceDetail.current.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && (document.activeElement === first || document.activeElement === evidenceDetail.current)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      background?.removeAttribute('inert')
    }
  }, [evidenceDetailOpen])

  function closeSessionDrawer(restoreFocus = true) {
    setSessionDrawerOpen(false)
    if (restoreFocus) window.setTimeout(() => sessionDrawerTrigger.current?.focus(), 0)
  }

  function closeEvidenceDetail(restoreFocus = true) {
    setEvidenceDetailOpen(false)
    if (restoreFocus) window.setTimeout(() => evidenceDetailTrigger.current?.focus(), 0)
  }

  async function newChat() {
    if (!canChat || chatting || creatingSession) return
    setCreatingSession(true)
    onError('')
    requestSequence.current++
    closeEvidenceDetail(false)
    try {
      const session = await api('/v1/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: `新会话 ${new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}` }) }) as ChatSession
      if (!mounted.current) return
      setSessions(value => [session, ...value.filter(item => item.id !== session.id)])
      setSessionID(session.id)
      setMessages([])
      setRetrieval(null)
      setCandidate(null)
      setActiveRunID('')
      setQuery('')
      setLoading(false)
      window.localStorage.setItem('note-agent-session', session.id)
      followOutput.current = true
      window.setTimeout(() => composer.current?.focus(), 0)
    } catch (reason) {
      if (mounted.current) onError((reason as Error).message)
    } finally {
      if (mounted.current) setCreatingSession(false)
    }
  }

  async function selectSession(id: string) {
    if (chatting || id === sessionID) return
    closeEvidenceDetail(false)
    setSessionID(id)
    setMessages([])
    setCandidate(null)
    setActiveRunID('')
    setRetrieval(null)
    followOutput.current = true
    window.localStorage.setItem('note-agent-session', id)
    await loadMessages(id)
  }

  function selectSessionFromDrawer(id: string) {
    closeSessionDrawer()
    void selectSession(id)
  }

  async function runQuestion(rawQuestion: string) {
    const question = rawQuestion.trim()
    if (!canChat || !question) return
    const candidateCommand = question === '确认保存' || question === '取消保存'
    onError('')
    closeEvidenceDetail(false)
    setRetrieval(null)
    if (!candidateCommand) setCandidate(null)
    setChatting(true)
    followOutput.current = true
    const optimisticMessageIDs: string[] = []
    try {
      let activeID = sessionID
      if (!activeID) {
        const session = await api('/v1/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: makeTitle(question) }) }) as ChatSession
        if (!mounted.current) return
        activeID = session.id
        setSessionID(activeID)
        setSessions(value => [session, ...value])
        window.localStorage.setItem('note-agent-session', activeID)
      }
      const userMessage: ChatMessage = { id: crypto.randomUUID(), session_id: activeID, sequence: messages.length + 1, role: 'user', content: question, created_at: new Date().toISOString() }
      const assistantMessage: ChatMessage = { id: `stream-${crypto.randomUUID()}`, session_id: activeID, sequence: messages.length + 2, role: 'assistant', content: '', created_at: new Date().toISOString() }
      optimisticMessageIDs.push(userMessage.id, assistantMessage.id)
      setMessages(value => [...value, userMessage, assistantMessage])
      setQuery('')
      const run = await api(`/v1/sessions/${activeID}/runs`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ message: question }) })
      if (!mounted.current) return
      const runID = run.run.id as string
      setActiveRunID(runID)
      closeActiveStream()
      const source = new EventSource(`/v1/runs/${runID}/events`, { withCredentials: true })
      activeStream.current = { runID, source }
      let terminal = false
      let checkingStream = false

      const finish = async () => {
        if (terminal || !mounted.current || activeStream.current?.runID !== runID) return
        terminal = true
        closeActiveStream(runID)
        setChatting(false)
        if (candidateCommand) setCandidate(null)
        await loadMessages(activeID, false)
        const refreshes = await Promise.allSettled([refreshSessions(), Promise.resolve().then(() => onNoteChanged?.())])
        const refreshFailure = refreshes.find(result => result.status === 'rejected')
        if (refreshFailure?.status === 'rejected' && mounted.current) onError(`回答已完成，但刷新工作台失败：${(refreshFailure.reason as Error).message}`)
      }

      const fail = async (fallback: string) => {
        if (terminal || !mounted.current || activeStream.current?.runID !== runID) return
        terminal = true
        closeActiveStream(runID)
        setChatting(false)
        setMessages(value => value.filter(message => message.id !== assistantMessage.id))
        try {
          const failedRun = await api(`/v1/runs/${runID}`)
          if (mounted.current) onError(`${failedRun.error_message || fallback}（Run ID: ${runID}）`)
        } catch (reason) {
          if (mounted.current) onError(`${(reason as Error).message}（Run ID: ${runID}）`)
        }
      }

      source.addEventListener('text.delta', eventValue => {
        if (!mounted.current || activeStream.current?.runID !== runID) return
        const data = JSON.parse((eventValue as MessageEvent).data)
        if (data.content) setMessages(value => value.map(message => message.id === assistantMessage.id ? { ...message, content: message.content + data.content } : message))
      })
      source.addEventListener('tool.completed', eventValue => {
        if (!mounted.current || activeStream.current?.runID !== runID) return
        const data = JSON.parse((eventValue as MessageEvent).data)
        if (data.tool !== 'semantic_search_notes' || !data.summary) return
        try {
          closeEvidenceDetail(false)
          setRetrieval(JSON.parse(data.summary) as RetrievalResult)
        } catch {
          onError('检索结果格式无效')
        }
      })
      source.addEventListener('note.draft.candidate', eventValue => {
        if (!mounted.current || activeStream.current?.runID !== runID) return
        try {
          setCandidate(JSON.parse((eventValue as MessageEvent).data) as NoteCandidate)
        } catch {
          onError(`候选笔记格式无效（Run ID: ${runID}）`)
        }
      })
      source.addEventListener('run.completed', () => { void finish() })
      for (const eventName of ['run.failed', 'run.timed_out', 'run.cancelled', 'run.interrupted']) {
        source.addEventListener(eventName, () => { void fail(`Agent 运行终止：${eventName}`) })
      }
      source.onerror = async () => {
        if (terminal || checkingStream || !mounted.current) return
        checkingStream = true
        try {
          const currentRun = await api(`/v1/runs/${runID}`)
          if (terminal || !mounted.current || activeStream.current?.runID !== runID) return
          if (currentRun.status === 'completed') await finish()
          else if (['failed', 'timed_out', 'cancelled', 'interrupted'].includes(currentRun.status)) await fail('Agent 流式连接意外中断')
          // queued/running keeps EventSource open so the browser reconnects with Last-Event-ID.
        } catch (reason) {
          if (terminal || !mounted.current || activeStream.current?.runID !== runID) return
          closeActiveStream(runID)
          terminal = true
          setChatting(false)
          setMessages(value => value.filter(message => message.id !== assistantMessage.id))
          onError(`${(reason as Error).message}（Run ID: ${runID}）`)
        } finally {
          checkingStream = false
        }
      }
    } catch (reason) {
      if (!mounted.current) return
      if (optimisticMessageIDs.length) {
        setMessages(value => value.filter(message => !optimisticMessageIDs.includes(message.id)))
      }
      setChatting(false)
      setActiveRunID('')
      if (reason instanceof APIRequestError && reason.code === 'knowledge_base_required') {
        void onKnowledgeBaseRequired()
      }
      onError((reason as Error).message)
    }
  }

  async function ask(event: FormEvent) {
    event.preventDefault()
    await runQuestion(query)
  }

  function candidateAction(message: string) {
    void runQuestion(message)
  }

  const activeSession = sessions.find(session => session.id === sessionID)
  const resolvingKnowledgeBase = knowledgeBaseState === 'resolving'
  const knowledgeBaseResolutionFailed = knowledgeBaseState === 'resolution_failed'
  const startKnowledgeBase = () => {
    if (knowledgeBaseState !== 'unconfigured') return
    initializationRequested.current = true
    void onInitializeKnowledgeBase()
  }
  const sessionNavigation = (inDrawer = false) => <>
    <div className="session-head"><div><h2 id={inDrawer ? 'session-drawer-title' : undefined}>对话历史</h2><small>{sessions.length} 个会话</small></div><div className="session-head-actions">{canChat && <button type="button" className="icon new-chat" title="新建对话" aria-label="新建对话" onClick={() => { if (inDrawer) closeSessionDrawer(); void newChat() }} disabled={chatting || creatingSession}><Plus size={18} /></button>}{inDrawer && <button type="button" className="icon session-drawer-close" title="关闭" aria-label="关闭" onClick={() => closeSessionDrawer()}><X size={18} /></button>}</div></div>
    <div className="session-list">{sessions.map(session => <button type="button" className={`session-item ${session.id === sessionID ? 'active' : ''}`} key={session.id} onClick={() => inDrawer ? selectSessionFromDrawer(session.id) : void selectSession(session.id)} disabled={chatting}><MessageSquare size={15} /><span><b>{session.title}</b><small><Clock3 size={11} />{formatTime(session.updated_at)}</small></span></button>)}{sessions.length === 0 && <div className="empty-history">还没有历史对话</div>}</div>
  </>
  const evidenceCount = retrieval?.items.length || 0
  const evidenceAvailable = Boolean(retrieval?.usable && evidenceCount > 0)
  return <section className="chat-workspace">
    <nav className="session-sidebar" aria-label="对话历史">
      {sessionNavigation()}
    </nav>

    <section className="conversation" ref={conversation}>
      <div className="conversation-head"><button ref={sessionDrawerTrigger} type="button" className="icon session-drawer-trigger" aria-label="打开会话列表" aria-expanded={sessionDrawerOpen} aria-controls="session-drawer" onClick={() => setSessionDrawerOpen(true)}><Menu size={18} /></button><div><h2>{canChat ? activeSession?.title || '新对话' : knowledgeBaseResolutionFailed ? '暂时无法确认知识花园' : resolvingKnowledgeBase ? '正在确认知识花园' : '开启知识花园'}</h2><small>{canChat ? activeSession ? '消息会被保存，可以随时回来继续' : '发送第一条消息后自动保存' : knowledgeBaseResolutionFailed ? '重新读取连接状态后再继续' : resolvingKnowledgeBase ? '正在读取当前连接状态' : '完成一次初始化，即可开始对话'}</small></div></div>
      {!canChat ? <div className="chat-onboarding" role={knowledgeBaseState === 'initializing' || resolvingKnowledgeBase ? 'status' : undefined} aria-live="polite"><span><Layers3 size={24} /></span><h3>{knowledgeBaseResolutionFailed ? '暂时无法确认知识花园' : resolvingKnowledgeBase ? '正在确认知识花园状态' : '先开启你的知识花园'}</h3><p>{knowledgeBaseResolutionFailed ? '连接状态读取失败，请重新确认后再开始对话。' : resolvingKnowledgeBase ? '请稍候，正在读取当前知识库连接。' : '知识库用于安全绑定你的个人笔记。完成初始化后即可使用笔记问答和普通对话。'}</p>{knowledgeBaseResolutionFailed ? <button type="button" onClick={() => void onRetryKnowledgeBase()} aria-label="重新确认知识花园状态">重新确认知识花园状态<ChevronRight size={16} /></button> : !resolvingKnowledgeBase && <button type="button" onClick={startKnowledgeBase} disabled={knowledgeBaseState === 'initializing'} aria-label={knowledgeBaseState === 'initializing' ? '正在开启知识花园' : '开启知识花园'}>{knowledgeBaseState === 'initializing' ? '正在开启知识花园' : '开启知识花园'}<ChevronRight size={16} /></button>}</div> : <>
        <div className="message-list" ref={messageList} aria-live="polite" onScroll={() => { const element = messageList.current; if (element) followOutput.current = element.scrollHeight - element.scrollTop - element.clientHeight < 96 }}>{loading ? <div className="conversation-empty">正在加载历史消息...</div> : messages.length === 0 ? <div className="conversation-empty"><Bot size={30} /><h3>从你的笔记开始</h3><p>可以询问过去记录的内容，也可以进行普通对话。</p></div> : messages.map(message => <div className={`message-row ${message.role}`} key={message.id}><div className="avatar">{message.role === 'user' ? <User size={15} /> : <Bot size={16} />}</div><div className="message-body"><div className="message-meta"><b>{message.role === 'user' ? '你' : 'Note Agent'}</b><small>{formatTime(message.created_at)}</small></div><div className={`message-content ${!message.content && chatting ? 'typing' : ''}`} aria-label={message.role === 'assistant' ? 'Note Agent 的回答' : undefined}>{message.content || (chatting ? '正在思考' : '')}</div></div></div>)}<div ref={messageEnd} /></div>
        {retrieval && (evidenceAvailable ? <section className="evidence-summary" aria-label="检索依据摘要"><span>检索依据 · {evidenceCount} 条</span><button ref={evidenceDetailTrigger} type="button" aria-label={`查看 ${evidenceCount} 条检索依据`} aria-expanded={evidenceDetailOpen} aria-controls="evidence-detail" onClick={() => setEvidenceDetailOpen(true)}>查看详情<ChevronRight size={15} /></button></section> : <div className="evidence-status" role="status" aria-label="检索状态">{retrieval.usable ? '未找到可用的检索依据' : `门禁未通过 · ${safeRetrievalReason(retrieval.reason)}`}</div>)}
        {candidate && <section className="note-candidate"><div><small>待确认笔记</small><b>{candidate.title}</b><p>{candidate.content}</p></div><div><button type="button" onClick={() => candidateAction('确认保存')} disabled={chatting}><Check size={15} />确认保存</button><button type="button" className="secondary" onClick={() => candidateAction('取消保存')} disabled={chatting}><X size={15} />取消</button></div></section>}
        <form className="chat-composer" onSubmit={ask}><div>{activeRunID && <small className="run-reference">{chatting ? '运行中' : '最近一次'} Run {activeRunID}</small>}<textarea ref={composer} aria-label="对话内容" placeholder="问问过去的记录，或开始一个新话题..." value={query} onChange={event => setQuery(event.target.value)} required disabled={chatting} onKeyDown={event => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} /></div><button title="发送消息" aria-label="发送消息" disabled={chatting || !query.trim()}><Send size={18} /></button></form>
      </>}
    </section>
    {sessionDrawerOpen && <div className="session-drawer-layer"><button type="button" className="session-drawer-backdrop" aria-label="关闭会话列表" onClick={() => closeSessionDrawer()} /><nav ref={sessionDrawer} id="session-drawer" className="session-drawer" role="dialog" aria-modal="true" aria-labelledby="session-drawer-title" tabIndex={-1}>{sessionNavigation(true)}</nav></div>}
    {evidenceDetailOpen && retrieval && evidenceAvailable && <div className="evidence-detail-layer"><button type="button" className="evidence-detail-backdrop" aria-label="关闭检索依据" onClick={() => closeEvidenceDetail()} /><section ref={evidenceDetail} id="evidence-detail" className="evidence-detail" role="dialog" aria-modal="true" aria-labelledby="evidence-detail-title" tabIndex={-1}><div className="evidence-detail-head"><div><h3 id="evidence-detail-title">检索依据</h3><small>门禁通过 · 命中 {evidenceCount} 条</small></div><button type="button" className="icon" aria-label="关闭" onClick={() => closeEvidenceDetail()}><X size={18} /></button></div><div className="evidence-detail-content">{retrieval.items.map((item, index) => <article className="source" key={`${item.citation.chunk_id}-${index}`}><div className="source-meta"><b>{item.citation.file_name || '未命名来源'}</b><span>相关度 {item.score.toFixed(3)}</span></div><p>{item.content}</p></article>)}</div></section></div>}
  </section>
}
