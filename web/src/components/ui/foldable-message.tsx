import { useState } from "react"

// FoldableMessage hides its message until the user opts in. The folded state
// shows only the reveal control (so a failed task's row stays clean and shows
// just the status badge); clicking exposes the full message, wrapped. A short
// single-line message is still shown directly since there's nothing to fold.
const FOLD_THRESHOLD = 80

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
  const [expanded, setExpanded] = useState(false)
  // Fold anything multi-line (a traceback) or long. A short single-line
  // message renders in full with no toggle.
  const foldable = message.includes("\n") || message.length > FOLD_THRESHOLD

  if (!foldable) {
    return (
      <div className={className}>
        <p className="whitespace-pre-wrap break-words">
          {label ? <span className="font-medium">{label}</span> : null}
          {message}
        </p>
      </div>
    )
  }

  return (
    <div className={className}>
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="text-[11px] font-medium opacity-70 underline-offset-2 hover:underline"
      >
        {expanded ? hideLabel : revealLabel}
      </button>
      {expanded ? (
        <p className="mt-1 whitespace-pre-wrap break-words">
          {label ? <span className="font-medium">{label}</span> : null}
          {message}
        </p>
      ) : null}
    </div>
  )
}
