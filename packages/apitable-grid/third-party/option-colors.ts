/**
 * APITable <https://github.com/apitable/apitable>
 * Copyright (C) 2022 APITable Ltd. <https://apitable.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

// Light-theme baseColor from createRainbowColorsArr: shade 100 then shade 200
// across deepPurple, indigo, blue, teal, green, yellow, orange, tangerine, pink, red.
// Hex values are the upstream color tokens. The vip shades (300–500) are not needed
// for the five host field kinds.

export const OPTION_COLORS = [
  "#DCD6FF", "#C2D3FF", "#C9EFFF", "#BDECDA", "#C7F5B1", "#FFF2C2", "#FFE5B7", "#FFD2A8", "#FFCED8", "#FFD1C8",
  "#CEC5FF", "#9CB9FF", "#9CE2FF", "#8BDDBE", "#9CE977", "#FFEBA6", "#FFD88A", "#FFAF66", "#FFB2C0", "#FFB4AF",
] as const;

export function optionColor(index: number): string {
  const safe = index < 0 ? 0 : index;
  return OPTION_COLORS[safe % OPTION_COLORS.length] ?? OPTION_COLORS[0];
}
