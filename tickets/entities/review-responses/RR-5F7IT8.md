---
id: RR-5F7IT8
type: review-response
title: 'Design review: Gaps in the acceptance criteria and spec wording'
finding: (1) Acceptance criterion 1 says 'no twin routes', but the design (following comments, comments_handler.go:54-61) registers the route and returns a JSON 404; reword the criterion. (2) Pending's contract 'excludes gone unless LocalChanged is irrelevant (include gone…)' (design:237) contradicts itself. State that gone twins are listed until unlinked, and say what the agent does for an externally-gone twin. (3) A gone twin keeps (Target.ID, System), so if an id is reused, relinking fails with ErrDuplicateTarget; exclude gone twins from that check. (4) There is no conflict-resolution verb. Resolving currently means editing one side until R = L; document that, or add `rela twin resolve --ours|--theirs`. (5) `.rela/` is gitignored (projectsetup/init.go:87-97), so on fs, twin bases do not travel with a clone, unlike committed state such as migrations/applied.json (filemigstate/file.go:4-21). Record that as an explicit 'machine vs content' decision (CLAUDE.md comments section). (6) Link should refuse git-crypt-locked entities, because PatchEntity already refuses them (manager.go:1049-1051).
severity: minor
resolution: Acceptance criteria reworded (JSON 404 when disabled); gone twins stay listed until unlinked and do not block re-linking; conflicts are resolved by making the sides equal or by setting the value in rela and pushing it (documented); `.rela/twins` is documented as machine state that does not travel with a clone; link refuses locked entities.
status: addressed
---
