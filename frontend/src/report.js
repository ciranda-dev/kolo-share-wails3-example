/** The longest report, in characters: maxReportRunes in report.go, where Go cuts it again. */
export const MAX_REPORT = 8192

const cut = (text) => Array.from(text).slice(0, MAX_REPORT).join('')

/** The parts of an error for the log: "name: message" and the stack, the component stack after it. No other text. */
export function reportParts(error, componentStack = '') {
  const message = error instanceof Error ? `${error.name}: ${error.message}` : `Error: ${String(error)}`
  let stack = error instanceof Error && typeof error.stack === 'string' ? error.stack : ''
  // Some engines start the stack with the name and the message: say them once.
  if (stack.startsWith(message)) {
    stack = stack.slice(message.length)
  }
  stack = [stack, componentStack].map((s) => s.replace(/^\n+|\s+$/g, '')).filter(Boolean).join('\n')
  return { message: cut(message), stack: cut(stack) }
}

/** The text the error pane shows and copies: the message, then the stack. */
export const reportText = ({ message, stack }) => cut([message, stack].filter(Boolean).join('\n'))
