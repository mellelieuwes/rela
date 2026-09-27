---
id: RR-XV2AAP
type: review-response
title: current_user.id on an enum property skips the enum check
finding: '{ status = current_user.id } on an enum was accepted although it can never match a declared value; so not related would match every row.'
severity: minor
resolution: validateTraversalProps refuses a current_user ref on an enum property at load; test case added.
status: addressed
---
