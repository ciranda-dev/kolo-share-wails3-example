import { avatarColor, initial } from './avatars.js'

/** A round avatar: the first letter of the name on a color of the seed. */
export function Avatar({ name, seed, size = 36, className = '' }) {
  return (
    <span className={`disc ${className}`} style={{ width: size, height: size, background: avatarColor(seed || name), fontSize: Math.round(size * 0.42) }} aria-hidden="true">
      {initial(name)}
    </span>
  )
}
