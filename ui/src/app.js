import 'katex/dist/katex.css'
import 'tailwindcss/tailwind.css'
import Main from './components/main.html'
import $ from 'jquery'
import './auth.scss'

function load(template) {
    document.body.innerHTML = template

    require('./components/main.js')
    require('./components/main.scss')
    require('./components/addition.scss')
}

function goToLogin(expired = true) {
    document.body.innerHTML = ''
    location.replace('/login?expired=' + (expired ? '1' : '0') + '&next=' + encodeURIComponent(location.pathname + location.hash))
}

function start() {
    fetch('/api/auth/me', {credentials: 'same-origin', cache: 'no-store'}).then(response => {
        if (response.status === 401) { goToLogin(); return null }
        if (!response.ok) throw new Error('会话服务暂时不可用，请刷新重试。')
        return response.json()
    }).then(session => {
        if (!session) return
        const remaining = Date.parse(session.expires_at) - Date.now()
        if (remaining <= 0) return goToLogin()
        load(Main)
        const toolbar = document.createElement('div')
        toolbar.className = 'session-toolbar'
        const username = document.createElement('span')
        username.textContent = session.username
        const expiry = document.createElement('span')
        expiry.className = 'session-expiry'
        expiry.textContent = '到期 ' + new Date(session.expires_at).toLocaleString('zh-CN', {hour12:false})
        const logout = document.createElement('button')
        logout.textContent = '退出登录'
        logout.onclick = () => {
            logout.disabled = true
            fetch('/api/auth/logout', {method:'POST', credentials:'same-origin'}).then(result => {
                if (!result.ok) throw new Error('退出失败')
                goToLogin(false)
            }).catch(() => { logout.disabled = false; logout.textContent = '退出失败，重试' })
        }
        toolbar.append(username, expiry, logout)
        document.body.append(toolbar)
        $(document).ajaxError((event, xhr) => { if (xhr.status === 401) goToLogin() })
        const checkExpiry = () => {
            const left = Date.parse(session.expires_at) - Date.now()
            if (left <= 0) goToLogin()
            else setTimeout(checkExpiry, Math.min(left, 60000))
        }
        checkExpiry()
        window.addEventListener('pageshow', event => { if (event.persisted) location.reload() })
    }).catch(() => {
        document.body.textContent = '暂时无法验证登录状态，请刷新页面重试。'
    })
}
start()
