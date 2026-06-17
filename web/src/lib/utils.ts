import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

/**
 * Tailwind classname combiner. The shadcn-canonical utility — combines clsx
 * (conditionals) with tailwind-merge (deduplicates conflicting classes).
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
