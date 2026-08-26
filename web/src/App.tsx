import { FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowUpRight, BookOpen, Check, ChevronRight, CircleUserRound, Clock3,
  Command, Feather, Layers3, LogIn, MessageCircle,
  MoreHorizontal, NotebookPen, Plus, Search, Send, Sparkles, X,
} from 'lucide-react'
import ChatWorkspace from './ChatWorkspace'
import './knowledgebase.css'
import './app-layout.css'

type Note = { id: string; title: string; content: string; status: string; last_error?: string; tags?: string[]; updated_at?: string }
type KnowledgeBase = { kb_id: number; name: string; status: string }
type KnowledgeBaseState = { configured: boolean; created?: boolean; knowledge_base?: KnowledgeBase }
type WorkspaceView = 'notes' | 'chat'

const formatWorkspaceDate = (date = new Date()) => {
  const weekdays = ['日', '一', '二', '三', '四', '五', '六']
  return `星期${weekdays[date.getDay()]} · ${date.getMonth() + 1}月${date.getDate()}日`
}

const api = async (path: string, init?: RequestInit) => fetch(path, { credentials: 'include', ...init }).then(async r => {
  const body = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(body?.error?.message || body?.message || '请求失败')
  return body
})

const demoNotes: Note[] = [
  { id: 'demo-1', title: '让知识真正流动起来', content: '好的笔记系统不是一座仓库，而是一张不断生长的思维网络。记录只是起点，重新遇见、建立联系和转化行动才是终点。', status: 'indexed', tags: ['产品思考', '知识管理'], updated_at: '今天 09:42' },
  { id: 'demo-2', title: '周三项目同步', content: '本周完成搜索体验的第一轮验证。下一步聚焦新用户的空状态，让第一次记录自然发生，同时减少不必要的设置项。', status: 'indexed', tags: ['会议', '工作'], updated_at: '昨天 16:20' },
  { id: 'demo-3', title: '读《打造第二大脑》', content: '不必在记录时就决定一条信息最终属于哪里。先捕获，再通过渐进式整理发现结构。行动导向比完美分类更重要。', status: 'indexed', tags: ['阅读', '灵感'], updated_at: '8月23日' },
  { id: 'demo-4', title: '关于慢下来的练习', content: '留出没有输入的时间。散步、写晨间页，在答案出现前容许问题多停留一会儿。清晰往往来自空白，而不是更多信息。', status: 'indexed', tags: ['随想'], updated_at: '8月21日' },
]

function DemoConversation() {
  const [query, setQuery] = useState('')
  const demoThreads = [
    { title: '知识管理的新想法', messages: [
      { role: 'user', content: '我最近关于知识管理有什么新的想法？' },
      { role: 'assistant', content: '你最近的思考有一个很清晰的变化：从“如何把笔记分类好”，转向了“如何让笔记推动行动”。\n\n你在《让知识真正流动起来》和《打造第二大脑》的读书笔记里都提到，记录只是起点，重新遇见和转化才是重点。或许可以把下一步实验定为：每周主动回顾 3 条旧笔记，并为其中 1 条创建具体行动。' },
    ] },
    { title: '本周项目回顾', messages: [
      { role: 'user', content: '帮我回顾一下本周项目进展。' },
      { role: 'assistant', content: '本周你完成了搜索体验的第一轮验证。下一步重点是优化新用户空状态，并减少首次使用时不必要的设置。' },
    ] },
    { title: '最近读过的书', messages: [
      { role: 'user', content: '我最近读书时记住了什么？' },
      { role: 'assistant', content: '你在《打造第二大脑》的笔记里留下了一个核心判断：先捕获，再渐进式整理；行动导向比完美分类更重要。' },
    ] },
  ]
  const [activeThread, setActiveThread] = useState(0)
  const [messages, setMessages] = useState(demoThreads[0].messages)
  function selectThread(index: number) {
    setActiveThread(index)
    setMessages(demoThreads[index].messages)
    setQuery('')
  }
  function ask(e: FormEvent) {
    e.preventDefault()
    if (!query.trim()) return
    setMessages(value => [...value, { role: 'user', content: query.trim() }, { role: 'assistant', content: '这是一个演示回答。连接真实服务后，Mori 会检索你的个人知识库，并带着来源回答这个问题。' }])
    setQuery('')
  }
  return <section className="demo-conversation">
    <aside><div><Plus size={15} />演示会话</div>{demoThreads.map((thread, index) => <button key={thread.title} className={index === activeThread ? 'active' : ''} aria-current={index === activeThread ? 'page' : undefined} onClick={() => selectThread(index)}>{thread.title}</button>)}</aside>
    <div className="demo-thread"><header><div><span><Sparkles size={16} /></span><div><b>Mori</b><small>正在使用 4 条相关笔记</small></div></div></header><div className="demo-messages">{messages.map((message, index) => <div className={`demo-message ${message.role}`} key={index}><small>{message.role === 'user' ? '你' : 'Mori'}</small><p>{message.content}</p>{message.role === 'assistant' && <div className="demo-sources"><span>来源 2</span><span>让知识真正流动起来</span><span>读《打造第二大脑》</span></div>}</div>)}</div><form onSubmit={ask}><textarea aria-label="演示提问" placeholder="继续追问你的笔记..." value={query} onChange={e => setQuery(e.target.value)} /><button aria-label="发送"><Send size={16} /></button></form></div>
  </section>
}

export default function App() {
  const [loggedIn, setLoggedIn] = useState(false)
  const [isDemo, setIsDemo] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [notes, setNotes] = useState<Note[]>([])
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [search, setSearch] = useState('')
  const [error, setError] = useState('')
  const [knowledgeBase, setKnowledgeBase] = useState<KnowledgeBaseState | null>(null)
  const [knowledgeBaseResolved, setKnowledgeBaseResolved] = useState(false)
  const [knowledgeBaseResolutionFailed, setKnowledgeBaseResolutionFailed] = useState(false)
  const [initializingKB, setInitializingKB] = useState(false)
  const [confirmDeleteID, setConfirmDeleteID] = useState('')
  const [deletingNoteIDs, setDeletingNoteIDs] = useState<Set<string>>(() => new Set())
  const [creatingNote, setCreatingNote] = useState(false)
  const [notice, setNotice] = useState('')
  const [view, setView] = useState<WorkspaceView>(() => window.location.hash === '#chat' ? 'chat' : 'notes')
  const searchInput = useRef<HTMLInputElement>(null)
  const mounted = useRef(true)
  const knowledgeBaseRequest = useRef(0)

  const loadNotes = () => api('/v1/notes').then(r => setNotes(r.items || [])).catch(e => setError(e.message))
  const loadKnowledgeBase = async () => {
    const request = ++knowledgeBaseRequest.current
    setKnowledgeBaseResolved(false)
    setKnowledgeBaseResolutionFailed(false)
    try {
      const value = await api('/v1/knowledge-base')
      if (mounted.current && request === knowledgeBaseRequest.current) {
        setKnowledgeBase(value)
        setKnowledgeBaseResolved(true)
      }
    } catch (reason) {
      if (mounted.current && request === knowledgeBaseRequest.current) {
        setKnowledgeBaseResolutionFailed(true)
        setError((reason as Error).message)
      }
    }
  }

  useEffect(() => {
    api('/v1/auth/me').then(() => {
      setLoggedIn(true)
      void Promise.all([loadNotes(), loadKnowledgeBase()])
    }).catch(() => undefined)
  }, [])

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      knowledgeBaseRequest.current++
    }
  }, [])

  useEffect(() => {
    const syncView = () => setView(window.location.hash === '#chat' ? 'chat' : 'notes')
    window.addEventListener('hashchange', syncView)
    return () => window.removeEventListener('hashchange', syncView)
  }, [])

  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        searchInput.current?.focus()
      }
    }
    window.addEventListener('keydown', focusSearch)
    return () => window.removeEventListener('keydown', focusSearch)
  }, [])

  const filteredNotes = useMemo(() => {
    const query = search.trim().toLowerCase()
    if (!query) return notes
    return notes.filter(note => [note.title, note.content, ...(note.tags || [])].join(' ').toLowerCase().includes(query))
  }, [notes, search])

  async function login(e: FormEvent) {
    e.preventDefault()
    setError('')
    try {
      await api('/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email, password }) })
      setLoggedIn(true)
      await Promise.all([loadNotes(), loadKnowledgeBase()])
    } catch (err) { setError((err as Error).message) }
  }

  function enterDemo() {
    setIsDemo(true)
    setLoggedIn(true)
    setNotes(demoNotes)
    setKnowledgeBase({ configured: true, knowledge_base: { kb_id: 7, name: '我的知识花园', status: 'ready' } })
    setKnowledgeBaseResolved(true)
    setKnowledgeBaseResolutionFailed(false)
  }

  async function initializeKnowledgeBase() {
    if (initializingKB || !knowledgeBaseResolved) return
    const request = ++knowledgeBaseRequest.current
    setInitializingKB(true)
    setError('')
    try {
      const value = await api('/v1/knowledge-base', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: '我的笔记' }) })
      if (mounted.current && request === knowledgeBaseRequest.current) {
        setKnowledgeBase(value)
        setKnowledgeBaseResolved(true)
        setKnowledgeBaseResolutionFailed(false)
      }
    } catch (err) {
      if (mounted.current && request === knowledgeBaseRequest.current) setError((err as Error).message)
    } finally {
      if (mounted.current && request === knowledgeBaseRequest.current) setInitializingKB(false)
    }
  }

  function knowledgeBaseRequired() {
    knowledgeBaseRequest.current++
    setInitializingKB(false)
    setKnowledgeBase({ configured: false })
    setKnowledgeBaseResolved(true)
    setKnowledgeBaseResolutionFailed(false)
  }

  async function createNote(e: FormEvent) {
    e.preventDefault()
    setError('')
    setNotice('')
    setCreatingNote(true)
    if (isDemo) {
      const next: Note = { id: crypto.randomUUID(), title, content, status: 'indexed', tags: ['新笔记'], updated_at: '刚刚' }
      window.setTimeout(() => {
        setNotes(value => [next, ...value])
        setTitle('')
        setContent('')
        setNotice('笔记已保存到演示空间。')
        setCreatingNote(false)
      }, 420)
      return
    }
    try {
      const result = await api('/v1/notes', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ title, content }) })
      setTitle('')
      setContent('')
      setNotes(value => [result.note, ...value.filter(note => note.id !== result.note.id)])
      for (let attempt = 0; attempt < 20; attempt++) {
        const current = await api(result.status_url || `/v1/notes/${result.note.id}/status`)
        setNotes(value => value.map(note => note.id === current.id ? current : note))
        if (current.status === 'indexed') { setNotice('笔记已保存并完成知识索引。'); return }
        if (current.status === 'index_failed' || current.last_error) { setError(`笔记已保存，但建立索引失败：${current.last_error || '未知错误'}`); return }
        await new Promise(resolve => window.setTimeout(resolve, 500))
      }
      setNotice('笔记已保存，知识索引仍在后台处理中。')
      await loadNotes()
    } catch (err) { setError((err as Error).message) } finally { setCreatingNote(false) }
  }

  const noteStatus = (status: string) => ({ indexed: '已连接', indexing: '连接中', index_failed: '索引失败', delete_pending: '删除中' }[status] || status)

  async function remove(id: string) {
    setError('')
    setConfirmDeleteID('')
    if (isDemo) {
      setNotes(value => value.filter(note => note.id !== id))
      setNotice('笔记已删除。')
      return
    }
    setDeletingNoteIDs(value => new Set(value).add(id))
    try {
      const result = await api(`/v1/notes/${id}`, { method: 'DELETE', headers: { 'Idempotency-Key': crypto.randomUUID() } })
      setNotes(value => value.map(note => note.id === id ? result.note : note))
      for (let attempt = 0; attempt < 12; attempt++) {
        const current = await api(result.status_url || `/v1/notes/${id}/status`)
        if (current.status === 'deleted') {
          setNotes(value => value.filter(note => note.id !== id))
          setNotice('笔记已删除。')
          return
        }
        if (current.last_error) { setError(`删除失败：${current.last_error}`); setNotes(value => value.map(note => note.id === id ? current : note)); return }
        await new Promise(resolve => window.setTimeout(resolve, 500))
      }
      await loadNotes()
    } catch (err) { setError((err as Error).message) } finally {
      setDeletingNoteIDs(value => { const next = new Set(value); next.delete(id); return next })
    }
  }

  function navigate(next: WorkspaceView) {
    window.location.hash = next
    setView(next)
  }

  if (!loggedIn) return <main className="auth-page">
    <section className="auth-story">
      <div className="auth-brand"><span><Feather size={20} /></span><b>mori</b></div>
      <div className="auth-quote"><Sparkles size={22} /><h1>把散落的想法，<br />慢慢长成你的知识花园。</h1><p>记录、连接、回顾。让 AI 帮你重新遇见那些值得记住的事情。</p></div>
      <div className="auth-note"><span>“</span><p>我们写下的每一条笔记，<br />都在悄悄塑造未来的自己。</p></div>
    </section>
    <section className="auth-panel">
      <form onSubmit={login}>
        <div className="auth-mobile-brand"><Feather size={19} /><b>mori</b></div>
        <p className="eyebrow">欢迎回来</p>
        <h2>继续你的思考</h2>
        <p className="auth-intro">登录个人空间，继续记录今天。</p>
        <label>邮箱<input aria-label="邮箱" placeholder="name@example.com" value={email} onChange={e => setEmail(e.target.value)} /></label>
        <label>密码<input aria-label="密码" type="password" placeholder="输入密码" value={password} onChange={e => setPassword(e.target.value)} /></label>
        <button className="auth-submit"><span>登录</span><LogIn size={17} /></button>
        <div className="auth-divider"><span>或</span></div>
        <button type="button" className="demo-button" onClick={enterDemo}><Sparkles size={17} />体验交互 Demo<ArrowUpRight size={16} /></button>
        {error && <div className="auth-error error" role="alert"><span>{error}</span><button type="button" className="icon" aria-label="关闭错误" onClick={() => setError('')}><X size={15} /></button></div>}
      </form>
    </section>
  </main>

  return <main className={`workspace-shell ${view === 'chat' ? 'is-chat' : ''}`}>
    <aside className="workspace-sidebar">
      <div className="sidebar-brand"><span><Feather size={18} /></span><b>mori</b></div>
      <nav aria-label="工作台导航">
        <button aria-label="我的笔记" aria-current={view === 'notes' ? 'page' : undefined} className={view === 'notes' ? 'active' : ''} onClick={() => navigate('notes')}><NotebookPen size={18} /><span>我的笔记</span><em>{notes.length}</em></button>
        <button aria-label="与知识对话" aria-current={view === 'chat' ? 'page' : undefined} className={view === 'chat' ? 'active' : ''} onClick={() => navigate('chat')}><MessageCircle size={18} /><span>与知识对话</span></button>
      </nav>
      <div className="sidebar-foot">
        <div className="sync-state"><span className={knowledgeBase?.configured ? 'online' : ''} /><div><b>{knowledgeBaseResolutionFailed ? '知识库状态未知' : !knowledgeBaseResolved ? '正在确认知识库' : knowledgeBase?.configured ? '知识库已连接' : '知识库未连接'}</b><small>{knowledgeBaseResolutionFailed ? '请重新确认连接' : !knowledgeBaseResolved ? '正在读取连接状态' : knowledgeBase?.configured ? '所有内容已同步' : '连接后开启智能检索'}</small></div></div>
        <div className="profile"><CircleUserRound size={28} /><span><b>{isDemo ? 'Demo 体验者' : '我的空间'}</b><small>个人工作台</small></span></div>
      </div>
    </aside>

    <section className="workspace-content">
      <header className="topbar">
        <div className="global-search"><Search size={17} /><input ref={searchInput} aria-label="搜索笔记" placeholder="搜索你的知识..." value={search} onChange={e => setSearch(e.target.value)} /><kbd><Command size={12} /> K</kbd></div>
        <div className="today-chip"><span />知识花园正在生长</div>
      </header>

      {view === 'notes' ? <div className="notes-page">
        <section className="welcome-row">
          <div><p className="eyebrow">{formatWorkspaceDate()}</p><h1>今天想记下什么？</h1><p>捕捉一个念头，让它在未来与你重逢。</p></div>
        </section>

        {knowledgeBaseResolutionFailed ? <section className="kb-banner"><div><Layers3 size={19} /><span><b>暂时无法确认知识花园</b><small>重新读取连接状态后再继续。</small></span></div><button type="button" onClick={() => void loadKnowledgeBase()}>重新确认<ChevronRight size={15} /></button></section> : !knowledgeBaseResolved ? <section className="kb-banner" role="status"><div><Layers3 size={19} /><span><b>正在确认知识花园状态</b><small>请稍候，正在读取当前连接。</small></span></div></section> : !knowledgeBase?.configured && <section className="kb-banner"><div><Layers3 size={19} /><span><b>开启你的知识花园</b><small>创建知识库后，笔记会自动建立连接并支持智能问答。</small></span></div><button onClick={initializeKnowledgeBase} disabled={initializingKB}>{initializingKB ? '正在创建' : '立即开启'}<ChevronRight size={15} /></button></section>}

        <div className="notes-stage">
          <section className="notes-main">
            <form className="capture-card" onSubmit={createNote}>
              <div className="capture-mark"><Plus size={18} /></div>
              <div className="capture-fields">
                <input aria-label="笔记标题" placeholder="一个值得记住的标题..." value={title} onChange={e => setTitle(e.target.value)} required disabled={!knowledgeBase?.configured || creatingNote} />
                <textarea aria-label="笔记内容" placeholder="写下此刻的想法、会议要点或灵感..." value={content} onChange={e => setContent(e.target.value)} required disabled={!knowledgeBase?.configured || creatingNote} />
                <div className="capture-actions"><div><span>支持 Markdown</span></div><button className="save-note" disabled={!knowledgeBase?.configured || creatingNote}>{creatingNote ? '正在保存' : '保存笔记'}<ArrowUpRight size={15} /></button></div>
              </div>
            </form>

            {isDemo && <section className="insight-strip"><div><Sparkles size={17} /><span><b>最近的主题：如何让知识流动</b><small>4 条演示笔记都提到了记录与行动之间的关系。</small></span></div><button onClick={() => navigate('chat')}>和知识聊聊<ArrowUpRight size={15} /></button></section>}

            <div className="library-heading"><div><h2>最近笔记</h2><span>{filteredNotes.length} 条内容</span></div></div>
            {filteredNotes.length === 0 ? <div className="notes-empty"><BookOpen size={27} /><b>{search ? '没有找到相关笔记' : '这里还很安静'}</b><small>{search ? '试试搜索其他关键词' : '在上方写下你的第一条笔记'}</small></div> : <div className="note-grid">{filteredNotes.map((note, index) => <article className={`note-card tone-${index % 4}`} key={note.id}>
              <div className="note-topline"><span><Clock3 size={13} />{note.updated_at || noteStatus(note.status)}</span><div className="note-actions">{confirmDeleteID === note.id ? <><button type="button" className="icon danger" title="确认删除" aria-label="确认删除" disabled={deletingNoteIDs.has(note.id)} onClick={() => remove(note.id)}><Check size={15} /></button><button type="button" className="icon" title="取消删除" aria-label="取消删除" onClick={() => setConfirmDeleteID('')}><X size={15} /></button></> : <button type="button" className="icon" title="删除笔记" aria-label="删除笔记" disabled={deletingNoteIDs.has(note.id)} onClick={() => setConfirmDeleteID(note.id)}><MoreHorizontal size={17} /></button>}</div></div>
              <h3>{note.title}</h3><p>{note.content}</p>
              <footer><div>{(note.tags?.length ? note.tags : ['未分类']).map(tag => <span key={tag}>#{tag}</span>)}</div><small>{noteStatus(note.status)}<i /></small></footer>
            </article>)}</div>}
          </section>

        </div>
      </div> : <div className="chat-page"><div className="chat-page-heading"><div><p className="eyebrow">知识对话</p><h1>从你的笔记里，找到答案。</h1></div></div>{isDemo ? <DemoConversation /> : <ChatWorkspace knowledgeBaseState={knowledgeBaseResolutionFailed ? 'resolution_failed' : !knowledgeBaseResolved ? 'resolving' : initializingKB ? 'initializing' : knowledgeBase?.configured ? 'ready' : 'unconfigured'} onInitializeKnowledgeBase={initializeKnowledgeBase} onRetryKnowledgeBase={loadKnowledgeBase} onKnowledgeBaseRequired={knowledgeBaseRequired} onError={setError} onNoteChanged={loadNotes} />}</div>}
    </section>
    {error && <div className="error toast" role="alert"><span>{error}</span><button type="button" className="icon" aria-label="关闭错误" onClick={() => setError('')}><X size={15} /></button></div>}
    {notice && <div className="notice toast" role="status"><span>{notice}</span><button type="button" className="icon" aria-label="关闭提示" onClick={() => setNotice('')}><X size={15} /></button></div>}
  </main>
}
