import { useState, type ReactNode } from 'react'
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels'
import { useRouterState } from '@tanstack/react-router'
import { TitleBar } from './TitleBar'
import { ActivityRail } from './ActivityRail'
import { StatusBar } from './StatusBar'
import { Banner } from './Banner'
import { useMediaQuery } from '../lib/useMediaQuery'
import { useShellConfig } from '../lib/shellConfig'
import styles from './Shell.module.css'

export function Shell({ children }: { children: ReactNode }) {
  const {
    useRailControls,
    useResetRailOnProjectChange,
    deriveActiveRail,
    railItems,
    renderSidebar,
    renderSidebarExtra,
    renderCommandPalette,
    renderToaster,
    renderNotifyBridge,
    shellWrapper: Wrapper,
    layoutAutoSaveId,
  } = useShellConfig()

  const [rail, setRail] = useState(railItems[0]?.id ?? '')
  const [sidebarOpen, setSidebarOpen] = useState(true)
  const isNarrow = useMediaQuery('(max-width: 640px)')

  const { onSelect: onRailSelect, disabledItems } = useRailControls(
    rail,
    sidebarOpen,
    setRail,
    setSidebarOpen,
  )
  useResetRailOnProjectChange(setRail)

  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeRail = deriveActiveRail
    ? deriveActiveRail(pathname, rail)
    : rail

  const direction = isNarrow ? 'vertical' : 'horizontal'

  const content = (
    <div className={styles.shell} data-narrow={isNarrow}>
      <TitleBar onToggleSidebar={() => setSidebarOpen((v) => !v)} />

      <div className={styles.body}>
        <ActivityRail
          active={activeRail}
          onSelect={onRailSelect}
          disabled={disabledItems}
        />

        <div className={styles.panelArea}>
          <PanelGroup
            key={direction}
            direction={direction}
            autoSaveId={
              isNarrow
                ? undefined
                : (layoutAutoSaveId ?? 'shoka-proto-layout')
            }
          >
            {sidebarOpen && (
              <>
                <Panel
                  id="sidebar"
                  order={1}
                  defaultSize={isNarrow ? 34 : 22}
                  minSize={isNarrow ? 18 : 14}
                  maxSize={isNarrow ? 60 : 40}
                  className={styles.sidebarPanel}
                >
                  {renderSidebar(activeRail)}
                  {renderSidebarExtra?.()}
                </Panel>
                <PanelResizeHandle
                  className={
                    isNarrow ? styles.resizeHandleH : styles.resizeHandle
                  }
                />
              </>
            )}
            <Panel id="content" order={2} className={styles.contentPanel}>
              <Banner />
              <main className={styles.content}>{children}</main>
            </Panel>
          </PanelGroup>
        </div>
      </div>

      <StatusBar />
      {renderCommandPalette?.()}
      {renderToaster?.()}
      {renderNotifyBridge?.()}
    </div>
  )

  return Wrapper ? <Wrapper>{content}</Wrapper> : content
}
