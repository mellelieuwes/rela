---
id: RR-3LULI0
type: review-response
title: 'Design review: Link records the local entity as agreed base, so the first pull corrupts shared fields'
finding: 'Link sets Base := the current entity snapshot (design:224-226). That asserts the external item equalled rela at link time, which is false for a never-synced twin. On the first pull, every shared field where the two sides differ has L = B, so it takes R (design:204): rela''s value is silently overwritten instead of being reported as a conflict. Every differing ours field is reported as foreign_edit, which mislabels the initial divergence as an external edit. Pushed-before-pull has a similar problem: theirs fields keep the link-time local values as base. Fix: give a never-synced twin an explicit ''no base'' state (e.g. Base nil or a HasBase flag). Define Reconcile without a base: theirs takes R; ours with L≠R becomes ''needs push'' (not foreign_edit); shared with L≠R becomes a conflict. Alternatively, make `link` take the initial remote and run the first reconcile. Add both to the Reconcile table tests.'
severity: significant
resolution: A linked twin has no base (HasBase=false) until the first pull; Reconcile has explicit first-sync rules (theirs takes remote, ours needs a push, differing shared values conflict). Table tests cover both paths.
status: addressed
---
