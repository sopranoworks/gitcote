// Clipboard API polyfill for non-secure contexts (HTTP on non-localhost).
// Web-core's CopyButton checks `navigator.clipboard` and calls writeText();
// without this polyfill the fallback path (selectFallback) silently fails
// because it never calls document.execCommand('copy').
if (!navigator.clipboard) {
  Object.defineProperty(navigator, 'clipboard', {
    value: {
      writeText(text: string): Promise<void> {
        return new Promise((resolve, reject) => {
          const ta = document.createElement('textarea')
          ta.value = text
          ta.style.position = 'fixed'
          ta.style.opacity = '0'
          document.body.appendChild(ta)
          ta.select()
          try {
            if (document.execCommand('copy')) { resolve() } else { reject(new Error('copy failed')) }
          } catch (e) { reject(e as Error) }
          finally { document.body.removeChild(ta) }
        })
      },
      readText(): Promise<string> {
        return Promise.reject(new Error('Clipboard.readText unavailable in non-secure context'))
      },
    },
    configurable: true,
  })
}

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
import { AuthGate } from '@shoka/web-core'
import { gitcoteShellConfig } from './gitcoteShellConfig'
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
                  <ShellProvider value={gitcoteShellConfig}>
                    <AuthGate appName="GitCote">
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
