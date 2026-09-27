---
id: RR-ZTBBQ1
type: review-response
title: No test where the principal cannot read the user entity type
finding: Every fixture read '*'. With an unreadable user type; mijn is empty and niet-van-mij returns every visible row; untested and undocumented.
severity: minor
resolution: Added TestQueryScope_RelatedToCurrentUserUnreadableUserType and a sentence under Matching the current user in docs/metamodel.md.
status: addressed
---
