import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

// Unmount React trees between tests so component state never leaks across cases.
afterEach(() => {
  cleanup()
})

// jsdom does not implement matchMedia; the responsive shell reads it. Provide a
// minimal, non-matching stub so components that call matchMedia render the wide
// layout in unit tests. E2E (Playwright) exercises the real responsive behaviour.
if (typeof window !== 'undefined' && !window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList
}
