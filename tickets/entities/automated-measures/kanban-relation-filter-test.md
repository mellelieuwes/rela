---
id: kanban-relation-filter-test
type: automated-measure
title: 'Regression test: kanban filter controls filter server-side, relation controls included'
description: 'Regression guard for BUG-GEMNW6. Frontend: KanbanView.filters.test.ts asserts a relation filter in the URL reaches the listAllEntities request and the returned card is shown. Backend: TestV1ListRelationFilter_ControlOnKanbanOnly asserts the list endpoint applies a relation filter that only a kanban configures; TestConfigRelationFilterDirection and TestCollectConfigWarnings_ConflictingRelationDirections pin kanban controls in resolution and warnings. e2e kanban.spec.ts (Filtering) filters the fixture board by a property and by a blocks relation.'
kind: test
location: frontend/src/views/KanbanView.filters.test.ts
status: active
---
