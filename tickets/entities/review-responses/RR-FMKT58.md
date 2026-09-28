---
id: RR-FMKT58
type: review-response
title: Zero face overloaded as whole-entity in notifyAliasesOfDelete
finding: Passing the default face to mean whole-entity delete would silently become DeleteAllFaces if DeleteEntityFace ever accepted the default face.
severity: minor
resolution: Split back into notifyAliasesOfDelete (method, unchanged) and a package function notifyAliasesOfFaceDelete, which keeps Manager under its plimsoll line.
status: addressed
---
