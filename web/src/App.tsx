import { FormEvent, useEffect, useState } from 'react'
import { BookOpen, Check, LogIn, MessageCircle, NotebookPen, Plus, Trash2, X } from 'lucide-react'
import ChatWorkspace from './ChatWorkspace'
import './knowledgebase.css'
import './app-layout.css'

type Note = { id: string; title: string; content: string; status: string; last_error?: string; tags?: string[] }
type KnowledgeBase = { kb_id: number; name: string; status: string }
type KnowledgeBaseState = { configured: boolean; created?: boolean; knowledge_base?: KnowledgeBase }
type WorkspaceView = 'notes' | 'chat'
const api = async (path: string, init?: RequestInit) => fetch(path, { credentials: 'include', ...init }).then(async r => { const body = await r.json().catch(() => ({})); if (!r.ok) throw new Error(body?.error?.message || body?.message || '请求失败'); return body })

export default function App() {
  const [loggedIn, setLoggedIn] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [notes, setNotes] = useState<Note[]>([])
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [error, setError] = useState('')
  const [knowledgeBase, setKnowledgeBase] = useState<KnowledgeBaseState | null>(null)
  const [initializingKB, setInitializingKB] = useState(false)
  const [confirmDeleteID, setConfirmDeleteID] = useState('')
  const [deletingNoteIDs, setDeletingNoteIDs] = useState<Set<string>>(() => new Set())
  const [view, setView] = useState<WorkspaceView>(() => window.location.hash === '#chat' ? 'chat' : 'notes')

  const loadNotes = () => api('/v1/notes').then(r => setNotes(r.items || [])).catch(e => setError(e.message))
  const loadKnowledgeBase = () => api('/v1/knowledge-base').then(r => setKnowledgeBase(r)).catch(e => setError(e.message))
  useEffect(() => { api('/v1/auth/me').then(() => { setLoggedIn(true); void Promise.all([loadNotes(), loadKnowledgeBase()]) }).catch(() => undefined) }, [])
  useEffect(() => {
    const syncView = () => setView(window.location.hash === '#chat' ? 'chat' : 'notes')
    window.addEventListener('hashchange', syncView)
    return () => window.removeEventListener('hashchange', syncView)
  }, [])

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
  function navigate(next: WorkspaceView) {
    window.location.hash = next
    setView(next)
  }
  if (!loggedIn) return <main className="auth"><form onSubmit={login}><BookOpen size={28}/><h1>Note Agent</h1><input aria-label="邮箱" placeholder="邮箱" value={email} onChange={e=>setEmail(e.target.value)} /><input aria-label="密码" type="password" placeholder="密码" value={password} onChange={e=>setPassword(e.target.value)} /><button><LogIn size={16}/>登录</button>{error && <p className="error">{error}</p>}</form></main>
  return <main className="app-shell">
    <header className="app-header">
      <div className="brand"><BookOpen size={22}/><strong>Note Agent</strong></div>
      <nav className="app-nav" aria-label="工作台导航">
        <button type="button" className={view === 'notes' ? 'active' : ''} onClick={() => navigate('notes')}><NotebookPen size={16}/>笔记</button>
        <button type="button" className={view === 'chat' ? 'active' : ''} onClick={() => navigate('chat')}><MessageCircle size={16}/>对话</button>
      </nav>
      <span className="workspace-name">个人工作台</span>
    </header>

    <section className="knowledge-base">
      <div className="kb-state"><span className={`kb-indicator ${knowledgeBase?.configured ? 'ready' : ''}`}/><div><b>个人知识库</b>{knowledgeBase?.configured && knowledgeBase.knowledge_base ? <small>{knowledgeBase.knowledge_base.name} · KB {knowledgeBase.knowledge_base.kb_id} · {knowledgeBase.knowledge_base.status}</small> : <small>尚未创建，创建后即可保存并检索笔记</small>}</div></div>
      {!knowledgeBase?.configured && <button type="button" onClick={initializeKnowledgeBase} disabled={initializingKB}><Plus size={16}/>{initializingKB?'创建中':'创建并绑定'}</button>}
    </section>

    {view === 'notes' ? <section className="notes-view">
      <div className="page-heading"><div><h1>笔记</h1><p>记录重要内容，并同步到你的个人知识库。</p></div><span>{notes.length} 条笔记</span></div>
      <div className="notes-layout">
        <form className="note-editor" onSubmit={createNote}>
          <div className="section-heading"><div><h2>记一笔</h2><small>保存后将自动建立检索索引</small></div></div>
          <label>标题<input placeholder="给这条笔记一个标题" value={title} onChange={e=>setTitle(e.target.value)} required disabled={!knowledgeBase?.configured}/></label>
          <label>内容<textarea placeholder="今天记录了什么？" value={content} onChange={e=>setContent(e.target.value)} required disabled={!knowledgeBase?.configured}/></label>
          <div className="editor-actions"><button disabled={!knowledgeBase?.configured}><Plus size={16}/>保存笔记</button></div>
        </form>
        <section className="note-library">
          <div className="library-head"><div><h2>我的笔记</h2><small>最近保存的内容</small></div></div>
          {notes.length === 0 ? <div className="notes-empty"><NotebookPen size={26}/><b>还没有笔记</b><small>在左侧记录第一条内容</small></div> : <div className="note-grid">{notes.map(n=><article className="note-card" key={n.id}><div className="note-card-head"><div><b>{n.title}</b><small>{deletingNoteIDs.has(n.id)?'删除中':n.status}</small></div><div className="note-actions">{confirmDeleteID === n.id ? <><button type="button" className="icon danger" title="确认删除" aria-label="确认删除" disabled={deletingNoteIDs.has(n.id)} onClick={()=>remove(n.id)}><Check size={16}/></button><button type="button" className="icon" title="取消删除" aria-label="取消删除" onClick={()=>setConfirmDeleteID('')}><X size={16}/></button></> : <button type="button" className="icon" title="删除笔记" aria-label="删除笔记" disabled={deletingNoteIDs.has(n.id)} onClick={()=>setConfirmDeleteID(n.id)}><Trash2 size={16}/></button>}</div></div><p>{n.content}</p></article>)}</div>}
        </section>
      </div>
    </section> : <section className="chat-view">
      <div className="page-heading"><div><h1>对话</h1><p>继续历史会话，或基于你的笔记发起新问题。</p></div></div>
      <ChatWorkspace enabled={Boolean(knowledgeBase?.configured)} onError={setError}/>
    </section>}
    {error && <div className="error toast"><span>{error}</span><button type="button" className="icon" aria-label="关闭错误" onClick={() => setError('')}><X size={15}/></button></div>}
  </main>
}
