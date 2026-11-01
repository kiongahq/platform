# Kionga interface design system

Kionga uses the Cobalt visual system across its console, workspaces, writing studio, API reference and public pages. The interface presents resources, their state and the next available action with minimal decoration.

## Foundations

Colors, typography, spacing, borders, elevation and motion are semantic tokens in `frontend/app/globals.css`. Components consume those tokens rather than declaring independent colors or font stacks.

- Light mode is the default, independent of the operating system. Dark mode applies only when explicitly selected and uses the corresponding graphite surface hierarchy. Surfaces use cool near-white paper steps with blue-grey rules.
- Cobalt marks primary actions, current selection and focus. Success, warning and failure have separate semantic colors and visible text.
- Familjen Grotesk provides headings, IBM Plex Sans provides application text, and JetBrains Mono provides code and machine values. Font files and licenses are self-hosted in `frontend/public/fonts/`.
- Controls use restrained keycap depth. Panels use a small radius, with cut corners where supported by the browser.
- Layouts use a four-pixel spacing scale. Content has a capped measure; wide displays increase the gutters rather than stretching text indefinitely.

## Operational screens

Console screens lead with the resource or task: a table, execution timeline, graph canvas, document or inspector. They use a fixed application type scale and compact, legible controls.

Navigation, filters, sorting, pagination and selected resources must have clear URL state. Every data region supports loading, empty, error and populated states. Access restrictions and unavailable dependencies are explicit states.

Prefer tables for comparable records, detail panels for resource context, and inline controls for small edits. Use dialogs for focused tasks that require a clear boundary. Dialogs manage focus, support Escape and return focus to their trigger.

## Public and editorial surfaces

Public pages use the same tokens and controls with more generous spacing. Product claims describe actual capabilities. Screenshots and figures must come from the product or a declared example; activity, statistics and testimonials are never invented.

Articles use a narrow reading measure, strong heading hierarchy and clear author/date metadata. The studio keeps formatting controls subordinate to the document and makes saving, review and publishing state explicit.

## Interaction and accessibility

- Interactive elements have visible keyboard focus and meaningful accessible names.
- Controls expose default, hover, active, focus, disabled, loading, error and success states as appropriate.
- Motion communicates state and respects reduced-motion preferences.
- Status does not rely on color alone. Text and icons convey meaning without decorative dots.
- Destructive operations require confirmation or a recoverable action.
- Mobile layouts preserve access to navigation, forms, tables and workflow actions.

## Content and implementation

Use sentence-case labels and concrete product language. Avoid decorative eyebrow labels, tracked capitals, gradients, glass surfaces and unsupported claims.

React owns each application subtree and its interaction state. Shared layouts and controls follow this system; feature components do not introduce separate visual themes. Backend authorization and resource lifecycle remain authoritative.

## Code, fields and navigation

Structured data and source code use a shared numbered viewer with syntax treatment, wrapping and copy controls. Copying returns source text without line numbers. JSON and YAML editors share a scroll-synchronized gutter and an explicit format action; invalid input remains available for correction. Formatting preserves JSON numeric lexemes and YAML comments and anchors.

Dropdowns retain native selection, form and keyboard behavior inside a consistent field with a Material chevron. Icon controls use the bundled Material Design Icons sprite and expose accessible labels. Console navigation uses distinct Build, Models and data, and Operate groups. Resource search finds assigned records by name or ID and carries project context into the destination.

The landing hero uses a dense section-wide ASCII flow field with a quiet area around product copy. Mouse and pen movements leave a continuous character trail that fades over 1.2 seconds; reduced-motion and touch interactions do not generate the trail. Its movement is illustrative, has no playback controls, and stops for reduced-motion preferences or an inactive viewport. The footer renders the product name in responsive character art. Cobalt accents distinguish the access section, selected workspace and resource states; paper, graphite source panels and ruled editorial lists provide different section compositions.

The landing resource registry is explicitly an example. Selecting a resource updates its numbered configuration inspector without creating platform resources. Interactive workspace examples remain local to the browser and link to the corresponding authenticated workspace tools.

The product landing page carries product capabilities and interactive examples only. Editorial content belongs to a separate publication surface with reader navigation. Adopters manage their own publication through the console and studio; Kionga's engineering publication has independent editorial data and hosting. API documentation uses Swagger UI outside product navigation.
