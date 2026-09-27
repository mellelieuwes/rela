import { describe, it, expect } from 'vitest'
import { mentionWorld } from './mentionWorld'

// Two sites, each led by its own language: the shape where the face being
// edited and the page's world can disagree.
const heads: Record<string, string> = { nl: 'site-nl', en: 'site-en' }
const worldForFace = (_type: string, face: string) => (face ? heads[face] : '')

describe('mentionWorld', () => {
  it('uses the world that heads the face being edited', () => {
    expect(mentionWorld({ type: 'page', face: 'en' }, worldForFace, 'site-nl')).toBe('site-en')
  })

  it('uses the page world when no world heads the face', () => {
    expect(mentionWorld({ type: 'page', face: 'draft' }, worldForFace, 'site-nl')).toBe('site-nl')
  })

  it('uses the page world for an entity without a face', () => {
    expect(mentionWorld({ type: 'person', face: '' }, worldForFace, 'site-nl')).toBe('site-nl')
  })

  it('uses the page world when nothing is being edited', () => {
    expect(mentionWorld(null, worldForFace, undefined)).toBeUndefined()
  })
})
