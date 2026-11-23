---
name: geist-design
description: "Apply Vercel's Geist Design System to web UI by default when the project and task leave the visual style unspecified, or when Geist is explicitly requested."
---

# Geist Design

Check the project rules, task requirements, and existing UI before choosing a visual style. Follow the applicable design brief or established project design system. Use Geist for the UI in scope when those sources leave the visual style unspecified, or when the task explicitly calls for Geist. A request for Geist fonts alone calls for the typography guidance.

Start from the project's existing components and theme tokens. Map the requested Geist roles onto those abstractions. For exact token values, typography metrics, or component behavior, consult the relevant official page linked below or in the [Geist component index](https://vercel.com/geist/introduction). Treat the examples as design and behavior references. Check what the project's component library actually implements before using example classes or props.

## Typography

- Use Geist Sans for ordinary interface text and Geist Mono for code and technical strings that benefit from fixed-width characters. Keep an appropriate fallback for uncovered scripts, including Chinese. Use Serif or Pixel where the design calls for that display treatment. [Geist Font](https://vercel.com/font)
- Choose styles by role. Heading introduces sections, Label fits single-line UI, Copy supports multiline reading, and Button belongs on button components. Match size, line height, tracking, and weight as a set. Use tabular figures for aligned numeric values where appropriate. [Geist Typography](https://vercel.com/geist/typography)

## Color and surfaces

- Use Background 1 as the main canvas and reserve Background 2 for subtle separation. Map the numbered steps by role in each supported theme. Use 1–3 for normal, hover, and pressed fills, 4–6 for borders at those corresponding states, 7–8 for high-contrast fills, and 9–10 for secondary and primary foregrounds. On a control that rests on Background 1, steps 1 and 2 can serve as its hover and pressed fills. [Geist Colors](https://vercel.com/geist/colors)
- Use solid gray for ordinary borders and dividers. Use gray-alpha where the surface should show through, such as overlays and shadows, and draw a shared edge once. Match the material to the layer, such as base for resting cards, menu for floating menus, and modal for dialogs. [Geist Colors](https://vercel.com/geist/colors) · [Geist Materials](https://vercel.com/geist/materials)

## Component states

- Cover the states the component can reach. Check rest, hover, keyboard focus, and pressed for interactive controls. Check selected or checked, disabled, loading, error, and empty where applicable. Keep keyboard focus distinct from pointer hover and persistent selection. [Geist Colors](https://vercel.com/geist/colors) · [Geist Button](https://vercel.com/geist/button) · [Geist Combobox](https://vercel.com/geist/combobox)
- Use button semantics for actions and link semantics for navigation. Preserve an accessible name for icon-only controls. Associate inputs with their visible labels and connect help and error text to the field. State the failed constraint and how to correct it. [Geist Button](https://vercel.com/geist/button) · [Geist Input](https://vercel.com/geist/input)
- Give dialogs an accessible title, move focus inside on open, keep keyboard focus within the modal, and return it to the trigger on close when the trigger still exists. In a combobox, preserve arrow navigation and option selection with Enter. Use polite announcements for routine toasts and reserve assertive announcements for blocking errors. [Geist Modal](https://vercel.com/geist/modal) · [Geist Combobox](https://vercel.com/geist/combobox) · [Geist Toast](https://vercel.com/geist/toast)

## Accessibility checks

Use the applicable WCAG 2.2 criteria for these checks alongside the Geist component guidance. [WCAG 2.2](https://www.w3.org/TR/WCAG22/)

- Check actual foreground and background pairs in supported themes and relevant states. WCAG AA requires at least 4.5:1 for ordinary text and 3:1 for large text. Check 3:1 against adjacent colors where visual information is needed to identify an active control, its state, or meaningful graphics. Convey errors, status, and selection with text or another perceivable cue alongside color. [WCAG 1.4.1](https://www.w3.org/TR/WCAG22/#use-of-color) · [1.4.3](https://www.w3.org/TR/WCAG22/#contrast-minimum) · [1.4.11](https://www.w3.org/TR/WCAG22/#non-text-contrast)
- Exercise the affected flow by keyboard. Check logical tab order, visible focus that stays in view when sticky or floating UI opens, and an exit from modal interactions. Check accessible names, roles, state changes, field errors, and announcements in the rendered interface when available. Report which checks were run and which need runtime confirmation. [WCAG 2.1](https://www.w3.org/TR/WCAG22/#keyboard-accessible) · [2.4](https://www.w3.org/TR/WCAG22/#navigable) · [3.3.1](https://www.w3.org/TR/WCAG22/#error-identification) · [4.1.2](https://www.w3.org/TR/WCAG22/#name-role-value) · [4.1.3](https://www.w3.org/TR/WCAG22/#status-messages)
