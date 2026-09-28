<!-- @managed: claude-workflow v1 -->
# Create and Analyze Bug

You are helping the user report and analyze a bug. The user has described the issue: $ARGUMENTS

## Step 1: Create the Bug

Using the rela MCP tools:

1. Parse the user's description to extract:
   - A concise title describing the bug
   - Detailed description of the issue
   - Priority based on severity (critical/high/medium/low)
   - Affected concepts (search existing concepts)
   - Feature this affects (search existing features)

2. Create the bug entity with:
   - Title and description
   - Status: `ready`
   - Link to affected concepts via `affects` relation
   - Link to feature via `fixes` relation

3. Run `analyze` (`check: validations`) to verify the bug is complete

## Step 2: Start Analysis

Transition bug to `analyzing` status. **Automation will create bug-analysis-checklist automatically.**

## Step 3: Reproduction

- [ ] Attempt to reproduce the bug locally
- [ ] Document minimal reproduction steps
- [ ] Note environment/conditions

If you cannot reproduce, ask the user for more details.

## Step 4: Root Cause Analysis (5-Whys)

Perform 5-whys analysis by investigating the codebase:

- **why1**: What was the immediate cause?
- **why2**: Why did that happen?
- **why3**: Why did that happen?
- **why4**: Why did that happen?
- **why5**: What is the systemic root cause?

Update the bug entity with your findings in the why1-why5 properties.

## Step 5: Fix Planning

- [ ] Determine fix approach
- [ ] Plan regression test
- [ ] Check related areas for similar issues

## Step 6: Ready for Implementation

Once analysis is complete:

1. Mark analysis checklist as `done`
2. Transition bug to `in-progress` (automation creates implementation-checklist)
3. Present the analysis and fix plan to the user

Output the bug ID, root cause analysis, and proposed fix.
