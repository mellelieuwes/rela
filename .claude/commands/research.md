<!-- @managed: claude-workflow v1 -->
# Research

Conduct structured research before implementing a feature or making a technical decision. The user has described the topic: $ARGUMENTS

## Purpose

Survey approaches, document tradeoffs, and arrive at a recommendation — so that implementation becomes mechanical and design decisions are recorded.

## Step 1: Create the Research Entity

Using the rela MCP tools (rela-issues-and-design-tickets):

1. Parse the user's description to extract:
   - A concise title framing the research question
   - Affected concepts (search existing concepts)
   - Related ticket or feature if mentioned

2. Create the research entity with:
   - Title framed as a question or topic
   - Status: `in-progress`
   - Link to concepts via `researches` relation
   - Link to ticket/feature via the ticket/feature's `has-research` relation (if applicable)

3. Run `analyze` (`check: cardinality`) and `analyze` (`check: validations`) to verify

## Step 2: Survey

Investigate the topic thoroughly. Use the Explore agent for broad codebase searches.

**Existing patterns:**
- Search the codebase for similar problems already solved
- Check for libraries or utilities that could be reused
- Look at how related subsystems handle the same concern

**External approaches:**
- Consider well-known patterns or libraries for this problem
- Check if reference implementations exist

**Constraints:**
- Review relevant architectural rules (CLAUDE.md, metamodel, arch-lint boundaries)
- Identify security, performance, or compatibility constraints

## Step 3: Document Options

Update the research entity body with findings:

**## Problem** — What question are we answering? Why does it matter now?

**## Context** — Constraints, existing patterns, prior art found in the codebase

**## Options** — For each viable approach:
- What would we do?
- Pros and cons
- Rough effort estimate
- Reference implementations or examples found

**## Recommendation** — Which option and why. What tradeoffs are we accepting?

## Step 4: Complete

1. Update the research entity:
   - Set `summary` to a one-line conclusion
   - Set status to `done`
2. If research was linked to a ticket, create an `informs` relation from the research to any decisions it led to
3. Run `analyze` (`check: validations`) — must pass

## Step 5: Present to User

Present the research summary:
- The problem framing
- Options considered (brief)
- The recommendation with key tradeoff
- ASK: "Does this direction look right, or should I explore any option further?"

## Key Principles

1. **Survey before deciding**: Don't jump to a recommendation without exploring alternatives
2. **Ground in the codebase**: Every option should reference existing patterns or explain why it departs from them
3. **Record for posterity**: The research entity is a durable record — future developers can understand why an approach was chosen
4. **Keep it proportional**: A small spike gets a short doc; a large feature gets thorough analysis
