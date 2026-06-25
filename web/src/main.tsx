import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ToastProvider, AdminProvider } from '@shoka/web-core'
import { AuthGate } from './components/AuthGate'
import { App } from './App'
import './styles/global.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <AdminProvider>
          <AuthGate>
            <App />
          </AuthGate>
        </AdminProvider>
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
)
