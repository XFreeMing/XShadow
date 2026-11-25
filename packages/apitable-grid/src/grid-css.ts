// Copied rules from APITable c9255a752 DOM grid styles. Class names are prefixed
// so they do not collide with the host. See third-party/NOTICES.md for paths.
// Less mixins (.ellipsis, lib imports) are expanded to the properties they set.

import { GRID_CELL_VALUE_PADDING, GRID_COLUMN_WIDTH, GRID_FIELD_HEAD_HEIGHT, GRID_ROW_HEAD_WIDTH, GRID_ROW_HEIGHT } from "../third-party/grid-metrics";

export const GRID_STYLE_ID = "apitable-grid-style";

export const gridCss = `
.apitable-grid {
  display: block;
  border-collapse: collapse;
  --cellSelectedColor: #DCD6FF;
  --primaryColor: #7B67EE;
  --shadowColor: #E0E0E0;
  --defaultBg: #FFFFFF;
  --fc1: #262626;
  --firstLevelText: #262626;
  --warningColor: #FFAB00;
  --row-head: ${GRID_ROW_HEAD_WIDTH}px;
  --head-height: ${GRID_FIELD_HEAD_HEIGHT}px;
  --row-height: ${GRID_ROW_HEIGHT}px;
  --column-width: ${GRID_COLUMN_WIDTH}px;
  --cell-padding: ${GRID_CELL_VALUE_PADDING}px;
  color: var(--fc1);
  background: var(--defaultBg);
  overflow: auto;
  max-height: 100%;
  font-size: 13px;
}
.dark .apitable-grid {
  --cellSelectedColor: rgba(144, 127, 240, 0.16);
  --primaryColor: #907FF0;
  --shadowColor: rgba(61, 61, 61, 1);
  --defaultBg: rgba(26, 26, 26, 1);
  --fc1: rgba(255, 255, 255, 0.85);
  --firstLevelText: rgba(255, 255, 255, 0.85);
  --warningColor: rgba(255, 183, 82, 1);
}
.apitable-grid thead,
.apitable-grid tbody {
  display: block;
}
.apitable-grid__row {
  display: flex;
  min-width: max-content;
}
.apitable-grid__rowHead {
  flex: 0 0 var(--row-head);
  width: var(--row-head);
  box-sizing: border-box;
  border-bottom: 1px solid var(--shadowColor);
  border-right: 1px solid var(--shadowColor);
  background: var(--defaultBg);
  color: var(--firstLevelText);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
}
.apitable-grid__fieldHeader {
  display: flex;
  align-items: center;
  flex: 0 0 var(--column-width);
  width: var(--column-width);
  box-sizing: border-box;
  height: var(--head-height);
  padding: 0 var(--cell-padding);
  background: var(--defaultBg);
  border-bottom: 1px solid var(--shadowColor);
  border-right: 1px solid var(--shadowColor);
  border-top: 1px solid var(--shadowColor);
  position: relative;
}
.apitable-grid__fieldName {
  font-size: 13px;
  color: var(--firstLevelText);
  font-weight: bold;
  margin-left: 4px;
  width: 100%;
  background: transparent;
  border: 0;
  outline: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.apitable-grid__cell {
  flex: 0 0 var(--column-width);
  width: var(--column-width);
  min-height: var(--row-height);
  box-sizing: border-box;
  border-bottom: 1px solid var(--shadowColor);
  border-right: 1px solid var(--shadowColor);
  background: var(--defaultBg);
  line-height: 32px;
  position: relative;
  font-size: 13px;
  display: flex;
  align-items: stretch;
  color: var(--fc1);
}
.apitable-grid__selected {
  background: var(--cellSelectedColor);
}
.apitable-grid__active {
  outline: 2px solid var(--primaryColor);
  outline-offset: -2px;
  z-index: 1;
}
.apitable-grid__conflict {
  box-shadow: inset 0 0 0 2px var(--warningColor);
}
.apitable-grid__cellText {
  padding: 3px 10px;
  line-height: 24px;
  flex-grow: 1;
  width: 100%;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  outline: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.apitable-grid__numberCell {
  text-align: right;
}
.apitable-grid__dateTime {
  padding: 3px 10px;
  flex-grow: 1;
  line-height: 24px;
  width: 100%;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  outline: none;
}
.apitable-grid__checkboxCell {
  width: 100%;
  line-height: initial;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 8px;
  gap: 8px;
}
.apitable-grid__tabItem {
  display: inline-flex;
  align-items: center;
  padding: 0 8px;
  margin: 3px 8px 2px 0;
  max-width: 100%;
  font-size: 12px;
  height: 20px;
  line-height: 20px;
  border-radius: 10px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.apitable-grid__optionCell {
  display: flex;
  align-items: center;
  width: 100%;
  padding-left: 8px;
  gap: 4px;
}
.apitable-grid__optionSelect {
  flex: 1;
  min-width: 0;
  height: 24px;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
}
.apitable-grid__empty {
  padding: 12px;
  color: var(--firstLevelText);
}
`;
