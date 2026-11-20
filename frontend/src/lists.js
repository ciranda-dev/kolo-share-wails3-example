/** value when it is a list, else an empty list: an event reaches the window as Go sent it, and a nil slice is null. */
export const list = (value) => (Array.isArray(value) ? value : [])
