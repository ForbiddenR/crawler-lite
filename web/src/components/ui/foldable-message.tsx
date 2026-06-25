import { useEffect, useLayoutEffect, useRef, useState } from "react"
import { createPortal } from "react-dom"

// FoldableMessage hides its message until the user opts in. The folded state
// shows only a "show reason" toggle; clicking opens the full reason in a
// portaled, fixed-position popover anchored under the toggle. Because the
// popover is rendered into document.body with position: fixed, it floats above
// the surrounding layout (e.g. a table row) without reflowing it.
type FoldableMessageProps = {
  message: string
  /** Optional label rendered before the message, e.g. "Error: ". */
  label?: string
  /** Text for the reveal control. Defaults to "show reason" / "hide reason". */
  revealLabel?: string
  hideLabel?: string
  className?: string
}

export function FoldableMessage({
  message,
  label,
  revealLabel = "show reason",
  hideLabel = "hide reason",
  className,
}: FoldableMessageProps) {
  const [open, setOpen] = useState(false)
  const btnRef = useRef<HTMLButtonElement>(null)
  const [pos, setPos] = useState({ top: 0, left: 0 })

  // Anchor the popover under the toggle button whenever it opens.
  useLayoutEffect(() => {
    if (open && btnRef.current) {
      const r = btnRef.current.getBoundingClientRect()
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

  return (
    <span className={className} style={{ position: "relative" }}>
      <button
        ref={btnRef}
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="text-[11px] font-medium opacity-70 underline-offset-2 hover:underline"
      >
        {open ? hideLabel : revealLabel}
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
