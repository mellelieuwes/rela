/**
 * Which world the `@` menu searches in.
 *
 * The world decides which entities the menu can find and which face's title a
 * row shows. It does not decide what gets written: a reference stores the bare
 * id, and each reader resolves it through their own world.
 *
 * The menu prefers the world that serves the face being edited, so writing in
 * the `en` face of a page finds what the English site shows, even when the page
 * was opened under the Dutch world. `worldForFace` in the schema store owns
 * that mapping. When no world heads the face (an atlas concept, whose only
 * world leads with the adopted face), or the entity has no face, the menu uses
 * the page's world, like the command palette and the entity picker.
 */

/** The entity being edited, as far as the world choice needs it. */
export interface MentionWorldSelf {
  type: string
  /** The face being edited, or '' for an entity without one. */
  face: string
}

/**
 * The `world` parameter the menu's reads send, or undefined for no parameter.
 *
 * `worldForFace` answers '' for the bare face, meaning the default world. That
 * is not a preference for the default world here: an entity without a face is
 * served in every world, so the page's world stands.
 */
export function mentionWorld(
  self: MentionWorldSelf | null,
  worldForFace: (entityType: string, face: string) => string | undefined,
  pageWorld: string | undefined
): string | undefined {
  const own = self?.face ? worldForFace(self.type, self.face) : undefined
  return own || pageWorld
}
