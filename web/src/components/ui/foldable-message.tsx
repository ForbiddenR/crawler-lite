import { useState } from "react"

// Collapse threshold for a folded message. Messages at or below this length
// render in full with no toggle; longer ones get a preview + a show more/less
// control so the surrounding layout stays compact but the full text is
// reachable. Used for task error reasons on the list and detail pages.
const PREVIEW_LEN = 120

type FoldableMessageProps = {
  message: string
  /** Optional label rendered before the message, e.g. "Error: ". */
  label?: string
  className?: string
}

export function FoldableMessage({ message, label, className }: FoldableMessageProps) {
  const [expanded, setExpanded] = useState(false)
  const long = message.length > PREVIEW_LEN
  const shown = long && !expanded ? `${message.slice(0, PREVIEW_LEN).trimEnd()}…` : message

  return (
    <div className={className}>
      <p className="whitespace-pre-wrap break-words">
        {label ? <span className="font-medium">{label}</span> : null}
        {shown}
      </p>
      {long ? (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="mt-0.5 text-[11px] font-medium underline-offset-2 hover:underline"
        >
          {expanded ? "show less" : "show more"}
        </button>
      ) : null}
    </div>
  )
}
