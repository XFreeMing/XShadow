# APITable grid sources

Upstream: APITable <https://github.com/apitable/apitable>
Commit: `c9255a752a85913e2e6b362ff6c306b0fc3585df`
License: GNU Affero General Public License v3.0, reproduced unmodified in `LICENSE`.
Copyright: APITable Ltd. <https://apitable.com>

Files in this directory keep the upstream file header. Each file notes the upstream path and the local import adaptation.

| Local file | Upstream path |
| --- | --- |
| `selection.ts` | `packages/core/src/modules/database/store/reducers/resource/datasheet/selection.ts` |
| `actions.ts` | `packages/core/src/modules/database/store/actions/resource/datasheet/selection.ts` (`setActiveCell`, `clearSelection`) and `datasheet.ts` (`setEditStatus`) |
| `editing.ts` | `isEditingCell` branch in `packages/core/src/modules/database/store/reducers/resource/datasheet/client.ts` |
| `selectors.ts` | `getDatasheetPack`, `getDatasheetClient`, `getActiveCell`, `getEditingCell` from `packages/core/src/modules/database/store/selectors/resource/datasheet/base.ts`; `getSelection` from `cell_range_calc.ts` |
| `option-colors.ts` | Light-theme shade 100 and 200 slice of `createRainbowColorsArr` in `packages/datasheet/src/pc/utils/color_utils.ts`, fed by `packages/components/src/colors/base/*.ts` |
| `grid-metrics.ts` | `GRID_FIELD_HEAD_HEIGHT`, `GRID_ROW_HEAD_WIDTH`, `GRID_CELL_VALUE_PADDING` from `packages/datasheet/src/pc/components/konva_grid/constant.ts`. Short row height is the coordinate default used by `model/__tests__/coordinate.test.ts` (32). |

DOM cell and header rules in `src/grid-css.ts` are copied from:

- `packages/datasheet/src/pc/components/multi_grid/cell/styles.module.less` (`.gridCellWrapper`, `.selected`)
- `packages/datasheet/src/pc/components/multi_grid/cell/cell_text/style.module.less`
- `packages/datasheet/src/pc/components/multi_grid/cell/cell_checkbox/style.module.less`
- `packages/datasheet/src/pc/components/multi_grid/cell/cell_options/style.module.less` (`.tabItem`)
- `packages/datasheet/src/pc/components/multi_grid/cell/cell_date_time/styles.module.less`
- `packages/datasheet/src/pc/components/multi_grid/header/styles.module.less` (`.fieldHeader`, `.fieldName`, `.select`)
- Light and dark tokens `--cellSelectedColor`, `--primaryColor`, `--shadowColor`, `--defaultBg`, `--fc1`, `--firstLevelText` from `packages/datasheet/src/pc/styles/lib_colors.css`

## What was not copied

`konva_grid_view.tsx` and `dom_grid.tsx` import `next/dynamic`, `@apitable/core` field engines, `resourceService` commands, field permissions, and the collaboration cursor layer. Those locks are not part of this package. The runnable grid is the DOM header, cell, and selection surface those views paint, driven by the selection and editing reducers above.

`isCellVisible` inside `getSelection` no longer calls the view-derivation selectors. It checks the host-supplied visible field and record ids stored on the local snapshot. Record values in that snapshot are a read-only mirror of the host query. The host callback remains the write path.

Permissions, members, and the APITable user module are not included.

## Compliance

This package is AGPL-3.0 because it contains and adapts APITable source. The host repository license file is unchanged. Copying these files into a local checkout does not decide whether a later public deployment of the combined service is allowed. Do not publish this package until that license question is resolved.
