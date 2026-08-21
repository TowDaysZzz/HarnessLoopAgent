import { FormEvent, useEffect, useRef, useState } from 'react'
import { Bot, Check, Clock3, MessageSquare, Plus, Send, User, X } from 'lucide-react'
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
  enabled: boolean
  onError: (message: string) => void
  onNoteChanged?: () => void | Promise<void>
}

const api = async (path: string, init?: RequestInit) => fetch(path, { credentials: 'include', ...init }).then(async response => {
  const body = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(body?.error?.message || body?.message || '请求失败')
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

export default function ChatWorkspace({ enabled, onError, onNoteChanged }: Props) {
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
  const requestSequence = useRef(0)
  const messageEnd = useRef<HTMLDivElement>(null)
  const messageList = useRef<HTMLDivElement>(null)
  const composer = useRef<HTMLTextAreaElement>(null)
  const followOutput = useRef(true)

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
    setSessions(items)
    return items
  }

  async function loadInitial() {
    try {
      const items = await refreshSessions()
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
      setLoading(false)
      onError((reason as Error).message)
    }
  }

  useEffect(() => { void loadInitial() }, [])
  useEffect(() => {
    if (followOutput.current) messageEnd.current?.scrollIntoView({ behavior: chatting ? 'smooth' : 'auto', block: 'end' })
  }, [messages, chatting])

  async function newChat() {
    if (chatting || creatingSession) return
    setCreatingSession(true)
    onError('')
    requestSequence.current++
    try {
      const session = await api('/v1/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: `新会话 ${new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}` }) }) as ChatSession
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
      onError((reason as Error).message)
    } finally {
      setCreatingSession(false)
    }
  }

  async function selectSession(id: string) {
    if (chatting || id === sessionID) return
    setSessionID(id)
    setCandidate(null)
    setActiveRunID('')
    setRetrieval(null)
    followOutput.current = true
    window.localStorage.setItem('note-agent-session', id)
    await loadMessages(id)
  }

  async function runQuestion(rawQuestion: string) {
    const question = rawQuestion.trim()
    if (!question) return
    const candidateCommand = question === '确认保存' || question === '取消保存'
    onError('')
    setRetrieval(null)
    if (!candidateCommand) setCandidate(null)
    setChatting(true)
    followOutput.current = true
    try {
      let activeID = sessionID
      if (!activeID) {
        const session = await api('/v1/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: makeTitle(question) }) }) as ChatSession
        activeID = session.id
        setSessionID(activeID)
        setSessions(value => [session, ...value])
        window.localStorage.setItem('note-agent-session', activeID)
      }
      const userMessage: ChatMessage = { id: crypto.randomUUID(), session_id: activeID, sequence: messages.length + 1, role: 'user', content: question, created_at: new Date().toISOString() }
      const assistantMessage: ChatMessage = { id: `stream-${crypto.randomUUID()}`, session_id: activeID, sequence: messages.length + 2, role: 'assistant', content: '', created_at: new Date().toISOString() }
      setMessages(value => [...value, userMessage, assistantMessage])
      setQuery('')
      const run = await api(`/v1/sessions/${activeID}/runs`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ message: question }) })
      const runID = run.run.id as string
      setActiveRunID(runID)
      const source = new EventSource(`/v1/runs/${runID}/events`, { withCredentials: true })
      let terminal = false
      let checkingStream = false

      const finish = async () => {
        if (terminal) return
        terminal = true
        source.close()
        setChatting(false)
        if (candidateCommand) setCandidate(null)
        await loadMessages(activeID, false)
        await refreshSessions()
        await onNoteChanged?.()
      }

      const fail = async (fallback: string) => {
        if (terminal) return
        terminal = true
        source.close()
        setChatting(false)
        try {
          const failedRun = await api(`/v1/runs/${runID}`)
          onError(`${failedRun.error_message || fallback}（Run ID: ${runID}）`)
        } catch (reason) {
          onError(`${(reason as Error).message}（Run ID: ${runID}）`)
        }
      }

      source.addEventListener('text.delta', eventValue => {
        const data = JSON.parse((eventValue as MessageEvent).data)
        if (data.content) setMessages(value => value.map(message => message.id === assistantMessage.id ? { ...message, content: message.content + data.content } : message))
      })
      source.addEventListener('tool.completed', eventValue => {
        const data = JSON.parse((eventValue as MessageEvent).data)
        if (data.tool !== 'semantic_search_notes' || !data.summary) return
        try {
          setRetrieval(JSON.parse(data.summary) as RetrievalResult)
        } catch {
          onError('检索结果格式无效')
        }
      })
      source.addEventListener('note.draft.candidate', eventValue => {
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
        if (terminal || checkingStream) return
        checkingStream = true
        try {
          const currentRun = await api(`/v1/runs/${runID}`)
          if (currentRun.status === 'completed') await finish()
          else if (['failed', 'timed_out', 'cancelled', 'interrupted'].includes(currentRun.status)) await fail('Agent 流式连接意外中断')
          // queued/running keeps EventSource open so the browser reconnects with Last-Event-ID.
        } catch (reason) {
          source.close()
          terminal = true
          setChatting(false)
          onError(`${(reason as Error).message}（Run ID: ${runID}）`)
        } finally {
          checkingStream = false
        }
      }
    } catch (reason) {
      setChatting(false)
      setActiveRunID('')
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
  return <section className="chat-workspace">
    <nav className="session-sidebar" aria-label="对话历史">
      <div className="session-head"><div><h2>对话历史</h2><small>{sessions.length} 个会话</small></div><button type="button" className="icon new-chat" title="新建对话" aria-label="新建对话" onClick={() => void newChat()} disabled={chatting || creatingSession}><Plus size={18} /></button></div>
      <div className="session-list">{sessions.map(session => <button type="button" className={`session-item ${session.id === sessionID ? 'active' : ''}`} key={session.id} onClick={() => selectSession(session.id)} disabled={chatting}><MessageSquare size={15} /><span><b>{session.title}</b><small><Clock3 size={11} />{formatTime(session.updated_at)}</small></span></button>)}{sessions.length === 0 && <div className="empty-history">还没有历史对话</div>}</div>
    </nav>

    <section className="conversation">
      <div className="conversation-head"><div><h2>{activeSession?.title || '新对话'}</h2><small>{activeSession ? '消息会被保存，可以随时回来继续' : '发送第一条消息后自动保存'}</small></div></div>
      <div className="message-list" ref={messageList} aria-live="polite" onScroll={() => { const element = messageList.current; if (element) followOutput.current = element.scrollHeight - element.scrollTop - element.clientHeight < 96 }}>{loading ? <div className="conversation-empty">正在加载历史消息...</div> : messages.length === 0 ? <div className="conversation-empty"><Bot size={30} /><h3>从你的笔记开始</h3><p>可以询问过去记录的内容，也可以进行普通对话。</p></div> : messages.map(message => <div className={`message-row ${message.role}`} key={message.id}><div className="avatar">{message.role === 'user' ? <User size={15} /> : <Bot size={16} />}</div><div className="message-body"><div className="message-meta"><b>{message.role === 'user' ? '你' : 'Note Agent'}</b><small>{formatTime(message.created_at)}</small></div><div className={`message-content ${!message.content && chatting ? 'typing' : ''}`}>{message.content || (chatting ? '正在思考' : '')}</div></div></div>)}<div ref={messageEnd} /></div>
      {retrieval && <section className="evidence"><div className="evidence-head"><h3>检索依据</h3><small>{retrieval.usable ? `门禁通过 · 命中 ${retrieval.item_count} 条` : `门禁未通过${retrieval.reason ? ` · ${retrieval.reason}` : ''}`}</small></div>{retrieval.items.map((item, index) => <div className="source" key={`${item.citation.chunk_id}-${index}`}><div className="source-meta"><b>{item.citation.file_name || '未命名来源'}</b><span>相关度 {item.score.toFixed(3)}</span></div><p>{item.content}</p><small>KB {item.citation.kb_id} · 文档 {item.citation.document_id} · Chunk {item.citation.chunk_id}{item.citation.chunk_index >= 0 ? ` · #${item.citation.chunk_index}` : ''}</small></div>)}</section>}
      {candidate && <section className="note-candidate"><div><small>待确认笔记</small><b>{candidate.title}</b><p>{candidate.content}</p></div><div><button type="button" onClick={() => candidateAction('确认保存')} disabled={chatting}><Check size={15}/>确认保存</button><button type="button" className="secondary" onClick={() => candidateAction('取消保存')} disabled={chatting}><X size={15}/>取消</button></div></section>}
      <form className="chat-composer" onSubmit={ask}><div>{activeRunID && <small className="run-reference">{chatting ? '运行中' : '最近一次'} Run {activeRunID}</small>}<textarea ref={composer} aria-label="对话内容" placeholder="问问过去的记录，或开始一个新话题..." value={query} onChange={event => setQuery(event.target.value)} required disabled={chatting || !enabled} onKeyDown={event => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} /></div><button title="发送消息" aria-label="发送消息" disabled={chatting || !enabled || !query.trim()}><Send size={18} /></button></form>
    </section>
  </section>
}
