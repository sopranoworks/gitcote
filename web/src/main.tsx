import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  ThemeProvider,
  PaletteProvider,
  BannerProvider,
  EditSignalProvider,
  ShellProvider,
  ToastProvider,
  AdminProvider,
} from '@shoka/web-core'
import { AuthGate } from './components/AuthGate'
import { gityardShellConfig } from './gityardShellConfig'
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
      <ThemeProvider>
        <ToastProvider>
          <BannerProvider>
            <EditSignalProvider>
              <AdminProvider>
                <PaletteProvider>
                  <ShellProvider value={gityardShellConfig}>
                    <AuthGate>
                      <App />
                    </AuthGate>
                  </ShellProvider>
                </PaletteProvider>
              </AdminProvider>
            </EditSignalProvider>
          </BannerProvider>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
