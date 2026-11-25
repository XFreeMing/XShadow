import { MARK_GRADIENT, MARK_STOPS } from "../../lib/baishadow-brand";

interface BaiShadowGradientProps {
  id: string;
  stops: typeof MARK_STOPS;
  axis?: typeof MARK_GRADIENT;
}

export function BaiShadowGradient({ id, stops, axis = MARK_GRADIENT }: BaiShadowGradientProps) {
  return (
    <linearGradient id={id} gradientUnits="userSpaceOnUse" {...axis}>
      {stops.map(({ offset, color }) => <stop key={offset} offset={offset} stopColor={color} />)}
    </linearGradient>
  );
}
