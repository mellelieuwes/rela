---
id: RR-HWTH5L
type: review-response
title: Root strip mangles sibling paths
finding: strings.ReplaceAll(msg, root, ".") also rewrote /srv/p2 when root is /srv/p.
severity: nit
resolution: Only root plus separator is stripped now.
status: addressed
---
