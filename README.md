<!-- This file is auto-generated from docs-project/entities/. Do not edit directly. -->

# Rela

A database layer on top of markdown. Define entities, link them together, and query their
relationships—all stored as human-readable, version-controllable markdown files.

Rela is a schema-driven entity-graph platform. You define the shape of your domain in a
YAML metamodel; rela gives you typed entities, typed relations between them, and a set of
tools for querying, validating, analyzing, and presenting the resulting graph.

Common domains:

- **Architecture & design** — Link requirements to decisions to components
- **Compliance & ISMS** — Connect controls to evidence and audit findings
- **Risk management** — Trace risks through mitigations to controls
- **Product development** — Map features to user stories to tasks
- **Knowledge bases** — Relate concepts, documents, and references
- **Project governance** — Track goals through milestones to deliverables
- **Issue tracking** — Bugs, features, and tickets with their lifecycle

Traceability is one use case, not the identity. Anything with typed entities and
typed relations fits. Rela handles the rest: ID generation, bidirectional linking,
orphan detection, coverage analysis, validation rules, and graph export.

## Quick Start

```bash
# Initialize a project
rela init

# Create entities
rela create requirement -P title="System must support 1000 users"
rela create decision -P title="Use PostgreSQL for persistence"

# Link them together
rela link DEC-001 addresses REQ-001

# View the relationship
rela show REQ-001

# Launch the interactive TUI
rela tui

# Work with a project in a different directory
rela -p /path/to/project list
export RELA_PROJECT=/path/to/project && rela list
```

## Features

- **Entity Management** - Create, update, delete entities
- **Relationship Tracing** - Link entities and trace dependencies
- **Quality Analysis** - Find orphans, check coverage, detect gaps
- **Graph Export** - Export to Graphviz DOT format
- **Interactive TUI** - Full-featured terminal interface
- **MCP Server** - Expose rela to AI assistants via Model Context Protocol
- **Markdown Storage** - Human-readable, version-controllable files

## Installation

```bash
go install github.com/Sourcehaven-BV/rela/cmd/rela@latest
```

Or build from source:

```bash
git clone https://github.com/Sourcehaven-BV/rela.git
cd rela
go build -o rela ./cmd/rela
```

## Documentation

| Document | Description |
| -------- | -------- |
| [Getting Started](docs/getting-started.md) | Installation, first project, core workflow |
| [Concepts](docs/concepts.md) | Architecture traceability fundamentals |
| [CLI Reference](docs/cli-reference.md) | Complete command reference |
| [Metamodel Reference](docs/metamodel.md) | Configure entity types and relations |
| [How To Publish Content with Faces and Worlds](docs/content-states.md) | Give an entity type several content states (draft, published, translations), declare worlds that pick one face per reader, and publish through a guarded copy |
| [Export Guide](docs/export.md) | Export, import, and data integration |
| [Best Practices](docs/best-practices.md) | Maintenance tips and team workflows |
| [MCP Server](docs/mcp-server.md) | AI assistant integration via MCP |
| [Data Entry Web App](docs/data-entry.md) | Config-driven web UI for entity management |
| [Lua Scripting](docs/lua-scripting.md) | Programmable automation with embedded Lua |
| [Encrypting a rela repo with git-crypt](docs/git-crypt.md) | Use git-crypt for confidentiality when pushing to untrusted git hosts |
| [Scheduled Tasks](docs/scheduled-tasks.md) | Run Lua scripts on recurring schedules |
| [Audit Log](docs/audit-log.md) | Forensic JSONL log of every entity / relation write |
| [Outbound Mail](docs/mail.md) | Configure outbound email — SMTP, branding, and what best-effort delivery means |
| [PostgreSQL Backend](docs/postgres-backend.md) | Run rela-server and the CLI against PostgreSQL instead of markdown files |
| [Attachment Security: Scanning, MIME Allowlist & Transforms](docs/attachment-security.md) | Virus scanning, a sniffed MIME allowlist, and byte transforms for uploaded attachments |
| [SQLite Backend](docs/sqlite-backend.md) | Run rela against an embedded SQLite database — one file, no server, single process |
| [Sync](docs/sync.md) | Two-way sync between a local fsstore project and a remote pgstore rela-server |
| [Security model for rela-server](docs/server-security.md) | Threat model, HTTP defenses, audit attribution, and residual risks for the rela-server data-entry app; how its read-side ACL coverage stands today. |
| [ACL: Authorization Overview](docs/acl-overview.md) | How rela's role-based authorization works end-to-end: from acl.yaml + the graph to a write decision and its audit attribution |
| [ACL: Security Hardening](docs/acl-security.md) | Operator's hardening guide for rela's ACL system: group membership trust, fail-loud boot, audit-isolation invariants |
| [Generated documentation: the rela docs language](docs/rela-docs.md) | Author a deployment manual in Markdown with embedded Lua islands that pull reference fragments (field tables, enum meanings, mermaid lifecycles, relation graphs, role matrices) straight from the schema. |
| [CalDAV: syncing to-dos with Apple Reminders and other clients](docs/caldav.md) | Sync to-do collections two-way with Apple Reminders and other CalDAV clients |
| [CalDAV to-do (VTODO) client compatibility](docs/caldav-clients.md) | Which task apps speak VTODO, and what each does with formatted descriptions |
| [Data Migration](docs/data-migration.md) | Detect schema shape changes and migrate stored content with generated, reviewable migrations |
| [Comments: Annotating Entities, Fields and Text](docs/comments.md) | Enable commenting, control who may comment, and understand how anchors survive edits |
| [Twins: integrating entities with external systems](docs/twins.md) | Declare which system owns which field with a pact, keep each entity's external counterpart in sync through an agent, and understand what rela enforces |

### Tutorials

| Tutorial | Description |
| -------- | -------- |
| [Tutorial: Building an ISO 27001 ISMS with Rela](docs/tutorials/iso27001-isms-tutorial.md) | Build a complete Information Security Management System |
| [Tutorial: Hybrid Project Management with Rela](docs/tutorials/project-management-tutorial.md) | Build a hybrid project management system |

### Scenarios

| Scenario | Description |
| -------- | -------- |
| [Scenario: DevOps/SRE Runbooks & Infrastructure Operations](docs/scenarios/devops-runbooks.md) | DevOps/SRE runbooks and infrastructure operations |
| [Scenario: ISO 27001 Information Security Management System](docs/scenarios/iso27001-isms.md) | ISO 27001 Information Security Management System |
| [Scenario: Hybrid Project Management](docs/scenarios/project-management.md) | Hybrid project management documentation |

### Examples

| Example | Description |
| -------- | -------- |
| [Ticket tracker — operator handbook](docs/examples/ticket-tracker-manual.md) | A generated operator handbook for the demo ticket tracker, built by `rela-docs` — field tables, a mermaid lifecycle, a role matrix, and an annotated screenshot, all derived from the project's schema so they can't drift. |

## Project Structure

After running `rela init`:

```text
your-project/
├── schema.yaml          # Entity types and relations config
├── entities/            # Markdown entity files (by type)
│   ├── requirements/
│   ├── decisions/
│   └── ...
├── relations/           # Markdown relation files
├── templates/           # Optional: templates for new entities/relations
│   ├── entities/        # One template per entity type
│   └── relations/       # One template per relation type
└── .rela/               # Cache (gitignored)
```

## Core Traceability Chain

```text
Requirement ──addresses──> Decision ──implements──> Solution ──realizes──> Component
```

Use `rela analyze coverage` to check for gaps in this chain.

## License

AGPL-3.0 - See [LICENSE](LICENSE) for details.
