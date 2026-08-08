import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import './styles.css'

// basename 必须跟 vite.config.ts 的 base 一致。少了它，路由会以为自己在根上，
// 点「接入点」跳到 /access-points 而不是 /admin/access-points——那个地址
// 网关直接回 404，不是 SPA。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter basename="/admin">
      <App />
    </BrowserRouter>
  </StrictMode>,
)
