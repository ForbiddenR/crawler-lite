import { cn } from "@/lib/utils"
import { statusClass } from "@/lib/format"
import type { TaskStatus } from "@/api/resources"

interface Props {
  status: TaskStatus
  className?: string
}

/** Compact pill showing a task status with the conventional colour. */
export function StatusBadge({ status, className }: Props) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
        statusClass(status),
        className,
      )}
    >
      {status.replace("_", " ")}
    </span>
  )
}
