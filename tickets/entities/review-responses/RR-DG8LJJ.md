---
id: RR-DG8LJJ
type: review-response
title: 'Code review: Fix GUIDE-twins claims that disagree with the code'
finding: (1) GUIDE-twins.md:162 says 'rela update and every other CLI write' is guarded. But `rela import` (importer.go:454), `rela normalize` (normalize.go:55) and `rela migrate data` (datamigration/run.go:268) write to the store directly, and GUIDE-metamodel's 'Only rela twin pull writes them' repeats the claim. List these commands under 'Where it is not', next to ApplyEntity and hand edits, as local_drift sources. (2) :637-638 says 'The finding names the field when rela knows it', but recordRejection never sets Field (service.go ~402). Say it names no field. (3) :251-252 shows `_targets/SC-015`, while the file is `_targets/^s^c-015` (file.go indexFileName uses encodeName). (4) The worked example's first pull also writes `status` (the created scenario has none, and the remote sends 'open'). (5) The HTTP key list at :684-686 omits `has_base`. (6) cli-reference says has_base is 'false until the first pull or push', but a rejected first pull keeps it false. Fix the guide text, then run `just docs` so the generated docs/twins.md, docs/cli-reference.md and docs/metamodel.md follow.
severity: minor
resolution: 'GUIDE-twins, GUIDE-cli-reference and GUIDE-metamodel corrected: store-direct commands (import, normalize, migrate data) listed as local_drift sources; rejected findings name no field; encoded `_targets` names; worked example; has_base in the HTTP keys and after a rejected first pull; gone, stale, pushed-findings, conflict-resolution and the new guarded paths documented.'
status: addressed
---
