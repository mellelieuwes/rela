---
id: RR-V2C1MI
type: review-response
title: Script loses entity.redacted and is_redacted
finding: resolveDetailActionEntity redacted by hand with copyVisibleProperties and never set Entity.Redacted, so entity:is_redacted() returned false for hidden fields and a default-if-unset script could overwrite a value it could not see. It was also a second redaction point.
severity: significant
resolution: The check redacts once through visibility.Redact (fixedRedactor over the computed hidden set) and feeds that one entity to both when and the script. The test script records is_redacted('owner'); mutation-verified.
status: addressed
---
