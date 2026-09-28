<!-- @managed: claude-workflow v1 -->
# Create and Start Ticket

You are helping the user create and work on a new ticket. The user has described what they want: $ARGUMENTS

## Step 1: Create the Ticket

Using the rela MCP tools (rela-issues-and-design-tickets):

1. Parse the user's description to extract:
   - A concise title
   - The ticket kind (enhancement, refactor, docs, test, chore)
   - Priority if mentioned (default: medium)
   - Affected concepts (search existing concepts)
   - Feature this implements (search existing features)

2. Create the ticket entity with:
   - Title and description from user input
   - Status: `ready`
   - Link to affected concepts via `affects` relation
   - Link to feature via `implements` relation

3. Run ALL analyze tools to verify the ticket is complete:
   - `analyze` (`check: cardinality`)
   - `analyze` (`check: validations`)
   - Fix any violations before proceeding

## Step 2: Planning Phase

Transition ticket to `planning` status. **Automation will create planning-checklist automatically.**

### Work through planning with DOCUMENTATION (not just checkboxes):

**Understanding:**

- Clarify the problem/requirements - ASK USER if anything is unclear
- Define scope explicitly - document what IS and IS NOT in scope
- Write specific acceptance criteria with test scenarios

**Approach:**

- Research the codebase thoroughly (use Explore agent if needed)
- Document the technical approach in detail
- List specific files that will be modified
- Consider and document alternatives

**Test Plan:**

- Document how EACH acceptance criterion will be tested
- List specific edge cases and expected behavior
- Define integration test approach (not just unit tests)

**Risk Assessment:**

- Identify risks with mitigations
- Estimate effort

### STOP AND PRESENT PLAN TO USER

Before proceeding to implementation:

1. Mark planning checklist as `done` (with all documentation filled in)
2. Present the plan summary to the user
3. ASK: "Does this plan look correct? Any concerns before I implement?"
4. Wait for user approval before proceeding

## Step 3: Implementation Phase

Transition ticket to `in-progress`. **Automation will create implementation-checklist automatically.**

### Implementation requirements:

**Development:**

- Write unit tests for new code
- Write integration tests (test the full flow, not just units)
- Implement the feature
- Handle ALL edge cases from planning

**Manual Verification (REQUIRED):**

- Test the feature end-to-end manually
- Verify EACH acceptance criterion
- Document verification evidence in the checklist

**Quality:**

- Check code follows project patterns
- Ensure no silent failures (errors must be surfaced, not just logged)

### STOP BEFORE REVIEW

1. Mark implementation checklist as `done` (with verification evidence)
2. Run `/verify {ticket-id}` to check transition readiness
3. Fix any blockers identified
4. Transition ticket to `review`

## Step 4: Review Phase

Transition ticket to `review`. **Automation will create review-checklist automatically.**

### Review requirements:

**Automated Checks:**

- Run `just test` - all must pass
- Run `just lint` - must be clean
- Run `just coverage-check` - must pass

**Code Review (REQUIRED):**

- Use the `cranky-code-reviewer` agent to review the code
- For EACH finding from the reviewer:
  1. Create a `review-response` entity with:
     - title: Brief description of the finding
     - finding: The full issue description
     - severity: critical/significant/minor/nit
     - status: open
  2. Link to ticket via `has-review-response` relation
- Address ALL critical and significant responses:
  - Fix the issue in code
  - Update the review-response status to `addressed`
  - Document the resolution
- For minor/nit issues, either:
  - Address them (preferred)
  - Mark as `wont-fix` or `deferred` with reason
- Document review summary in checklist

**Acceptance Verification:**

- Verify each acceptance criterion from planning
- Document PASS/FAIL with evidence for each

### Complete the ticket

1. Address any issues from code review
2. Verify all review-responses are resolved:
   - Run `list_relations` with from={ticket-id}, type=has-review-response
   - Check each linked review-response has status != open (for critical/significant)
3. Run `/verify {ticket-id}` to confirm readiness
4. Mark review checklist as `done`
5. Transition ticket to `done`
6. Run `analyze` (`check: validations`) - must pass with no errors
7. Commit with a message that explains WHY, not just WHAT

## Key Principles

1. **Documentation over checkboxes**: Checking a box without documentation is meaningless
2. **Ask when uncertain**: If requirements are unclear, ASK the user
3. **Test what you build**: Manual verification is required, not optional
4. **Review catches bugs**: The code reviewer will find issues - address them
5. **Stop at phase boundaries**: Get user approval before major transitions
