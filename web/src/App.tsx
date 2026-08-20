import { FormEvent, useEffect, useState } from 'react'
import { BookOpen, LogIn, Plus, Send, Trash2 } from 'lucide-react'

type Note = { id: string; title: string; content: string; status: string; tags?: string[] }
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
  const [chatSessionID, setChatSessionID] = useState('')
  const [chatting, setChatting] = useState(false)
  const [error, setError] = useState('')

  const loadNotes = () => api('/v1/notes').then(r => setNotes(r.items || [])).catch(e => setError(e.message))
  useEffect(() => { api('/v1/auth/me').then(() => { setLoggedIn(true); loadNotes() }).catch(() => undefined) }, [])

  async function login(e: FormEvent) { e.preventDefault(); setError(''); try { await api('/v1/auth/login', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({email, password}) }); setLoggedIn(true); await loadNotes() } catch (err) { setError((err as Error).message) } }
  async function createNote(e: FormEvent) { e.preventDefault(); try { await api('/v1/notes', {method:'POST', headers:{'Content-Type':'application/json','Idempotency-Key':crypto.randomUUID()}, body:JSON.stringify({title,content})}); setTitle(''); setContent(''); await loadNotes() } catch (err) { setError((err as Error).message) } }
  async function remove(id: string) { try { await api(`/v1/notes/${id}`, {method:'DELETE', headers:{'Idempotency-Key':crypto.randomUUID()}}); await loadNotes() } catch (err) { setError((err as Error).message) } }
  async function ask(e: FormEvent) {
    e.preventDefault()
    setAnswer('')
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
      source.addEventListener('run.completed', () => { setChatting(false); source.close() })
      source.addEventListener('run.failed', () => { setChatting(false); source.close() })
      source.onerror = () => { setChatting(false); source.close() }
      setQuery('')
    } catch (err) {
      setChatting(false)
      setError((err as Error).message)
    }
  }

  if (!loggedIn) return <main className="auth"><form onSubmit={login}><BookOpen size={28}/><h1>Note Agent</h1><input aria-label="邮箱" placeholder="邮箱" value={email} onChange={e=>setEmail(e.target.value)} /><input aria-label="密码" type="password" placeholder="密码" value={password} onChange={e=>setPassword(e.target.value)} /><button><LogIn size={16}/>登录</button>{error && <p className="error">{error}</p>}</form></main>
  return <main><header><div><BookOpen size={22}/><strong>Note Agent</strong></div><span>个人工作台</span></header><section className="grid"><form className="panel" onSubmit={createNote}><h2>记一笔</h2><input placeholder="标题" value={title} onChange={e=>setTitle(e.target.value)} required /><textarea placeholder="今天记录了什么？" value={content} onChange={e=>setContent(e.target.value)} required /><button><Plus size={16}/>保存笔记</button></form><section className="panel"><h2>我的笔记</h2>{notes.map(n=><article key={n.id}><div><b>{n.title}</b><small>{n.status}</small><p>{n.content}</p></div><button className="icon" title="删除笔记" onClick={()=>remove(n.id)}><Trash2 size={16}/></button></article>)}</section><form className="panel chat" onSubmit={ask}><h2>问问过去的记录</h2><textarea placeholder="例如：我之前关于垃圾回收记了什么？" value={query} onChange={e=>setQuery(e.target.value)} required disabled={chatting}/><button disabled={chatting}><Send size={16}/>{chatting?'回答中':'发送'}</button>{answer && <pre>{answer}</pre>}</form></section>{error && <p className="error toast">{error}</p>}</main>
}
