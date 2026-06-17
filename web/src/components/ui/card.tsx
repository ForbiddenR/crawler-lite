import type { ReactNode } from "react"

interface CardProps {
  children: ReactNode
  className?: string
}

import { cn } from "@/lib/utils"

export function Card({ children, className }: CardProps) {
  return (
    <div
      className={cn(
        "rounded-lg border border-zinc-200 bg-white shadow-sm",
        className,
      )}
    >
      {children}
    </div>
  )
}

export function CardHeader({ children, className }: CardProps) {
  return <div className={cn("border-b border-zinc-200 px-6 py-4", className)}>{children}</div>
}

export function CardBody({ children, className }: CardProps) {
  return <div className={cn("px-6 py-5", className)}>{children}</div>
}
