import { type InputHTMLAttributes, forwardRef } from "react"

import { cn } from "@/lib/utils"

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input
      ref={ref}
      className={cn(
        "h-10 w-full rounded-md border border-zinc-200 bg-white px-3 text-sm",
        "focus:border-zinc-400 focus:outline-none focus:ring-2 focus:ring-zinc-200",
        "disabled:cursor-not-allowed disabled:bg-zinc-50",
        className,
      )}
      {...props}
    />
  ),
)
Input.displayName = "Input"
