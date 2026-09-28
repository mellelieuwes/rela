---
id: RR-2YVAJL
type: review-response
title: Reader address test cannot see the row gate receive the address
finding: typeGate allowed every id, so a PolicyReader handing the whole address to PermitsRead still passed; no test drove DeleteEntityFace into a real comments.Service.
severity: minor
resolution: address_test uses idGate admitting only the bare id and states its reliance on memstore's refusal. TestFaceDelete_DropsTheRealCommentThread drives DeleteEntityFace into a real comments.Service.
status: addressed
---
