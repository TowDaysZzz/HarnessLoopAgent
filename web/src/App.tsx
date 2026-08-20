import { FormEvent, useEffect, useState } from 'react'
import { BookOpen, Check, LogIn, Plus, Send, Trash2, X } from 'lucide-react'
import './knowledgebase.css'

type Note = { id: string; title: string; content: string; status: string; last_error?: string; tags?: string[] }
type RetrievalItem = {
  content: string
  score: number
  citation: { kb_id: number; document_id: number; chunk_id: string; file_name: string; chunk_index: number }
}
type RetrievalResult = { usable: boolean; reason?: string; request_id?: string; item_count: number; items: RetrievalItem[] }
type KnowledgeBase = { kb_id: number; name: string; status: string }
type KnowledgeBaseState = { configured: boolean; created?: boolean; knowledge_base?: KnowledgeBase }
const api = async (path: string, init?: RequestInit) => fetch(path, { credentials: 'include', ...init }).then(async r => { const body = await r.json().catch(() => ({})); if (!r.ok) throw new Error(body?.error?.message || body?.message || '请求失败'); return body })

export default function App() {
  const [loggedIn, setLoggedIn] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [notes, setNotes] = useState<Note[]>([])
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [query, setQuery] = useState('')
  const [answer, setAnswer] = useState('')
  const [retrieval, setRetrieval] = useState<RetrievalResult | null>(null)
  const [chatSessionID, setChatSessionID] = useState('')
  const [chatting, setChatting] = useState(false)
  const [error, setError] = useState('')
  const [knowledgeBase, setKnowledgeBase] = useState<KnowledgeBaseState | null>(null)
  const [initializingKB, setInitializingKB] = useState(false)
  const [confirmDeleteID, setConfirmDeleteID] = useState('')
  const [deletingNoteIDs, setDeletingNoteIDs] = useState<Set<string>>(() => new Set())

  const loadNotes = () => api('/v1/notes').then(r => setNotes(r.items || [])).catch(e => setError(e.message))
  const loadKnowledgeBase = () => api('/v1/knowledge-base').then(r => setKnowledgeBase(r)).catch(e => setError(e.message))
  useEffect(() => { api('/v1/auth/me').then(() => { setLoggedIn(true); void Promise.all([loadNotes(), loadKnowledgeBase()]) }).catch(() => undefined) }, [])

  async function login(e: FormEvent) { e.preventDefault(); setError(''); try { await api('/v1/auth/login', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({email, password}) }); setLoggedIn(true); await Promise.all([loadNotes(), loadKnowledgeBase()]) } catch (err) { setError((err as Error).message) } }
  async function initializeKnowledgeBase() { setInitializingKB(true); setError(''); try { const value = await api('/v1/knowledge-base', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:'我的笔记'})}); setKnowledgeBase(value) } catch (err) { setError((err as Error).message) } finally { setInitializingKB(false) } }
  async function createNote(e: FormEvent) { e.preventDefault(); try { await api('/v1/notes', {method:'POST', headers:{'Content-Type':'application/json','Idempotency-Key':crypto.randomUUID()}, body:JSON.stringify({title,content})}); setTitle(''); setContent(''); await loadNotes() } catch (err) { setError((err as Error).message) } }
  async function remove(id: string) {
    setError('')
    setConfirmDeleteID('')
    setDeletingNoteIDs(value => new Set(value).add(id))
    try {
      const result = await api(`/v1/notes/${id}`, {method:'DELETE', headers:{'Idempotency-Key':crypto.randomUUID()}})
      setNotes(value => value.map(note => note.id === id ? result.note : note))
      for (let attempt = 0; attempt < 12; attempt++) {
        const current = await api(result.status_url || `/v1/notes/${id}/status`)
        if (current.status === 'deleted') {
          setNotes(value => value.filter(note => note.id !== id))
          return
        }
        if (current.last_error) {
          setError(`删除失败：${current.last_error}`)
          setNotes(value => value.map(note => note.id === id ? current : note))
          return
        }
        await new Promise(resolve => window.setTimeout(resolve, 500))
      }
      await loadNotes()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setDeletingNoteIDs(value => { const next = new Set(value); next.delete(id); return next })
    }
  }
  async function ask(e: FormEvent) {
    e.preventDefault()
    setAnswer('')
    setRetrieval(null)
    setError('')
    setChatting(true)
    try {
      let sessionID = chatSessionID
      if (!sessionID) {
        const session = await api('/v1/sessions', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title:'工作台对话'})})
        sessionID = session.id
        setChatSessionID(sessionID)
      }
      const run = await api(`/v1/sessions/${sessionID}/runs`, {method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':crypto.randomUUID()},body:JSON.stringify({message:query})})
      const source = new EventSource(`/v1/runs/${run.run.id}/events`, {withCredentials:true})
      source.addEventListener('text.delta', event => {
        const data = JSON.parse((event as MessageEvent).data)
        if (data.content) setAnswer(value => value + data.content)
      })
      source.addEventListener('tool.completed', event => {
        const data = JSON.parse((event as MessageEvent).data)
        if (data.tool !== 'semantic_search_notes' || !data.summary) return
        try {
          setRetrieval(JSON.parse(data.summary) as RetrievalResult)
        } catch {
          setError('检索结果格式无效')
        }
      })
      source.addEventListener('run.completed', () => { setChatting(false); source.close() })
      source.addEventListener('run.failed', async () => {
        setChatting(false)
        source.close()
        try {
          const failedRun = await api(`/v1/runs/${run.run.id}`)
          setError(failedRun.error_message || 'Agent 运行失败')
        } catch (err) {
          setError((err as Error).message)
        }
      })
      source.onerror = () => { setChatting(false); source.close() }
      setQuery('')
    } catch (err) {
      setChatting(false)
      setError((err as Error).message)
    }
  }

  if (!loggedIn) return <main className="auth"><form onSubmit={login}><BookOpen size={28}/><h1>Note Agent</h1><input aria-label="邮箱" placeholder="邮箱" value={email} onChange={e=>setEmail(e.target.value)} /><input aria-label="密码" type="password" placeholder="密码" value={password} onChange={e=>setPassword(e.target.value)} /><button><LogIn size={16}/>登录</button>{error && <p className="error">{error}</p>}</form></main>
  return <main><header><div><BookOpen size={22}/><strong>Note Agent</strong></div><span>个人工作台</span></header><section className="grid"><section className="panel knowledge-base"><div><h2>个人知识库</h2>{knowledgeBase?.configured && knowledgeBase.knowledge_base ? <small>{knowledgeBase.knowledge_base.name} · KB {knowledgeBase.knowledge_base.kb_id} · {knowledgeBase.knowledge_base.status}</small> : <small>尚未创建，笔记会在创建后写入你的独立知识库</small>}</div>{!knowledgeBase?.configured && <button type="button" onClick={initializeKnowledgeBase} disabled={initializingKB}><Plus size={16}/>{initializingKB?'创建中':'创建并绑定'}</button>}</section><form className="panel" onSubmit={createNote}><h2>记一笔</h2><input placeholder="标题" value={title} onChange={e=>setTitle(e.target.value)} required disabled={!knowledgeBase?.configured}/><textarea placeholder="今天记录了什么？" value={content} onChange={e=>setContent(e.target.value)} required disabled={!knowledgeBase?.configured}/><button disabled={!knowledgeBase?.configured}><Plus size={16}/>保存笔记</button></form><section className="panel"><h2>我的笔记</h2>{notes.length === 0 && <small>暂无笔记</small>}{notes.map(n=><article key={n.id}><div><b>{n.title}</b><small>{deletingNoteIDs.has(n.id)?'删除中':n.status}</small><p>{n.content}</p></div><div className="note-actions">{confirmDeleteID === n.id ? <><button className="icon danger" title="确认删除" aria-label="确认删除" disabled={deletingNoteIDs.has(n.id)} onClick={()=>remove(n.id)}><Check size={16}/></button><button className="icon" title="取消删除" aria-label="取消删除" onClick={()=>setConfirmDeleteID('')}><X size={16}/></button></> : <button className="icon" title="删除笔记" aria-label="删除笔记" disabled={deletingNoteIDs.has(n.id)} onClick={()=>setConfirmDeleteID(n.id)}><Trash2 size={16}/></button>}</div></article>)}</section><form className="panel chat" onSubmit={ask}><h2>问问过去的记录</h2><textarea placeholder="例如：我之前关于垃圾回收记了什么？" value={query} onChange={e=>setQuery(e.target.value)} required disabled={chatting || !knowledgeBase?.configured}/><button disabled={chatting || !knowledgeBase?.configured}><Send size={16}/>{chatting?'回答中':'发送'}</button>{answer && <pre>{answer}</pre>}{retrieval && <section className="evidence"><div className="evidence-head"><h3>检索依据</h3><small>{retrieval.usable ? `门禁通过 · 命中 ${retrieval.item_count} 条` : `门禁未通过${retrieval.reason ? ` · ${retrieval.reason}` : ''}`}</small></div>{retrieval.items.map((item, index)=><div className="source" key={`${item.citation.chunk_id}-${index}`}><div className="source-meta"><b>{item.citation.file_name || '未命名来源'}</b><span>相关度 {item.score.toFixed(3)}</span></div><p>{item.content}</p><small>KB {item.citation.kb_id} · 文档 {item.citation.document_id} · Chunk {item.citation.chunk_id}{item.citation.chunk_index >= 0 ? ` · #${item.citation.chunk_index}` : ''}</small></div>)}</section>}</form></section>{error && <p className="error toast">{error}</p>}</main>
}
