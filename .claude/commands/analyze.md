<!-- @managed: claude-workflow v1 -->
# Analyze Ticket/Bug

Run all rela validation and analysis tools on a ticket or bug.

## Usage

If $ARGUMENTS is provided, analyze that specific entity. Otherwise, ask the user which entity to analyze.

## Process

Run all analysis tools via rela MCP:

1. **analyze** `check: cardinality` - Check required relations
   - tickets need `affects` and `implements`
   - bugs need `affects` and `fixes`
   - checklists need to be linked to their parent

2. **analyze** `check: orphans` - Find unlinked entities
   - Checklists without parent tickets/bugs
   - Entities missing required connections

3. **analyze** `check: properties` - Validate property values
   - Required properties present
   - Values match expected types/enums

4. **analyze** `check: validations` - Run custom validation rules
   - Status-specific requirements
   - Checklist completion rules
   - Skip reason validation

## Output

For each violation found:

```text
Issue: [violation description]
Entity: [entity type and ID]
Fix: [what needs to be done]
```

Offer to fix each issue automatically where possible.

## Loop Until Clean

Repeat the analysis after each fix until all checks pass:

```text
Analysis complete:
  Cardinality: PASS
  Orphans: PASS
  Properties: PASS
  Validations: PASS

All checks passed!
```
