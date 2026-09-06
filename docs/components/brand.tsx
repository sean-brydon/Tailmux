export function Mark({ size = 28 }: { size?: number }) {
  return <svg width={size} height={size} viewBox="0 0 32 32" fill="none" aria-hidden="true"><rect x="1" y="1" width="30" height="30" rx="8" fill="currentColor" opacity=".12"/><path d="m8 10 6 6-6 6M17 22h7M22 8v7m-3-3 3 3 3-3" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"/></svg>;
}
