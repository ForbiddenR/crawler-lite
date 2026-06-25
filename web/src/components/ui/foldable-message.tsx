import { useEffect, useLayoutEffect, useRef, useState } from "react"
import type { ReactNode } from "react"
import { createPortal } from "react-dom"

// FoldableMessage has two modes:
//
//  - Inline (default, no `trigger`): the message renders directly. Used for
//    log lines that should always be visible.
//  - Popover (`trigger` provided): the message is hidden until the user
//    clicks the trigger node; clicking opens the full message in a portaled,
//    fixed-position popover anchored under the trigger. Because it renders
//    into document.body with position: fixed, it floats above the surrounding
//    layout (e.g. a table row) without reflowing it. Used for task error
//    reasons, where the trigger is the task's status badge.
type FoldableMessageProps = {
  message: string
  /** Optional label rendered before the message in the popover, e.g. "Error: ". */
  label?: string
  /** When provided, the message is hidden and this node becomes the click
   *  trigger that opens the popover. */
  trigger?: ReactNode
  className?: string
}

export function FoldableMessage({ message, label, trigger, className }: FoldableMessageProps) {
  const [open, setOpen] = useState(false)
  const anchorRef = useRef<HTMLSpanElement>(null)
  const [pos, setPos] = useState({ top: 0, left: 0 })

  // Anchor the popover under the trigger whenever it opens.
  useLayoutEffect(() => {
    if (open && anchorRef.current) {
      const r = anchorRef.current.getBoundingClientRect()
      setPos({ top: r.bottom + 4, left: r.left })
    }
  }, [open])

  // Close on Escape and on any scroll/resize so the popover never detaches
  // from its anchor as the page or a scroll container moves.
  useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close()
    }
    window.addEventListener("keydown", onKey)
    window.addEventListener("scroll", close, true)
    window.addEventListener("resize", close)
    return () => {
      window.removeEventListener("keydown", onKey)
      window.removeEventListener("scroll", close, true)
      window.removeEventListener("resize", close)
    }
  }, [open])

  // Inline mode: just show the message.
  if (!trigger) {
    return (
      <span className={className}>
        <p className="whitespace-pre-wrap break-words">{message}</p>
      </span>
    )
  }

  return (
    <span ref={anchorRef} className={className} style={{ position: "relative" }}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        // Reset button chrome so the trigger node (e.g. a status badge pill)
        // renders identically to when it stands alone.
        className="cursor-pointer border-0 bg-transparent p-0 leading-none"
      >
        {trigger}
      </button>
      {open
        ? createPortal(
            <>
              {/*
                Transparent click-catcher: a <button> so click-outside-to-close
                is keyboard-accessible (Enter/Space closes) and screen readers
                announce it as a control rather than a bare clickable div.
              */}
              <button
                type="button"
                tabIndex={-1}
                aria-label="Close"
                className="fixed inset-0 z-40 cursor-default"
                onClick={() => setOpen(false)}
              />
              <div
                className="fixed z-50 max-h-[300px] w-80 max-w-[calc(100vw-2rem)] overflow-auto rounded-lg border border-zinc-200 bg-white p-3 text-xs shadow-xl"
                style={{ top: pos.top, left: pos.left }}
              >
                {label ? <span className="font-medium">{label}</span> : null}
                <p className="whitespace-pre-wrap break-words text-zinc-800">{message}</p>
              </div>
            </>,
            document.body,
          )
        : null}
    </span>
  )
}
